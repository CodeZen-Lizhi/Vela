package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/app"
	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestCredentialsServiceRestart 通过真实应用服务和 SQLite 验证访问密钥及上游密钥保存、迁移和更新后重读。
func TestCredentialsServiceRestart(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	secrets, err := credentials.New(dir, func(name string) ([]byte, error) {
		calls++
		if name == "gateway-access-key" {
			return []byte("test-gateway-secret-123456"), nil
		}
		return nil, credentials.ErrNotFound
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := models.Open(filepath.Join(dir, "vela.sqlite"), http.DefaultClient, "http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 全局代理先按旧版写入，再接入加密，验证升级迁移保留内容。
	proxy := models.Proxy{Enabled: true, URL: "http://test-user:test-proxy-password@127.0.0.1:18888"}
	if err := db.SetProxy(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	if err := db.ProtectProxy(secrets); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetProxy(ctx); err != nil || got != proxy {
		t.Fatal("旧代理迁移失败")
	}
	upstreams, err := providers.New(db.DB(), credentialEntry{store: secrets}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	s := app.NewVelaService(app.Options{Credentials: secrets, Providers: upstreams})
	if ok, err := s.HasAccessKey(); err != nil || !ok {
		t.Fatal("旧访问密钥迁移失败")
	}
	if err := s.SaveAccessKey("test-gateway-updated-123456"); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProvider(ctx, providers.ProviderDraft{Name: "Test", URL: "https://example.invalid", Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyCustom, URL: proxy.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	multiplier := 1.0
	k, err := s.CreateProviderKey(ctx, p.ID, providers.ProviderKeyDraft{Name: "Test", Secret: "test-upstream-secret-123456", Multiplier: &multiplier, Proxy: providers.ProxyChoice{Mode: providers.ProxyCustom, URL: proxy.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := s.Providers(ctx); err != nil || snapshot.Providers[0].Provider.Request.Proxy.URL != proxy.URL {
		t.Fatal("供应商加密代理重读失败")
	}
	// 更新重启后旧存储即使不可用，也必须能读到最新访问密钥与上游凭据。
	secrets, err = credentials.New(dir, func(string) ([]byte, error) {
		t.Fatal("更新重启不应访问钥匙串")
		return nil, errors.New("denied")
	})
	if err != nil {
		t.Fatal(err)
	}
	upstreams, err = providers.New(db.DB(), credentialEntry{store: secrets}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	s = app.NewVelaService(app.Options{Version: "next-build", Credentials: secrets, Providers: upstreams})
	if value, err := s.RevealAccessKey(); err != nil || value != "test-gateway-updated-123456" {
		t.Fatal("重启后访问密钥不一致")
	}
	if value, err := upstreams.Secret(ctx, k.ID); err != nil || value != "test-upstream-secret-123456" {
		t.Fatal("重启后上游密钥不一致")
	}
	keys, err := s.ProviderKeys(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Proxy.URL != proxy.URL {
		t.Fatal("Key 加密代理重读失败")
	}
	data, err := json.Marshal(keys)
	if err != nil || strings.Contains(string(data), "test-upstream-secret-123456") {
		t.Fatal("列表暴露密钥")
	}
	if calls != 2 {
		t.Fatal("迁移调用次数不正确")
	}
	for _, path := range []string{"vela.sqlite", "credentials/secrets.enc", "credentials/master.key"} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"test-gateway-secret-123456", "test-gateway-updated-123456", "test-upstream-secret-123456"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("磁盘文件含明文凭据")
			}
		}
	}
	// SQLite 旧页可能保留迁移前字节；检查当前逻辑记录，不能承诺安全擦除已有明文。
	var plaintextRows int
	if err := db.DB().QueryRow(`SELECT count(*) FROM (SELECT document FROM settings UNION ALL SELECT document FROM providers UNION ALL SELECT document FROM provider_keys) WHERE document LIKE '%test-proxy-password%'`).Scan(&plaintextRows); err != nil || plaintextRows != 0 {
		t.Fatal("代理密码仍存在于数据库逻辑记录")
	}
}

// TestIsolatedCredentials 验证验收数据目录不接触日常钥匙串。
func TestIsolatedCredentials(t *testing.T) {
	if legacyCredentials(true) != nil {
		t.Fatal("隔离环境不应提供钥匙串迁移入口")
	}
}

// TestProviderDocumentMigration 验证旧版代理配置迁移、事务回滚及丢失主密钥后拒绝重建。
func TestProviderDocumentMigration(t *testing.T) {
	dir := t.TempDir()
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := models.Open(filepath.Join(dir, "vela.sqlite"), http.DefaultClient, "http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old, err := providers.New(db.DB(), credentialEntry{store: secrets})
	if err != nil {
		t.Fatal(err)
	}
	proxy := "http://test-user:test-migration-password@127.0.0.1:18888"
	p, err := old.Create(context.Background(), providers.ProviderDraft{Name: "Legacy", URL: "https://example.invalid", Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyCustom, URL: proxy}}})
	if err != nil {
		t.Fatal(err)
	}
	multiplier := 1.0
	_, err = old.CreateKey(context.Background(), p.ID, providers.ProviderKeyDraft{Name: "Legacy", Multiplier: &multiplier, Proxy: providers.ProxyChoice{Mode: providers.ProxyCustom, URL: proxy}})
	if err != nil {
		t.Fatal(err)
	}
	// 第二张表损坏时，第一张表的迁移也必须回滚。
	var before string
	if err := db.DB().QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("UPDATE provider_keys SET document='invalid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := providers.New(db.DB(), credentialEntry{store: secrets}, secrets); err == nil {
		t.Fatal("非法文档应拒绝迁移")
	}
	var after string
	if err := db.DB().QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&after); err != nil || after != before {
		t.Fatal("迁移失败没有回滚")
	}
	if _, err := db.DB().Exec(`UPDATE provider_keys SET document='{"name":"Legacy","proxy":{"mode":"provider"}}'`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := providers.New(db.DB(), credentialEntry{store: secrets}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := upgraded.Snapshot(context.Background())
	if err != nil || snapshot.Providers[0].Provider.Request.Proxy.URL != proxy {
		t.Fatal("迁移丢失代理配置")
	}
	if err := db.DB().QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&after); err != nil || strings.Contains(after, "test-migration-password") {
		t.Fatal("迁移未加密")
	}
	if err := os.Remove(filepath.Join(dir, "credentials", "master.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := providers.New(db.DB(), credentialEntry{store: secrets}, secrets); err == nil {
		t.Fatal("密文存在时不得重建主密钥")
	}
}
