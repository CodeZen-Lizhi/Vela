package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// authenticatedProxy 构造仅供本地测试的带用户名密码代理地址，不在失败日志中打印认证。
func authenticatedProxy(t *testing.T, base, user, password string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

// proxyDocument 读取一条资源完整文档，用于核对失败无副作用及密文保持。
func proxyDocument(t *testing.T, s *Store, table, id string) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow("SELECT document FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// assertProxyPrivate 核对序列化的页面响应或数据库字节不包含合成认证信息。
func assertProxyPrivate(t *testing.T, data []byte) {
	t.Helper()
	for _, secret := range []string{"fixture-proxy-user", "fixture-proxy-password", "fixture-key-proxy-user", "fixture-key-proxy-password"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("代理认证明文泄露")
		}
	}
}

// TestProxyCredentialPersistence 验证 Create/Update/Keys 的数据库密文、页面掩码、留空保留、无认证兼容及重启。
func TestProxyCredentialPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "proxy.sqlite")
	master := &memoryKeys{}
	s, closeDB := openStore(t, path, master)
	d := validDraft("proxy-private")
	d.Request.Proxy = ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")}
	p, err := s.Create(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Request.Proxy.URL != "***" {
		t.Fatal("Create回传代理认证")
	}
	raw := proxyDocument(t, s, "providers", p.ID)
	assertProxyPrivate(t, []byte(raw))
	if !strings.Contains(raw, proxyCipherPrefix) {
		t.Fatal("未加密供应商代理")
	}
	d.Request.Proxy = p.Request.Proxy
	if _, err = s.Update(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	if proxyDocument(t, s, "providers", p.ID) != raw {
		t.Fatal("保存掩码重写原代理")
	}
	d.Request.Proxy.URL = ""
	if _, err = s.Update(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	if proxyDocument(t, s, "providers", p.ID) != raw {
		t.Fatal("留空未保留原代理")
	}
	kd := keyDraft("key-proxy", "", 1)
	kd.Proxy = ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, "socks5://127.0.0.1:17891", "fixture-key-proxy-user", "fixture-key-proxy-password")}
	k, err := s.CreateKey(ctx, p.ID, kd)
	if err != nil {
		t.Fatal(err)
	}
	if k.Proxy.URL != "***" {
		t.Fatal("CreateKey回传代理认证")
	}
	keyRaw := proxyDocument(t, s, "provider_keys", k.ID)
	assertProxyPrivate(t, []byte(keyRaw))
	kd.Proxy = k.Proxy
	if _, err = s.UpdateKey(ctx, k.ID, kd); err != nil {
		t.Fatal(err)
	}
	kd.Proxy.URL = ""
	if _, err = s.UpdateKey(ctx, k.ID, kd); err != nil {
		t.Fatal(err)
	}
	if proxyDocument(t, s, "provider_keys", k.ID) != keyRaw {
		t.Fatal("更新Key未保留代理密文")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := s.Keys(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := json.Marshal([]any{snapshot, keys})
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPrivate(t, page)
	if strings.Contains(string(page), proxyCipherPrefix) || keys[0].Proxy.URL != "***" {
		t.Fatal("页面暴露密文或真实代理")
	}
	closeDB()
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPrivate(t, disk)
	s, closeDB = openStore(t, path, master)
	defer closeDB()
	keys, err = s.Keys(ctx, p.ID)
	if err != nil || keys[0].Proxy.URL != "***" {
		t.Fatal("重启无法读取代理掩码", err)
	}
	d.Request.Proxy = ProxyChoice{Mode: ProxyCustom, URL: "http://127.0.0.1:17892"}
	p, err = s.Update(ctx, p.ID, d)
	if err != nil || p.Request.Proxy.URL != d.Request.Proxy.URL {
		t.Fatal("无认证代理行为改变", err)
	}
	kd.Proxy = ProxyChoice{Mode: ProxyDirect}
	k, err = s.UpdateKey(ctx, k.ID, kd)
	if err != nil || k.Proxy.URL != "" {
		t.Fatal("直连未清理局部代理", err)
	}
	d.Name = "invalid-placeholder"
	d.Request.Proxy.URL = "***"
	expectError(t, sCreateError(ctx, s, d), "填写")
}

// sCreateError 只返回真实 Create 错误，便于复用现有业务错误断言。
func sCreateError(ctx context.Context, s *Store, d ProviderDraft) error {
	_, err := s.Create(ctx, d)
	return err
}

// TestProxyMasterKeyMissing 验证只有代理密文时也不允许丢失主密钥后重新生成或覆盖已有记录。
func TestProxyMasterKeyMissing(t *testing.T) {
	for _, resource := range []string{"provider", "key"} {
		t.Run(resource, func(t *testing.T) {
			ctx := context.Background()
			master := &memoryKeys{}
			s, closeDB := openStore(t, filepath.Join(t.TempDir(), "missing.sqlite"), master)
			defer closeDB()
			d := validDraft("master")
			p, err := s.Create(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			kd := keyDraft("proxy-only", "", 1)
			k, err := s.CreateKey(ctx, p.ID, kd)
			if err != nil {
				t.Fatal(err)
			}
			authenticated := ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")}
			if resource == "provider" {
				d.Request.Proxy = authenticated
				if _, err = s.Update(ctx, p.ID, d); err != nil {
					t.Fatal(err)
				}
			} else {
				kd.Proxy = authenticated
				if _, err = s.UpdateKey(ctx, k.ID, kd); err != nil {
					t.Fatal(err)
				}
			}
			beforeProvider := proxyDocument(t, s, "providers", p.ID)
			beforeKey := proxyDocument(t, s, "provider_keys", k.ID)
			missing := &memoryKeys{}
			reopened, err := New(s.db, missing)
			if err != nil {
				t.Fatal(err)
			}
			d.Name = "new-authenticated"
			d.Request.Proxy = authenticated
			expectError(t, sCreateError(ctx, reopened, d), "找不到")
			if resource == "provider" {
				d.Name = p.Name
				d.Request.Proxy.URL = "***"
				_, err = reopened.Update(ctx, p.ID, d)
				expectError(t, err, "找不到")
				_, err = reopened.Snapshot(ctx)
				expectError(t, err, "找不到")
			} else {
				kd.Proxy.URL = "***"
				_, err = reopened.UpdateKey(ctx, k.ID, kd)
				expectError(t, err, "找不到")
				_, err = reopened.Keys(ctx, p.ID)
				expectError(t, err, "找不到")
			}
			if missing.saves != 0 || proxyDocument(t, s, "providers", p.ID) != beforeProvider || proxyDocument(t, s, "provider_keys", k.ID) != beforeKey {
				t.Fatal("主密钥丢失后更改了密钥或记录")
			}
		})
	}
}

// TestLegacyProxyMigration 验证旧明文代理在重新装配时原子迁移，未知文档字段和无认证代理保留。
func TestLegacyProxyMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	master := &memoryKeys{}
	s, closeDB := openStore(t, path, master)
	p, err := s.Create(ctx, validDraft("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateKey(ctx, p.ID, keyDraft("legacy-key", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"providers", "provider_keys"} {
		id := p.ID
		proxyPath := "$.request.proxy.url"
		if table == "provider_keys" {
			id = k.ID
			proxyPath = "$.proxy.url"
		}
		modePath := strings.TrimSuffix(proxyPath, "url") + "mode"
		raw := authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")
		if _, err = s.db.Exec("UPDATE "+table+" SET document=json_set(document,?,? ,?,?,'$.futureField','preserved') WHERE id=?", modePath, string(ProxyCustom), proxyPath, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	closeDB()
	s, closeDB = openStore(t, path, master)
	for _, item := range []struct {
		// table 和 id 指定待检查的资源文档。
		table, id string
	}{{"providers", p.ID}, {"provider_keys", k.ID}} {
		raw := proxyDocument(t, s, item.table, item.id)
		assertProxyPrivate(t, []byte(raw))
		if !strings.Contains(raw, proxyCipherPrefix) || !strings.Contains(raw, "preserved") {
			t.Fatal("迁移丢失认证或未知配置")
		}
	}
	if _, err = s.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	closeDB()
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPrivate(t, disk)
}

// TestAuthenticatedProxyAccountTraffic 经真实本地认证代理验证供应商继承、Key 独立代理、组合保存及同步写回不泄露认证。
func TestAuthenticatedProxyAccountTraffic(t *testing.T) {
	ctx := context.Background()
	var providerHits, keyHits, directHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		fmt.Fprint(w, `{"effective_rate_multiplier":9}`)
	}))
	defer upstream.Close()
	providerProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-proxy-user:fixture-proxy-password"))
		if r.Header.Get("Proxy-Authorization") != want {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		providerHits.Add(1)
		if r.URL.Path == "/v1/usage" {
			fmt.Fprint(w, `{"remaining":7,"unit":"USD"}`)
		} else {
			fmt.Fprint(w, `{"effective_rate_multiplier":0}`)
		}
	}))
	defer providerProxy.Close()
	keyProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-key-proxy-user:fixture-key-proxy-password"))
		if r.Header.Get("Proxy-Authorization") != want {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		keyHits.Add(1)
		fmt.Fprint(w, `{"effective_rate_multiplier":2}`)
	}))
	defer keyProxy.Close()
	a, p, k, _ := accountFixture(t, upstream.URL)
	d := validDraft(p.Name)
	d.URL = upstream.URL
	d.Request.Proxy = ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, providerProxy.URL, "fixture-proxy-user", "fixture-proxy-password")}
	if _, err := a.store.Update(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	account, err := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "balance-only"}})
	if err != nil || account.Balance.Status.State != "success" || providerHits.Load() != 1 || directHits.Load() != 0 {
		t.Fatal("余额未经过认证代理", err)
	}
	query := ProviderKeyQueryDraft{ReuseProvider: true, MultiplierSource: "upstream"}
	result, err := a.SaveProviderKeyQuery(ctx, k.ID, query)
	if err != nil || result.MultiplierStatus.State != "success" || providerHits.Load() != 2 {
		t.Fatal("Key未继承供应商认证代理", err)
	}
	kd := keyDraft(k.Name, "", 1)
	kd.Proxy = ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, keyProxy.URL, "fixture-key-proxy-user", "fixture-key-proxy-password")}
	result, err = a.SaveKey(ctx, p.ID, k.ID, kd, query)
	if err != nil || result.MultiplierStatus.State != "success" || keyHits.Load() != 1 {
		t.Fatal("组合保存未使用Key独立认证代理", err)
	}
	raw := proxyDocument(t, a.store, "provider_keys", k.ID)
	assertProxyPrivate(t, []byte(raw))
	if !strings.Contains(raw, proxyCipherPrefix) {
		t.Fatal("查询同步把代理解密结果写回文档")
	}
	kd.Proxy.URL = "***"
	result, err = a.SaveKey(ctx, p.ID, k.ID, kd, query)
	if err != nil || result.MultiplierStatus.State != "success" || keyHits.Load() != 2 {
		t.Fatal("组合编辑掩码未保留代理", err)
	}
	result, err = a.SaveKey(ctx, p.ID, "", kd, query)
	if err == nil {
		t.Fatal("新Key接受未设置的认证占位")
	}
	kd.Secret = "sk-proxy-combined-created"
	kd.Proxy.URL = authenticatedProxy(t, keyProxy.URL, "fixture-key-proxy-user", "fixture-key-proxy-password")
	result, err = a.SaveKey(ctx, p.ID, "", kd, query)
	if err != nil || result.MultiplierStatus.State != "success" || keyHits.Load() != 3 {
		t.Fatal("组合新增未加密/使用代理", err)
	}
	assertProxyPrivate(t, []byte(proxyDocument(t, a.store, "provider_keys", result.KeyID)))
	kd.Proxy = ProxyChoice{Mode: ProxyDirect}
	kd.Secret = ""
	if _, err = a.store.UpdateKey(ctx, k.ID, kd); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RefreshProviderKey(ctx, k.ID); err != nil || directHits.Load() != 1 {
		t.Fatal("显式直连未覆盖认证代理", err)
	}
}

// TestProxyCipherBinding 验证代理密文不能挪用到其他记录，损坏数据不会被覆盖或回传页面。
func TestProxyCipherBinding(t *testing.T) {
	ctx := context.Background()
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "binding.sqlite"), &memoryKeys{})
	defer closeDB()
	d := validDraft("binding-one")
	d.Request.Proxy = ProxyChoice{Mode: ProxyCustom, URL: authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")}
	first, err := s.Create(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, validDraft("binding-two"))
	if err != nil {
		t.Fatal(err)
	}
	raw := proxyDocument(t, s, "providers", first.ID)
	if _, err = s.db.Exec("UPDATE providers SET document=? WHERE id=?", raw, second.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Snapshot(ctx)
	expectError(t, err, "解密失败")
	d.Name = second.Name
	d.Request.Proxy.URL = "***"
	_, err = s.Update(ctx, second.ID, d)
	expectError(t, err, "解密失败")
	if proxyDocument(t, s, "providers", second.ID) != raw {
		t.Fatal("损坏代理被覆盖")
	}
}

// TestProxyMigrationRollback 验证迁移第二条记录写入失败时第一条也回滚，不保留半套认证迁移。
func TestProxyMigrationRollback(t *testing.T) {
	ctx := context.Background()
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "rollback.sqlite"), &memoryKeys{})
	defer closeDB()
	p, err := s.Create(ctx, validDraft("rollback"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateKey(ctx, p.ID, keyDraft("rollback-key", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	auth := authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")
	if _, err = s.db.Exec("UPDATE providers SET document=json_set(document,'$.request.proxy.mode','custom','$.request.proxy.url',?) WHERE id=?", auth, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE provider_keys SET document=json_set(document,'$.proxy.mode','custom','$.proxy.url',?) WHERE id=?", auth, k.ID); err != nil {
		t.Fatal(err)
	}
	beforeProvider := proxyDocument(t, s, "providers", p.ID)
	beforeKey := proxyDocument(t, s, "provider_keys", k.ID)
	if _, err = s.db.Exec("CREATE TRIGGER reject_proxy_migration BEFORE UPDATE ON provider_keys BEGIN SELECT RAISE(ABORT,'fixture migration failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateProxyCredentials(ctx); err == nil {
		t.Fatal("迁移写入失败未返回错误")
	}
	if proxyDocument(t, s, "providers", p.ID) != beforeProvider || proxyDocument(t, s, "provider_keys", k.ID) != beforeKey {
		t.Fatal("迁移未完整回滚")
	}
}
