package providers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// localProviderKeys 以真实本地 credentials 存储上游主密钥，等价桌面适配而不依赖桌面包。
type localProviderKeys struct {
	// store 同时提供配置文档加密与上游主密钥存储。
	store *credentials.Store
}

// Load 保留本地凭据错误；仅不存在条目时转成尚未生成。
func (k localProviderKeys) Load() ([]byte, error) {
	v, err := k.store.Get("provider-master-key")
	if errors.Is(err, credentials.ErrNotFound) {
		return nil, ErrNoMasterKey
	}
	return v, err
}

// Save 使用真实加密文件保存主密钥。
func (k localProviderKeys) Save(v []byte) error { return k.store.Set("provider-master-key", v) }

// openLocalProvider 使用真实单连接 SQLite 和本地 credentials 打开完整供应商模块。
func openLocalProvider(t *testing.T, dir string) (*Store, *Accounts, func()) {
	t.Helper()
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "providers.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s, err := New(db, localProviderKeys{store: secrets}, secrets)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	a, err := NewAccounts(s, nil)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return s, a, func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}
}

// TestLocalDocumentsCombinedAccountLifecycle 验证整文档加密下账户、Key组合保存/普通更新/刷新/重启、代理实际出站及失钥保护。
func TestLocalDocumentsCombinedAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") == "" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		requests.Add(1)
		if r.URL.Path == "/v1/usage" {
			fmt.Fprint(w, `{"remaining":7,"unit":"CNY"}`)
		} else {
			fmt.Fprint(w, `{"effective_rate_multiplier":2}`)
		}
	}))
	defer proxy.Close()
	s, a, closeDB := openLocalProvider(t, dir)
	d := validDraft("local-credentials")
	d.URL = "http://upstream.test"
	d.Request.Proxy = ProxyChoice{Mode: ProxyGlobal}
	global := authenticatedProxy(t, proxy.URL, "fixture-proxy-user", "fixture-proxy-password")
	a.globalProxy = func(context.Context) (bool, string, error) { return true, global, nil }
	p, err := s.Create(ctx, d)
	if err != nil || p.Request.Proxy.Mode != ProxyGlobal {
		t.Fatal("加密供应商创建失败", err)
	}
	account, err := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-local-account"}})
	if err != nil || account.Balance.Status.State != "success" {
		t.Fatal("整文档加密下余额查询失败", err, account.Balance.Status, requests.Load())
	}
	kd := keyDraft("local-key", "fixture-local-call-key", 1)
	q := ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "manual", MultiplierSource: "upstream"}
	query, err := a.SaveKey(ctx, p.ID, "", kd, q)
	if err != nil || query.MultiplierStatus.State != "success" {
		t.Fatal("组合新增失败", err)
	}
	keys, err := s.Keys(ctx, p.ID)
	if err != nil || len(keys) != 1 || keys[0].Multiplier == nil || *keys[0].Multiplier != 2 {
		t.Fatal("查询倍率未保存", err)
	}
	kd.Secret = ""
	kd.Multiplier = nil
	if _, err = s.UpdateKey(ctx, query.KeyID, kd); err != nil {
		t.Fatal("普通更新未保留上游倍率", err)
	}
	if _, err = a.SaveKey(ctx, p.ID, query.KeyID, kd, q); err != nil {
		t.Fatal("组合编辑失败", err)
	}
	if _, err = a.SaveProviderKeyQuery(ctx, query.KeyID, q); err != nil {
		t.Fatal("独立查询配置保存失败", err)
	}
	d.Request.Proxy.Mode = ProxyGlobal
	if _, err = s.Update(ctx, p.ID, d); err != nil {
		t.Fatal("加密文档代理掩码编辑失败", err)
	}
	for _, item := range []struct {
		// table 和 id 指定供应商或Key完整文档。
		table, id string
	}{{"providers", p.ID}, {"provider_keys", query.KeyID}} {
		raw := proxyDocument(t, s, item.table, item.id)
		if !strings.HasPrefix(raw, "vela-enc-v1:") {
			t.Fatal("未保存整文档密文")
		}
		assertProxyPrivate(t, []byte(raw))
		var doc map[string]any
		if err = s.decodeDocument([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || snap.Defaults.FirstByteSeconds != 30 || snap.Defaults.KeyProbeMinutes != 10 {
		t.Fatal("快照或默认值错误", err)
	}
	before := requests.Load()
	closeDB()
	s, a, closeDB = openLocalProvider(t, dir)
	a.globalProxy = func(context.Context) (bool, string, error) { return true, global, nil }
	if _, err = a.RefreshProviderAccounts(ctx, p.ID); err != nil {
		t.Fatal("重启后账户刷新失败", err)
	}
	if _, err = a.RefreshProviderKey(ctx, query.KeyID); err != nil {
		t.Fatal("重启后Key刷新失败", err)
	}
	if requests.Load() <= before {
		t.Fatal("重启后没有真实代理请求")
	}
	keys, err = s.Keys(ctx, p.ID)
	if err != nil || len(keys) != 1 || *keys[0].Multiplier != 2 {
		t.Fatal("重启持久化失败", err)
	}
	var doc keyDocument
	raw := proxyDocument(t, s, "provider_keys", query.KeyID)
	if err = s.decodeDocument([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	originalProvider := proxyDocument(t, s, "providers", p.ID)
	closeDB()
	disk, err := os.ReadFile(filepath.Join(dir, "providers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPrivate(t, disk)
	for _, secret := range []string{"fixture-local-account", "fixture-local-call-key"} {
		if strings.Contains(string(disk), secret) {
			t.Fatal("凭据明文落库")
		}
	}
	if err = os.Remove(filepath.Join(dir, "credentials", "master.key")); err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "providers.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = New(db, localProviderKeys{store: secrets}, secrets); err == nil {
		t.Fatal("主密钥丢失仍能装配")
	}
	var after string
	if err = db.QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&after); err != nil || after != originalProvider {
		t.Fatal("失钥重写供应商密文", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "credentials", "master.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失钥后重新生成了主密钥")
	}
}

// TestLocalDocumentsLegacyMigration 验证旧JSON含认证代理迁入整文档密文，局部代理被移除且未知字段保留。
func TestLocalDocumentsLegacyMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, closeDB := openStore(t, filepath.Join(dir, "legacy.sqlite"), &memoryKeys{})
	p, err := s.Create(ctx, validDraft("legacy-document"))
	if err != nil {
		t.Fatal(err)
	}
	auth := authenticatedProxy(t, "http://127.0.0.1:17890", "fixture-proxy-user", "fixture-proxy-password")
	if _, err = s.db.Exec("UPDATE providers SET document=json_set(document,'$.request.proxy.mode','custom','$.request.proxy.url',?,'$.future','retained') WHERE id=?", auth, p.ID); err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := New(s.db, localProviderKeys{store: secrets}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	raw := proxyDocument(t, protected, "providers", p.ID)
	if !strings.HasPrefix(raw, "vela-enc-v1:") {
		t.Fatal("旧文档未迁移")
	}
	var doc map[string]any
	if err = protected.decodeDocument([]byte(raw), &doc); err != nil || doc["future"] != "retained" {
		t.Fatal("迁移丢失字段", err)
	}
	var saved providerDocument
	if err = protected.decodeDocument([]byte(raw), &saved); err != nil || saved.Request.Proxy.Mode != ProxyGlobal {
		t.Fatal("局部代理未迁为继承", err)
	}
	snapshot, err := protected.Snapshot(ctx)
	if err != nil || snapshot.Providers[0].Provider.Request.Proxy.Mode != ProxyGlobal {
		t.Fatal("迁移后出口错误", err)
	}
	closeDB()
}
