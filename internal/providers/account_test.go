package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/settings"
)

// accountFixture 建立真实 SQLite 和两个独立 Key，使用内存主密钥替代系统钥匙串。
func accountFixture(t *testing.T, base string) (*Accounts, Provider, ProviderKey, ProviderKey) {
	t.Helper()
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "accounts.sqlite"), &memoryKeys{})
	t.Cleanup(closeDB)
	a, e := NewAccounts(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	d := validDraft("account")
	d.URL = base
	p, e := s.Create(context.Background(), d)
	if e != nil {
		t.Fatal(e)
	}
	k, e := s.CreateKey(context.Background(), p.ID, keyDraft("local", "sk-wanted-full-secret", 1))
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.CreateKey(context.Background(), p.ID, keyDraft("manual", "sk-other-full-secret", 2))
	if e != nil {
		t.Fatal(e)
	}
	return a, p, k, other
}

// TestAccountUsagePersistence 验证真实 HTTP、原币种、有效零、失败保留、掩码、留空不改和重开持久化。
func TestAccountUsagePersistence(t *testing.T) {
	ctx := context.Background()
	var fail atomic.Bool
	var auth atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization") == "Bearer fixture-sensitive-token")
		if fail.Load() {
			fmt.Fprint(w, `{"isValid":false}`)
			return
		}
		fmt.Fprint(w, `{"remaining":0,"unit":"CNY","is_active":true}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	keys := &memoryKeys{}
	s, closeDB := openStore(t, path, keys)
	a, e := NewAccounts(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	d := validDraft("usage")
	d.URL = server.URL
	p, e := s.Create(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	draft := ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-sensitive-token"}}
	result, e := a.SaveProviderAccount(ctx, p.ID, draft)
	if e != nil {
		t.Fatal(e)
	}
	if !auth.Load() || result.Balance.Amount == nil || *result.Balance.Amount != 0 || result.Balance.Currency != "CNY" || result.Config.APIKey != "***" {
		t.Fatalf("unexpected result: %+v", result)
	}
	fail.Store(true)
	draft.Config.APIKey = ""
	result, e = a.SaveProviderAccount(ctx, p.ID, draft)
	if e != nil {
		t.Fatal(e)
	}
	if result.Balance.Status.State != "failed" || result.Balance.Amount == nil || *result.Balance.Amount != 0 || result.Balance.Status.LastSuccess.IsZero() {
		t.Fatalf("failure lost last value: %+v", result)
	}
	closeDB()
	disk, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(disk), "fixture-sensitive-token") {
		t.Fatal("plaintext credential persisted")
	}
	s, closeDB = openStore(t, path, keys)
	defer closeDB()
	a, e = NewAccounts(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e = a.ProviderAccount(ctx, p.ID)
	if e != nil || result.Balance.Status.State != "failed" || result.Config.APIKey != "***" {
		t.Fatalf("reopen: %+v %v", result, e)
	}
	if e = s.Delete(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.db.QueryRow("SELECT COUNT(*) FROM provider_accounts").Scan(&count); e != nil || count != 0 {
		t.Fatalf("cascade %d %v", count, e)
	}
}

// TestAccountCustomIsolation 验证自定义 POST 认证加密、独立来源、零倍率及部分字段失败保留。
func TestAccountCustomIsolation(t *testing.T) {
	ctx := context.Background()
	var missing atomic.Bool
	var valid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		valid.Store(r.Method == "POST" && r.Header.Get("X-Secret") == "header-fixture" && body["token"] == "body-fixture")
		if missing.Load() {
			fmt.Fprint(w, `{"data":{"name":"new-name","rate":null}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"name":"upstream-name","rate":0,"group":"G","balance":5}}`)
	}))
	defer server.Close()
	a, p, k, other := accountFixture(t, server.URL)
	config := QueryConfig{Mode: "custom", Method: "POST", URL: server.URL, Headers: `{"X-Secret":"header-fixture"}`, Body: `{"token":"body-fixture"}`, BalancePath: "data.balance"}
	account, e := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: config})
	if e != nil || account.Balance.Amount == nil || *account.Balance.Amount != 5 {
		t.Fatalf("balance: %+v %v", account, e)
	}
	draft := ProviderKeyQueryDraft{Config: config, NameSource: "upstream_key", MultiplierSource: "upstream", NamePath: "data.name", MultiplierPath: "data.rate", GroupPath: "data.group"}
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, draft)
	if e != nil {
		t.Fatal(e)
	}
	if !valid.Load() || q.Config.Body != "***" || q.MultiplierStatus.State != "success" {
		t.Fatalf("query: %+v", q)
	}
	keys, e := a.store.Keys(ctx, p.ID)
	if e != nil || keys[0].Name != "upstream-name" || keys[0].Multiplier == nil || *keys[0].Multiplier != 0 || keys[1].ID != other.ID || keys[1].Name != "manual" {
		t.Fatalf("keys: %+v %v", keys, e)
	}
	missing.Store(true)
	q, e = a.RefreshProviderKey(ctx, k.ID)
	if e != nil || q.NameStatus.State != "success" || q.MultiplierStatus.State != "failed" {
		t.Fatalf("partial: %+v %v", q, e)
	}
	keys, e = a.store.Keys(ctx, p.ID)
	if e != nil || keys[0].Name != "new-name" || *keys[0].Multiplier != 0 {
		t.Fatalf("preservation: %+v %v", keys, e)
	}
	draft.ReuseProvider = true
	draft.KeyPath = "data.key"
	q, e = a.SaveProviderKeyQuery(ctx, k.ID, draft)
	if e != nil || q.NameStatus.State != "failed" {
		t.Fatalf("unowned response updated: %+v %v", q, e)
	}
	draft.Config.Body = "{"
	draft.ReuseProvider = false
	if _, e = a.SaveProviderKeyQuery(ctx, k.ID, draft); e == nil {
		t.Fatal("invalid JSON accepted")
	}
	var raw string
	if e = a.store.db.QueryRow("SELECT document FROM provider_key_queries WHERE key_id=?", k.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "header-fixture") || strings.Contains(raw, "body-fixture") {
		t.Fatal("plaintext custom secrets")
	}
}

// TestAccountSub2APIRotation 验证完整 Key 匹配、专属零倍率、名称独立、订阅失败不抹余额及令牌轮换。
func TestAccountSub2APIRotation(t *testing.T) {
	ctx := context.Background()
	var refreshes atomic.Int32
	var duplicate atomic.Bool
	var wrongAuth atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" && r.Header.Get("Authorization") != "Bearer access-fixture" {
			wrongAuth.Store(true)
		}
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshes.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"access-fixture","refresh_token":"rotated-fixture"}}`)
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"code":0,"data":{"balance":12,"points":3}}`)
		case "/api/v1/subscriptions/summary":
			w.WriteHeader(503)
		case "/api/v1/keys":
			if duplicate.Load() {
				fmt.Fprint(w, `{"data":{"total":2,"items":[{"key":"sk-wanted-full-secret","name":"duplicate","group_id":1},{"key":"sk-wanted-full-secret","group_id":1}]}}`)
			} else {
				fmt.Fprint(w, `{"data":{"total":2,"items":[{"key":"sk-wanted-full-secret","name":"remote-key","group_id":1},{"key":"sk-other-full-secret","name":"same-group","group_id":1}]}}`)
			}
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"data":[{"id":1,"name":"remote-group","rate_multiplier":1}]}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{"1":0}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, p, k, _ := accountFixture(t, server.URL)
	if _, e := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "original-fixture"}}); e != nil {
		t.Fatal(e)
	}
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "upstream_group", MultiplierSource: "upstream"})
	if e != nil || q.NameStatus.State != "success" || q.MultiplierStatus.State != "success" {
		t.Fatalf("sync %+v %v", q, e)
	}
	refreshes.Store(0)
	result, e := a.RefreshProviderAccounts(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := result.Accounts[0].Balance
	if b.Amount == nil || *b.Amount != 15 || b.Cash == nil || *b.Cash != 12 || b.Points == nil || *b.Points != 3 || b.SubscriptionStatus.State != "failed" || refreshes.Load() != 1 || wrongAuth.Load() {
		t.Fatalf("balance/auth %+v calls=%d", b, refreshes.Load())
	}
	c, _, _, e := a.accountConfig(ctx, "provider_accounts", p.ID)
	if e != nil || c.RefreshToken != "rotated-fixture" {
		t.Fatal("rotation not persisted", e)
	}
	duplicate.Store(true)
	q, e = a.RefreshProviderKey(ctx, k.ID)
	if e != nil || q.NameStatus.State != "failed" {
		t.Fatalf("duplicate %+v %v", q, e)
	}
	keys, e := a.store.Keys(ctx, p.ID)
	if e != nil || keys[0].Name != "remote-group" || *keys[0].Multiplier != 0 {
		t.Fatalf("duplicate changed values %+v %v", keys, e)
	}
}

// TestAccountNewAPI 验证额度换算、完整密钥还原、sk 前缀处理和分组接口回退；空分组仍同步名称。
func TestAccountNewAPI(t *testing.T) {
	ctx := context.Background()
	var auto atomic.Bool
	var checkins atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("New-Api-User") != "42" || r.Header.Get("Authorization") != "Bearer system-fixture" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":2500000,"used_quota":500000}}`)
		case "/api/user/checkin":
			checkins.Add(1)
			w.WriteHeader(500)
		case "/api/token/":
			group := "G"
			if auto.Load() {
				group = "auto"
			}
			fmt.Fprintf(w, `{"success":true,"data":{"total":1,"items":[{"id":9,"key":"***","name":"new-name","group":%q}]}}`, group)
		case "/api/token/9/key":
			if r.Method != "POST" {
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"success":true,"data":{"key":"wanted-full-secret"}}`)
		case "/api/user/self/groups":
			w.WriteHeader(404)
		case "/api/user/groups":
			fmt.Fprint(w, `{"success":true,"data":{"G":{"ratio":120}}}`)
		}
	}))
	defer server.Close()
	a, p, k, _ := accountFixture(t, server.URL)
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "newapi", UserID: "42", AccessToken: "system-fixture"}}
	account, e := a.SaveProviderAccount(ctx, p.ID, d)
	if e != nil || account.Balance.Amount == nil || *account.Balance.Amount != 5 || *account.Balance.Used != 1 || checkins.Load() != 0 {
		t.Fatalf("conversion %+v %v", account, e)
	}
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "upstream_key", MultiplierSource: "upstream"})
	if e != nil || q.MultiplierStatus.State != "success" {
		t.Fatalf("key %+v %v", q, e)
	}
	keys, e := a.store.Keys(ctx, p.ID)
	if e != nil || *keys[0].Multiplier != 120 {
		t.Fatalf("rate %+v %v", keys, e)
	}
	auto.Store(true)
	q, e = a.RefreshProviderKey(ctx, k.ID)
	if e != nil || q.NameStatus.State != "success" || q.MultiplierStatus.State != "failed" {
		t.Fatalf("auto %+v %v", q, e)
	}
	d.Checkin = true
	account, e = a.SaveProviderAccount(ctx, p.ID, d)
	if e != nil || account.Balance.Status.State != "success" || account.Balance.Checkin.State != "failed" || checkins.Load() != 1 {
		t.Fatalf("checkin %+v %v", account, e)
	}
}

// TestAccountStaleKeyResult 用真实在途 HTTP 证明替换 Key 后旧结果不写回且网络未占用 Store 锁。
func TestAccountStaleKeyResult(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var block atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			started <- struct{}{}
			<-release
			fmt.Fprint(w, `{"effective_rate_multiplier":9}`)
			return
		}
		fmt.Fprint(w, `{"effective_rate_multiplier":3}`)
	}))
	defer server.Close()
	a, p, k, _ := accountFixture(t, server.URL)
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api", BaseURL: server.URL}, MultiplierSource: "upstream"})
	if e != nil || q.MultiplierStatus.State != "success" {
		t.Fatal(e)
	}
	block.Store(true)
	done := make(chan error, 1)
	go func() { _, e := a.RefreshProviderKey(ctx, k.ID); done <- e }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no HTTP request")
	}
	update := keyDraft("replacement", "sk-new-secret", 1)
	updated := make(chan error, 1)
	go func() { _, e := a.store.UpdateKey(ctx, k.ID, update); updated <- e }()
	select {
	case e := <-updated:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network held Store lock")
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	keys, e := a.store.Keys(ctx, p.ID)
	if e != nil || len(keys) != 2 || keys[0].Name != "replacement" || keys[0].Multiplier == nil || *keys[0].Multiplier != 3 {
		t.Fatalf("stale overwrote %+v %v", keys, e)
	}
}

// TestAccountRunCancellation 验证退出取消后台任务且关闭定时不发请求。
func TestAccountRunCancellation(t *testing.T) {
	a, _, _, _ := accountFixture(t, "https://example.com")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := a.Run(ctx, func(context.Context) (settings.AccountRefresh, error) { return settings.DefaultAccountRefresh(), nil }); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

// TestAccountMasterKeyLoss 验证只有账户密文而没有调用 Key 时，丢失主密钥也不能生成新密钥或改写旧数据。
func TestAccountMasterKeyLoss(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"balance":1}`) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	keys := &memoryKeys{}
	s, closeDB := openStore(t, path, keys)
	a, e := NewAccounts(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	d := validDraft("only-account")
	d.URL = server.URL
	p, e := s.Create(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	draft := ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-original"}}
	if _, e = a.SaveProviderAccount(ctx, p.ID, draft); e != nil {
		t.Fatal(e)
	}
	var before []byte
	if e = s.db.QueryRow("SELECT secret FROM provider_accounts WHERE provider_id=?", p.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	closeDB()
	missing := &memoryKeys{}
	s, closeDB = openStore(t, path, missing)
	defer closeDB()
	a, e = NewAccounts(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.ProviderAccount(ctx, p.ID); e == nil {
		t.Fatal("missing master key accepted")
	}
	if _, e = a.SaveProviderAccount(ctx, p.ID, draft); e == nil {
		t.Fatal("lost credential replaced")
	}
	if _, e = s.CreateKey(ctx, p.ID, keyDraft("cannot-seal", "fixture-new-secret", 1)); e == nil {
		t.Fatal("generated new master key despite account ciphertext")
	}
	if missing.saves != 0 {
		t.Fatal("replacement master key created")
	}
	var after []byte
	if e = s.db.QueryRow("SELECT secret FROM provider_accounts WHERE provider_id=?", p.ID).Scan(&after); e != nil || string(before) != string(after) {
		t.Fatal("ciphertext changed", e)
	}
}

// TestAccountProxyChoice 通过实际 HTTP 代理证明余额继承全局出口，Key 直连可覆盖供应商继承。
func TestAccountProxyChoice(t *testing.T) {
	ctx := context.Background()
	var direct, proxied atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		fmt.Fprint(w, `{"effective_rate_multiplier":0}`)
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		fmt.Fprint(w, `{"remaining":7,"unit":"CNY"}`)
	}))
	defer proxy.Close()
	a, p, k, _ := accountFixture(t, upstream.URL)
	d := validDraft(p.Name)
	d.URL = upstream.URL
	d.Request.Proxy = ProxyChoice{Mode: ProxyGlobal}
	a.globalProxy = func(context.Context) (bool, string, error) { return true, proxy.URL, nil }
	if _, e := a.store.Update(ctx, p.ID, d); e != nil {
		t.Fatal(e)
	}
	account, e := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "balance-only"}})
	if e != nil || account.Balance.Amount == nil || *account.Balance.Amount != 7 || proxied.Load() != 1 || direct.Load() != 0 {
		t.Fatalf("proxy balance %+v %v", account, e)
	}
	kd := keyDraft(k.Name, "", 1)
	kd.Proxy = ProxyChoice{Mode: ProxyDirect}
	if _, e = a.store.UpdateKey(ctx, k.ID, kd); e != nil {
		t.Fatal(e)
	}
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{ReuseProvider: true, MultiplierSource: "upstream"})
	if e != nil || q.MultiplierStatus.State != "success" || direct.Load() != 1 || proxied.Load() != 1 {
		t.Fatalf("direct key %+v %v", q, e)
	}
}

// TestAccountIncompletePagination 验证看到一次匹配但列表未读完时拒绝更新，首次失败的 nil 倍率保持未知。
func TestAccountIncompletePagination(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token/":
			if r.URL.Query().Get("p") == "1" {
				fmt.Fprint(w, `{"success":true,"data":{"total":2,"items":[{"key":"wanted-full-secret","name":"unsafe","group":"G"}]}}`)
			} else {
				fmt.Fprint(w, `{"success":true,"data":{"total":2,"items":[]}}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, p, k, _ := accountFixture(t, server.URL)
	// 与主 agent 的新增 Key 集成契约一致：上游来源的新 Key 初始倍率未知。
	if _, e := a.store.db.Exec("UPDATE provider_keys SET document=json_set(document,'$.multiplier',NULL) WHERE id=?", k.ID); e != nil {
		t.Fatal(e)
	}
	q, e := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{Config: QueryConfig{Mode: "newapi", BaseURL: server.URL, UserID: "42", AccessToken: "fixture"}, NameSource: "upstream_key", MultiplierSource: "upstream"})
	if e != nil || q.NameStatus.State != "failed" || q.MultiplierStatus.State != "failed" {
		t.Fatalf("incomplete %+v %v", q, e)
	}
	keys, e := a.store.Keys(ctx, p.ID)
	if e != nil || keys[0].Name != k.Name || keys[0].Multiplier != nil {
		t.Fatalf("first failure changed key %+v %v", keys, e)
	}
	persisted, e := a.ProviderKeyQuery(ctx, k.ID)
	if e != nil || persisted.MultiplierSource != "upstream" {
		t.Fatalf("source lost %+v %v", persisted, e)
	}
}

// TestAccountStaleConnectionRotation 验证查询期间修改认证后，旧余额和旧轮换令牌均不能覆盖新连接。
func TestAccountStaleConnectionRotation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var block atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			if block.Load() {
				started <- struct{}{}
				<-release
			}
			fmt.Fprint(w, `{"data":{"access_token":"old-access","refresh_token":"old-rotated"}}`)
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"balance":99}}`)
		case "/api/v1/subscriptions/summary":
			fmt.Fprint(w, `{"data":{"subscriptions":[]}}`)
		}
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	if _, e := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "initial"}}); e != nil {
		t.Fatal(e)
	}
	block.Store(true)
	done := make(chan error, 1)
	go func() { _, e := a.RefreshProviderAccounts(ctx, p.ID); done <- e }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no auth request")
	}
	// 写入与公开保存相同的加密/世代路径，跳过随后会排队的主动同步以精确观察旧请求。
	a.store.mu.Lock()
	e := a.writeAccount(ctx, "provider_accounts", p.ID, QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "new-user-token"}, ProviderAccount{ProviderID: p.ID}, "changed-generation")
	a.store.mu.Unlock()
	if e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	c, _, _, e := a.accountConfig(ctx, "provider_accounts", p.ID)
	if e != nil || c.RefreshToken != "new-user-token" {
		t.Fatal("stale token overwrote config", e)
	}
	result, e := a.ProviderAccount(ctx, p.ID)
	if e != nil || result.Balance.Amount != nil {
		t.Fatalf("stale balance wrote back %+v %v", result, e)
	}
}

// TestAccountCustomGET 验证 GET 查询参数认证掩码留空不改，GET 永不发送已保存请求体。
func TestAccountCustomGET(t *testing.T) {
	var accepted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Store(r.Method == "GET" && r.URL.Query().Get("token") == "query-secret" && r.ContentLength == 0)
		fmt.Fprint(w, `{"data":{"balance":"0"}}`)
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "custom", Method: "GET", URL: server.URL + "?token=query-secret", BalancePath: "data.balance", Body: `{"ignored":true}`}}
	result, err := a.SaveProviderAccount(context.Background(), p.ID, d)
	if err != nil || !accepted.Load() || strings.Contains(result.Config.URL, "query-secret") || result.Balance.Amount == nil || *result.Balance.Amount != 0 {
		t.Fatalf("GET %+v %v", result, err)
	}
	d.Config = result.Config
	result, err = a.SaveProviderAccount(context.Background(), p.ID, d)
	if err != nil || !accepted.Load() || result.Balance.Status.State != "success" {
		t.Fatalf("masked GET %+v %v", result, err)
	}
}

// TestAccountRefreshSkipsDeletedObjects 在真实在途 HTTP 中删除 Key 或供应商，批次仍继续刷新其他供应商且不恢复已删数据。
func TestAccountRefreshSkipsDeletedObjects(t *testing.T) {
	for _, removeProvider := range []bool{false, true} {
		name := "key"
		if removeProvider {
			name = "provider"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			started := make(chan struct{}, 1)
			release := make(chan struct{}, 1)
			var block atomic.Bool
			var balances atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/sub2api/billing" {
					if block.Load() {
						started <- struct{}{}
						<-release
					}
					fmt.Fprint(w, `{"effective_rate_multiplier":2}`)
					return
				}
				balances.Add(1)
				fmt.Fprint(w, `{"remaining":9}`)
			}))
			defer server.Close()
			defer close(release)
			a, p, k, _ := accountFixture(t, server.URL)
			if _, err := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api", BaseURL: server.URL}, MultiplierSource: "upstream"}); err != nil {
				t.Fatal(err)
			}
			draft := validDraft("survivor")
			draft.URL = server.URL
			survivor, err := a.store.Create(ctx, draft)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.SaveProviderAccount(ctx, survivor.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-survivor"}}); err != nil {
				t.Fatal(err)
			}
			balances.Store(0)
			block.Store(true)
			done := make(chan struct{})
			var result ProviderAccountRefresh
			var refreshErr error
			go func() { defer close(done); result, refreshErr = a.RefreshProviderAccounts(ctx, "") }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("query did not start")
			}
			if removeProvider {
				err = a.store.Delete(ctx, p.ID)
			} else {
				err = a.store.DeleteKey(ctx, k.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			release <- struct{}{}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("refresh did not finish after deletion")
			}
			if refreshErr != nil {
				t.Fatal(refreshErr)
			}
			if balances.Load() != 1 {
				t.Fatal("remaining provider was not refreshed")
			}
			found := false
			for _, account := range result.Accounts {
				if removeProvider && account.ProviderID == p.ID {
					t.Fatal("deleted provider returned")
				}
				if account.ProviderID == survivor.ID {
					found = account.Balance.Amount != nil && *account.Balance.Amount == 9
				}
			}
			if !found {
				t.Fatal("surviving provider result missing")
			}
			for _, query := range result.Keys {
				if query.KeyID == k.ID {
					t.Fatal("deleted key returned")
				}
			}
			var count int
			if err = a.store.db.QueryRow("SELECT COUNT(*) FROM provider_keys WHERE id=?", k.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("deleted key restored: %d %v", count, err)
			}
			if _, err = a.RefreshProviderAccounts(ctx, ""); err != nil {
				t.Fatal("next refresh failed", err)
			}
		})
	}
}
