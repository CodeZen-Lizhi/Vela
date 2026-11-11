package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// authenticatedProxy 构造仅供本地测试的全局认证代理地址，不在失败日志打印认证。
func authenticatedProxy(t *testing.T, base, user, password string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

// proxyDocument 读取资源原始文档，用于断言事务及整文档加密仍有效。
func proxyDocument(t *testing.T, s *Store, table, id string) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow("SELECT document FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// assertProxyPrivate 保证数据库及页面内容不包含测试用全局认证秘密。
func assertProxyPrivate(t *testing.T, data []byte) {
	t.Helper()
	for _, secret := range []string{"fixture-proxy-user", "fixture-proxy-password"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("代理认证明文泄露")
		}
	}
}

// TestLocalProxyMigration 验证明文及已加密文档旧custom和Key global统一迁为继承，移除URL、保留未知字段并可重启。
func TestLocalProxyMigration(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprint(encrypted), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			s, closeDB := openStore(t, filepath.Join(dir, "migration.sqlite"), &memoryKeys{})
			defer closeDB()
			p, err := s.Create(ctx, validDraft("legacy"))
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"custom", "global", "provider", "direct"} {
				k, err := s.CreateKey(ctx, p.ID, keyDraft(mode, "fixture-key-"+mode, 1))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec("UPDATE provider_keys SET document=json_set(document,'$.proxy.mode',?,'$.proxy.url','vela-proxy:v1:obsolete','$.proxy.future',7,'$.future','retained') WHERE id=?", mode, k.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec("UPDATE providers SET document=json_set(document,'$.request.proxy.mode','custom','$.request.proxy.url','http://fixture-proxy-user:fixture-proxy-password@127.0.0.1:7890','$.request.proxy.future',7,'$.request.future',8,'$.future','retained') WHERE id=?", p.ID); err != nil {
				t.Fatal(err)
			}
			if encrypted {
				secrets, err := credentials.New(dir, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.protectDocuments(secrets); err != nil {
					t.Fatal(err)
				}
			}
			beforeKeySecrets := []string{}
			rows, err := s.db.Query("SELECT hex(secret) FROM provider_keys ORDER BY seq")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var secret string
				if err = rows.Scan(&secret); err != nil {
					t.Fatal(err)
				}
				beforeKeySecrets = append(beforeKeySecrets, secret)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if err = s.removeLocalProxies(ctx); err != nil {
				t.Fatal(err)
			}
			first := proxyDocument(t, s, "providers", p.ID)
			var pd map[string]any
			if err = s.decodeDocument([]byte(first), &pd); err != nil {
				t.Fatal(err)
			}
			request := pd["request"].(map[string]any)
			proxy := request["proxy"].(map[string]any)
			if pd["future"] != "retained" || request["future"] != float64(8) || proxy["future"] != float64(7) || proxy["mode"] != "global" {
				t.Fatal("迁移丢失其他字段")
			}
			if _, ok := proxy["url"]; ok {
				t.Fatal("供应商URL未删除")
			}
			if _, ok := request["name"]; ok {
				t.Fatal("迁移污染请求配置")
			}
			keys, err := s.Keys(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			for i, k := range keys {
				want := ProxyProvider
				if k.Name == "direct" {
					want = ProxyDirect
				}
				if k.Proxy.Mode != want {
					t.Fatal("Key模式迁移错误")
				}
				raw := proxyDocument(t, s, "provider_keys", k.ID)
				var kd map[string]any
				if err = s.decodeDocument([]byte(raw), &kd); err != nil {
					t.Fatal(err)
				}
				kp := kd["proxy"].(map[string]any)
				if _, ok := kp["url"]; ok {
					t.Fatal("Key URL未删除")
				}
				if kd["future"] != "retained" || kp["future"] != float64(7) {
					t.Fatal("Key其他字段丢失")
				}
				var secret string
				if err = s.db.QueryRow("SELECT hex(secret) FROM provider_keys WHERE id=?", k.ID).Scan(&secret); err != nil || secret != beforeKeySecrets[i] {
					t.Fatal("迁移更改调用密钥", err)
				}
			}
			if err = s.removeLocalProxies(ctx); err != nil || proxyDocument(t, s, "providers", p.ID) != first {
				t.Fatal("重复迁移不幂等", err)
			}
			if _, err = s.Snapshot(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestLocalProxyMigrationRollback 验证跨供应商和Key的迁移写入失败时全部回滚，旧认证即使不可解密也无需恢复。
func TestLocalProxyMigrationRollback(t *testing.T) {
	ctx := context.Background()
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "rollback.sqlite"), &memoryKeys{})
	defer closeDB()
	p, err := s.Create(ctx, validDraft("rollback"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateKey(ctx, p.ID, keyDraft("key", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE providers SET document=json_set(document,'$.request.proxy.mode','custom','$.request.proxy.url','vela-proxy:v1:obsolete')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE provider_keys SET document=json_set(document,'$.proxy.mode','global','$.proxy.url','old')"); err != nil {
		t.Fatal(err)
	}
	pBefore, kBefore := proxyDocument(t, s, "providers", p.ID), proxyDocument(t, s, "provider_keys", k.ID)
	if _, err = s.db.Exec("CREATE TRIGGER reject_local_proxy_migration BEFORE UPDATE ON provider_keys BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.removeLocalProxies(ctx); err == nil {
		t.Fatal("触发失败未返回")
	}
	if proxyDocument(t, s, "providers", p.ID) != pBefore || proxyDocument(t, s, "provider_keys", k.ID) != kBefore {
		t.Fatal("跨表迁移未完整回滚")
	}
}

// TestLocalProxyModesRejectObsolete 验证写入边界只接受两种模式，不恢复旧custom或Key global。
func TestLocalProxyModesRejectObsolete(t *testing.T) {
	ctx := context.Background()
	a, p := combinedFixture(t, "http://127.0.0.1:1")
	for _, mode := range []ProxyMode{"custom", ProxyProvider, ""} {
		d := validDraft("invalid")
		d.Request.Proxy.Mode = mode
		_, err := a.store.Create(ctx, d)
		expectError(t, err, "网络代理")
	}
	for _, mode := range []ProxyMode{"custom", ProxyGlobal, ""} {
		d := keyDraft("invalid", "", 1)
		d.Proxy.Mode = mode
		_, err := a.store.CreateKey(ctx, p.ID, d)
		expectError(t, err, "网络代理")
		_, err = a.SaveKey(ctx, p.ID, "", d, ProviderKeyQueryDraft{ReuseProvider: true})
		expectError(t, err, "网络代理")
	}
}

// TestGlobalProxyAccountInheritance 验证真实全局认证代理继承、全局动态变更与两级直连；直连时不读取全局凭据。
func TestGlobalProxyAccountInheritance(t *testing.T) {
	ctx := context.Background()
	var proxyHits, directHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		fmt.Fprint(w, `{"remaining":3,"effective_rate_multiplier":0}`)
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture-proxy-user:fixture-proxy-password")) {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		proxyHits.Add(1)
		fmt.Fprint(w, `{"remaining":7,"effective_rate_multiplier":2}`)
	}))
	defer proxy.Close()
	a, p, k, _ := accountFixture(t, upstream.URL)
	global := authenticatedProxy(t, proxy.URL, "fixture-proxy-user", "fixture-proxy-password")
	var globalReads atomic.Int32
	var rejectGlobal atomic.Bool
	a.globalProxy = func(context.Context) (bool, string, error) {
		globalReads.Add(1)
		if rejectGlobal.Load() {
			return false, "", errors.New("unreadable global")
		}
		return true, global, nil
	}
	account, err := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "balance-only"}})
	if err != nil || account.Balance.Status.State != "success" || proxyHits.Load() != 1 {
		t.Fatal("余额未继承全局认证代理", err)
	}
	q := ProviderKeyQueryDraft{ReuseProvider: true, MultiplierSource: "upstream"}
	result, err := a.SaveProviderKeyQuery(ctx, k.ID, q)
	if err != nil || result.MultiplierStatus.State != "success" || proxyHits.Load() != 2 {
		t.Fatal("Key未继承全局代理", err)
	}
	rejectGlobal.Store(true)
	reads := globalReads.Load()
	kd := keyDraft(k.Name, "", 1)
	kd.Proxy.Mode = ProxyDirect
	result, err = a.SaveKey(ctx, p.ID, k.ID, kd, q)
	if err != nil || result.MultiplierStatus.State != "success" || globalReads.Load() != reads || directHits.Load() != 1 {
		t.Fatal("Key直连读取全局凭据或没有真实直连", err)
	}
	d := validDraft(p.Name)
	d.URL = upstream.URL
	d.Request.Proxy.Mode = ProxyDirect
	if _, err = a.store.Update(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	account, err = a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api"}})
	// 保存余额配置还会同步复用它的 Key，两项都必须直连且不读取全局认证。
	if err != nil || account.Balance.Status.State != "success" || globalReads.Load() != reads || directHits.Load() != 3 {
		t.Fatal("供应商直连读取全局凭据", err)
	}
	page, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPrivate(t, page)
}
