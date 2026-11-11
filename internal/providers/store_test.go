package providers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// memoryKeys 是测试用的主密钥保存位置，代替系统钥匙串；fail 非空时模拟钥匙串读取失败。
type memoryKeys struct {
	value []byte
	saves int
	fail  error
}

// Load 返回已保存的主密钥，未保存时按契约返回 ErrNoMasterKey。
func (m *memoryKeys) Load() ([]byte, error) {
	if m.fail != nil {
		return nil, m.fail
	}
	if m.value == nil {
		return nil, ErrNoMasterKey
	}
	return m.value, nil
}

// Save 记录主密钥与保存次数。
func (m *memoryKeys) Save(value []byte) error {
	m.value = value
	m.saves++
	return nil
}

// openStore 经模型模块的真实入口打开 SQLite，再接入供应商模块，与桌面入口的装配顺序一致；返回关闭数据库的函数。
func openStore(t *testing.T, path string, keys KeyStore) (*Store, func()) {
	t.Helper()
	modelStore, err := models.Open(path, http.DefaultClient, "http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(modelStore.DB(), keys)
	if err != nil {
		t.Fatal(err)
	}
	return s, func() {
		if err := modelStore.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// validDraft 返回一份合法的供应商草稿，各用例在其上修改单个字段。
func validDraft(name string) ProviderDraft {
	return ProviderDraft{Name: name, URL: "https://api.example.com", Request: RequestPolicy{Proxy: ProxyChoice{Mode: ProxyGlobal}}}
}

// expectError 断言操作失败且提示包含给定文字。
func expectError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("错误 = %v，应包含 %q", err, want)
	}
}

// TestProviderLifecycle 在真实 SQLite 上验证新增、归一化、重名与非法输入、编辑、启停排序、重启重读与删除。
func TestProviderLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, closeDB := openStore(t, path, &memoryKeys{})

	snapshot, err := s.Snapshot(ctx)
	if err != nil || snapshot.Providers == nil || len(snapshot.Providers) != 0 {
		t.Fatalf("空列表 = %+v, %v", snapshot.Providers, err)
	}
	if snapshot.Defaults.StreamTotalMillis != 120_000 || snapshot.Defaults.Attempts != 1 || snapshot.Defaults.FirstByteSeconds != 30 || snapshot.Defaults.RequestSeconds != 300 {
		t.Fatalf("默认值 = %+v", snapshot.Defaults)
	}

	draft := validDraft("  Northstar  ")
	draft.Website = "northstar.example/"
	draft.URL = " https://API.Example.com/v1/ "
	first, err := s.Create(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Northstar" || first.Website != "https://northstar.example" || first.URL != "https://api.example.com" || !first.Enabled {
		t.Fatalf("归一化结果 = %+v", first)
	}

	_, err = s.Create(ctx, validDraft("Northstar"))
	expectError(t, err, "已存在")
	// 名称区分大小写，只有完全相同才算重名。
	second, err := s.Create(ctx, validDraft("northstar"))
	if err != nil {
		t.Fatal(err)
	}

	invalid := map[string]func(*ProviderDraft){
		"名称需为":      func(d *ProviderDraft) { d.Name = "   " },
		"请填写服务地址":   func(d *ProviderDraft) { d.URL = "" },
		"开头":        func(d *ProviderDraft) { d.URL = "api.example.com" },
		"账号密码":      func(d *ProviderDraft) { d.URL = "https://user:pass@api.example.com" },
		"查询参数":      func(d *ProviderDraft) { d.URL = "https://api.example.com/v1?key=1" },
		"站点根地址":     func(d *ProviderDraft) { d.URL = "https://api.example.com/openai" },
		"主站链接":      func(d *ProviderDraft) { d.Website = "ftp://example.com" },
		"总尝试次数":     func(d *ProviderDraft) { d.Request.Attempts = 100 },
		"流式请求总超时":   func(d *ProviderDraft) { d.Request.StreamTotalMillis = 999 },
		"首次响应超时":    func(d *ProviderDraft) { d.Request.FirstByteSeconds = 301 },
		"非流式请求超时":   func(d *ProviderDraft) { d.Request.RequestSeconds = -1 },
		"不能为负数":     func(d *ProviderDraft) { d.Request.MaxTransfers = -1 },
		"请选择网络代理方式": func(d *ProviderDraft) { d.Request.Proxy.Mode = ProxyProvider },
		"代理方式":      func(d *ProviderDraft) { d.Request.Proxy = ProxyChoice{Mode: "custom"} },
	}
	for want, change := range invalid {
		bad := validDraft("Invalid")
		change(&bad)
		_, err = s.Create(ctx, bad)
		expectError(t, err, want)
	}
	if snapshot, err = s.Snapshot(ctx); err != nil || len(snapshot.Providers) != 2 {
		t.Fatalf("非法输入不应写入，当前 %d 条, %v", len(snapshot.Providers), err)
	}

	edit := validDraft("Northstar Pro")
	edit.URL = "http://localhost:3000/v1beta"
	edit.Request = RequestPolicy{Attempts: 2, StreamTotalMillis: 300_125, MaxTransfers: 3, Proxy: ProxyChoice{Mode: ProxyDirect}}
	updated, err := s.Update(ctx, first.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Northstar Pro" || updated.URL != "http://localhost:3000" || updated.Website != "" || updated.Request.StreamTotalMillis != 300_125 || updated.Request.Proxy.Mode != ProxyDirect {
		t.Fatalf("编辑结果 = %+v", updated)
	}
	_, err = s.Update(ctx, first.ID, validDraft("northstar"))
	expectError(t, err, "已存在")
	_, err = s.Update(ctx, "missing", validDraft("Ghost"))
	expectError(t, err, "不存在")

	if err = s.SetEnabled(ctx, first.ID, false); err != nil {
		t.Fatal(err)
	}
	expectError(t, s.SetEnabled(ctx, "missing", true), "不存在")
	// 编辑不改变启停状态。
	if updated, err = s.Update(ctx, first.ID, edit); err != nil || updated.Enabled {
		t.Fatalf("编辑后启停 = %+v, %v", updated, err)
	}

	closeDB()
	s, closeDB = openStore(t, path, &memoryKeys{})
	defer closeDB()
	snapshot, err = s.Snapshot(ctx)
	if err != nil || len(snapshot.Providers) != 2 {
		t.Fatalf("重启重读 = %+v, %v", snapshot.Providers, err)
	}
	// 启用的排在前面，停用的在后。
	if got := snapshot.Providers[0].Provider; got.ID != second.ID || !got.Enabled {
		t.Fatalf("第一行 = %+v", got)
	}
	if got := snapshot.Providers[1].Provider; got != updated {
		t.Fatalf("重读内容 = %+v，应为 %+v", got, updated)
	}
	if health := snapshot.Providers[1].Health; health.State != ProviderHealthUnavailable || health.Percent != nil || health.Reason != "供应商已停用" {
		t.Fatalf("停用供应商的健康汇总 = %+v", health)
	}

	if err = s.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	expectError(t, s.Delete(ctx, first.ID), "不存在")
	if snapshot, err = s.Snapshot(ctx); err != nil || len(snapshot.Providers) != 1 || snapshot.Providers[0].Provider.ID != second.ID {
		t.Fatalf("删除后 = %+v, %v", snapshot.Providers, err)
	}
}

// keyDraft 返回一份合法的 Key 草稿。
func keyDraft(name, secret string, priority int) ProviderKeyDraft {
	multiplier := 1.0
	return ProviderKeyDraft{Name: name, Secret: secret, Multiplier: &multiplier, Priority: priority, CacheMinutes: 5, ProbeMinutes: 32, Proxy: ProxyChoice{Mode: ProxyProvider}}
}

// TestKeyLifecycle 验证上游 Key 的新增、掩码、密文落库、替换与保留密钥、重复密钥、复制、启停、重启重读及删除级联。
func TestKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	keys := &memoryKeys{}
	s, closeDB := openStore(t, path, keys)
	provider, err := s.Create(ctx, validDraft("Northstar"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, validDraft("Other"))
	if err != nil {
		t.Fatal(err)
	}

	list, err := s.Keys(ctx, provider.ID)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("空 Key 列表 = %+v, %v", list, err)
	}
	// 没有密钥的 Key 不需要主密钥，也不会生成。
	blank, err := s.CreateKey(ctx, provider.ID, keyDraft("  备用  ", "   ", 5))
	if err != nil || blank.HasSecret || blank.SecretMask != "" || blank.Name != "备用" || !blank.Enabled || !blank.AllModels || blank.HealthScore != 10 {
		t.Fatalf("无密钥的 Key = %+v, %v", blank, err)
	}
	if keys.saves != 0 {
		t.Fatal("没有密钥时不应生成主密钥")
	}

	const secret = "sk-northstar-0123456789abcdef"
	main, err := s.CreateKey(ctx, provider.ID, keyDraft("主力", " "+secret+" ", 2))
	if err != nil {
		t.Fatal(err)
	}
	if !main.HasSecret || main.SecretMask != "sk-north***cdef" || strings.Contains(main.SecretMask, "0123456789") {
		t.Fatalf("掩码 = %q", main.SecretMask)
	}
	if keys.saves != 1 {
		t.Fatalf("主密钥应只生成一次，实际 %d 次", keys.saves)
	}
	short, err := s.CreateKey(ctx, provider.ID, keyDraft("短密钥", "short-key-12", 9))
	if err != nil || short.SecretMask != "***" {
		t.Fatalf("不超过 12 个字符应全部掩码: %+v, %v", short, err)
	}

	_, err = s.CreateKey(ctx, provider.ID, keyDraft("重复", secret, 1))
	expectError(t, err, "名称：主力")
	// 不同供应商之间允许使用相同的密钥。
	if _, err = s.CreateKey(ctx, other.ID, keyDraft("同密钥", secret, 1)); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateKey(ctx, "missing", keyDraft("孤儿", "", 1))
	expectError(t, err, "供应商不存在")

	invalid := map[string]func(*ProviderKeyDraft){
		"Key 名称": func(d *ProviderKeyDraft) { d.Name = "" },
		"请填写倍率":  func(d *ProviderKeyDraft) { d.Multiplier = nil },
		"倍率需在":   func(d *ProviderKeyDraft) { v := 100.5; d.Multiplier = &v },
		"优先级":    func(d *ProviderKeyDraft) { d.Priority = -1 },
		"RPM":    func(d *ProviderKeyDraft) { d.RPMLimit = 10001 },
		"并发上限":   func(d *ProviderKeyDraft) { d.ConcurrencyLimit = -1 },
		"缓存 TTL": func(d *ProviderKeyDraft) { d.CacheMinutes = 61 },
		"熔断探测":   func(d *ProviderKeyDraft) { d.ProbeMinutes = 33 },
		"网络代理":   func(d *ProviderKeyDraft) { d.Proxy.Mode = "" },
	}
	for want, change := range invalid {
		bad := keyDraft("非法", "", 1)
		change(&bad)
		_, err = s.CreateKey(ctx, provider.ID, bad)
		expectError(t, err, want)
	}

	// 密钥留空表示不修改：名称与倍率更新，掩码不变。
	edit := keyDraft("主力改名", "", 2)
	zero := 0.0
	edit.Multiplier = &zero
	edit.RPMLimit = 60
	edited, err := s.UpdateKey(ctx, main.ID, edit)
	if err != nil || edited.Name != "主力改名" || edited.SecretMask != main.SecretMask || edited.Multiplier == nil || *edited.Multiplier != 0 || edited.RPMLimit != 60 {
		t.Fatalf("保留密钥的编辑 = %+v, %v", edited, err)
	}
	if got, err := s.Secret(ctx, main.ID); err != nil || got != secret {
		t.Fatalf("复制的密钥 = %q, %v", got, err)
	}
	// 编辑为同供应商另一把 Key 的密钥应被拒绝；改回自己的原密钥不算重复。
	_, err = s.UpdateKey(ctx, short.ID, keyDraft("短密钥", secret, 9))
	expectError(t, err, "名称：主力改名")
	if _, err = s.UpdateKey(ctx, main.ID, keyDraft("主力改名", secret, 2)); err != nil {
		t.Fatal(err)
	}
	const replaced = "sk-replaced-abcdefghijklmnop"
	if edited, err = s.UpdateKey(ctx, main.ID, keyDraft("主力改名", replaced, 2)); err != nil || edited.SecretMask != "sk-repla***mnop" {
		t.Fatalf("替换密钥 = %+v, %v", edited, err)
	}
	_, err = s.Secret(ctx, blank.ID)
	expectError(t, err, "尚未设置密钥")
	_, err = s.UpdateKey(ctx, "missing", keyDraft("x", "", 1))
	expectError(t, err, "不存在")

	if err = s.SetKeyEnabled(ctx, blank.ID, false); err != nil {
		t.Fatal(err)
	}
	expectError(t, s.SetKeyEnabled(ctx, "missing", true), "不存在")

	// 数据库文件与主密钥保存位置中都不应出现密钥明文。
	closeDB()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{secret, replaced, "short-key-12"} {
		if strings.Contains(string(raw), plain) || strings.Contains(string(keys.value), plain) {
			t.Fatalf("发现明文密钥 %q", plain)
		}
	}

	// 重启后用同一把主密钥仍可读回：顺序为创建顺序，内容与编辑结果一致。
	s, closeDB = openStore(t, path, &memoryKeys{value: keys.value})
	defer closeDB()
	list, err = s.Keys(ctx, provider.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("重启重读 = %+v, %v", list, err)
	}
	// 倍率是指针字段，按指向的值比较。
	if list[0].ID != blank.ID || list[0].Enabled || !reflect.DeepEqual(list[1], edited) || list[2].ID != short.ID {
		t.Fatalf("重读的列表 = %+v", list)
	}
	if got, err := s.Secret(ctx, main.ID); err != nil || got != replaced {
		t.Fatalf("重启后复制的密钥 = %q, %v", got, err)
	}

	// 概览：首选 Key 取已启用的 Key 中优先级数值最小的一把；停用的 Key 不参与。
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overview := snapshot.Providers[0]
	if overview.Provider.ID != provider.ID || overview.KeyCount != 3 || overview.PreferredKey == nil || overview.PreferredKey.Name != "主力改名" {
		t.Fatalf("概览 = %+v", overview)
	}
	if overview.Health.State != ProviderHealthNormal || overview.Health.Percent == nil || *overview.Health.Percent != 100 {
		t.Fatalf("健康汇总 = %+v", overview.Health)
	}
	if err = s.SetKeyEnabled(ctx, main.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetKeyEnabled(ctx, short.ID, false); err != nil {
		t.Fatal(err)
	}
	if snapshot, err = s.Snapshot(ctx); err != nil || snapshot.Providers[0].PreferredKey != nil || snapshot.Providers[0].Health.Reason != "没有已启用的 Key" {
		t.Fatalf("全部停用后的概览 = %+v, %v", snapshot.Providers[0], err)
	}

	if err = s.DeleteKey(ctx, short.ID); err != nil {
		t.Fatal(err)
	}
	expectError(t, s.DeleteKey(ctx, short.ID), "不存在")
	// 删除供应商时它的 Key 一并删除，其他供应商的 Key 不受影响。
	if err = s.Delete(ctx, provider.ID); err != nil {
		t.Fatal(err)
	}
	var remaining, orphans int
	if err = s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(provider_id=?),0) FROM provider_keys", provider.ID).Scan(&remaining, &orphans); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 || orphans != 0 {
		t.Fatalf("删除供应商后剩余 Key %d 把，其中属于已删除供应商 %d 把", remaining, orphans)
	}
}

// TestMasterKeyProtection 验证主密钥缺失、读取失败或不匹配时明确报错，且不生成新主密钥、不改动已保存的密文。
func TestMasterKeyProtection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	keys := &memoryKeys{}
	s, closeDB := openStore(t, path, keys)
	provider, err := s.Create(ctx, validDraft("Northstar"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateKey(ctx, provider.ID, keyDraft("主力", "sk-protected-0123456789", 1))
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err = s.db.QueryRow("SELECT secret FROM provider_keys WHERE id=?", key.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	closeDB()

	// sealedNow 重新读取密文，用于确认失败的操作没有改动它。
	sealedNow := func(s *Store) string {
		t.Helper()
		var now []byte
		if err := s.db.QueryRow("SELECT secret FROM provider_keys WHERE id=?", key.ID).Scan(&now); err != nil {
			t.Fatal(err)
		}
		return string(now)
	}

	// 主密钥丢失：读取、复制、新增带密钥的 Key 与替换密钥都失败，且不会生成新的主密钥。
	lost := &memoryKeys{}
	s, closeDB = openStore(t, path, lost)
	_, err = s.Keys(ctx, provider.ID)
	expectError(t, err, "找不到凭据密钥")
	_, err = s.Secret(ctx, key.ID)
	expectError(t, err, "找不到凭据密钥")
	_, err = s.CreateKey(ctx, provider.ID, keyDraft("新 Key", "sk-another-0123456789", 2))
	expectError(t, err, "找不到凭据密钥")
	_, err = s.UpdateKey(ctx, key.ID, keyDraft("主力", "sk-rotated-0123456789", 1))
	expectError(t, err, "找不到凭据密钥")
	if lost.saves != 0 || sealedNow(s) != string(before) {
		t.Fatal("主密钥丢失时不应生成新主密钥或改动密文")
	}
	// 不涉及密钥的操作仍可进行：概览可读，没有密钥的 Key 可以新增。
	if _, err = s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKey(ctx, provider.ID, keyDraft("无密钥", "", 3)); err != nil {
		t.Fatal(err)
	}
	closeDB()

	// 钥匙串读取失败：报错并保留原因。
	s, closeDB = openStore(t, path, &memoryKeys{fail: errors.New("keychain locked")})
	_, err = s.Keys(ctx, provider.ID)
	expectError(t, err, "keychain locked")
	closeDB()

	// 主密钥内容不对：格式错误与不匹配的密钥都不能解密，也不改动密文。
	s, closeDB = openStore(t, path, &memoryKeys{value: []byte("not-a-key")})
	_, err = s.Keys(ctx, provider.ID)
	expectError(t, err, "格式不正确")
	closeDB()
	s, closeDB = openStore(t, path, &memoryKeys{value: []byte(strings.Repeat("ab", masterKeySize))})
	defer closeDB()
	_, err = s.Secret(ctx, key.ID)
	expectError(t, err, "不匹配")
	if sealedNow(s) != string(before) {
		t.Fatal("解密失败不应改动密文")
	}

	// 密文与所属 Key 绑定：挪到另一把 Key 上无法解密。
	master, err := (&vault{store: keys}).load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = open(master, secretBinding("another-key"), before); err == nil {
		t.Fatal("密文不应能在其他 Key 上解密")
	}
	if plain, err := open(master, secretBinding(key.ID), before); err != nil || string(plain) != "sk-protected-0123456789" {
		t.Fatalf("原 Key 上解密 = %q, %v", plain, err)
	}
}
