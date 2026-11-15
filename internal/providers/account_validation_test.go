package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestAccountValidationQueueTimeout 验证后台批次占用查询席位时，前台验证仍在总时限内返回，不无限锁住表单。
func TestAccountValidationQueueTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &Accounts{queryMu: make(chan struct{}, 1)}
		if err := a.beginQuery(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer func() {
			cancel()
			synctest.Wait()
			a.endQuery()
		}()
		done := make(chan error, 1)
		go func() {
			_, err := a.ValidateProviderAccount(ctx, "queued-provider", ProviderAccountDraft{})
			done <- err
		}()
		synctest.Wait()
		time.Sleep(61 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("验证应明确返回超时，实际为 %v", err)
			}
		default:
			t.Fatal("排队超过验证总时限仍未返回，前台会一直显示正在验证")
		}
	})
}

// TestAccountValidationLifecycle 通过真实 HTTP 和 SQLite 核对保存零请求、临时验证、取消、草稿失效与一次性应用。
func TestAccountValidationLifecycle(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"remaining":0,"unit":"USD"}`)
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx := context.Background()
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-original"}}
	saved, err := a.SaveProviderAccount(ctx, p.ID, d)
	if err != nil || calls.Load() != 0 || saved.Balance.Amount != nil {
		t.Fatalf("直接保存不应联网或产生余额：calls=%d err=%v", calls.Load(), err)
	}
	d.Config.APIKey = "fixture-draft"
	v, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || v.ID == "" || v.Balance.Status.State != "success" || v.Balance.Amount == nil || *v.Balance.Amount != 0 || calls.Load() != 1 {
		t.Fatalf("验证结果错误：%+v %v", v, err)
	}
	stored, err := a.ProviderAccount(ctx, p.ID)
	if err != nil || stored.Balance.Amount != nil {
		t.Fatal("验证写入了正式余额", err)
	}
	c, _, _, err := a.accountConfig(ctx, "provider_accounts", p.ID)
	if err != nil || c.APIKey != "fixture-original" {
		t.Fatal("验证保存了草稿配置", err)
	}
	a.DiscardProviderAccountValidation(p.ID, v.ID)
	d.ValidationID = v.ID
	if _, err = a.SaveProviderAccount(ctx, p.ID, d); err == nil {
		t.Fatal("已取消验证仍可应用")
	}
	d.ValidationID = ""
	v, err = a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	d.ValidationID = v.ID
	d.Config.APIKey = "fixture-changed"
	if _, err = a.SaveProviderAccount(ctx, p.ID, d); err == nil {
		t.Fatal("修改后的草稿仍可应用旧余额")
	}
	d.Config.APIKey = "fixture-draft"
	before := calls.Load()
	saved, err = a.SaveProviderAccount(ctx, p.ID, d)
	if err != nil || calls.Load() != before || saved.Balance.Amount == nil || *saved.Balance.Amount != 0 || saved.Balance.Status.LastSuccess != v.Balance.Status.LastSuccess {
		t.Fatalf("保存没有采用已有零余额或发生重复查询：%+v %v", saved.Balance, err)
	}
	if _, err = a.SaveProviderAccount(ctx, p.ID, d); err == nil {
		t.Fatal("已消费的验证仍可再次应用")
	}
}

// TestAccountValidationKeySeparation 验证余额成功不更新 Key，后续独立同步只处理复用连接的上游字段。
func TestAccountValidationKeySeparation(t *testing.T) {
	var calls, independent atomic.Int32
	var generation atomic.Int32
	generation.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/independent" {
			independent.Add(1)
		} else {
			calls.Add(1)
		}
		fmt.Fprintf(w, `{"balance":12,"key":"sk-wanted-full-secret","name":"remote-%d","rate":0.5}`, generation.Load())
	}))
	defer server.Close()
	a, p, k, other := accountFixture(t, server.URL)
	ctx := context.Background()
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "custom", URL: server.URL, Method: "GET", BalancePath: "balance", Currency: "USD"}}
	if _, err := a.SaveProviderAccount(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	q := ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "upstream_key", MultiplierSource: "upstream", KeyPath: "key", NamePath: "name", MultiplierPath: "rate"}
	if _, err := a.SaveProviderKeyQuery(ctx, k.ID, q); err != nil {
		t.Fatal(err)
	}
	q.ReuseProvider = false
	q.Config = QueryConfig{Mode: "custom", URL: server.URL + "/independent", Method: "GET"}
	if _, err := a.SaveProviderKeyQuery(ctx, other.ID, q); err != nil {
		t.Fatal(err)
	}
	generation.Store(2)
	calls.Store(0)
	independent.Store(0)
	v, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || v.Balance.Status.State != "success" {
		t.Fatal("验证失败", err)
	}
	keys, err := a.store.Keys(ctx, p.ID)
	if err != nil || keys[0].Name != "remote-1" || calls.Load() != 1 || independent.Load() != 0 {
		t.Fatal("验证意外同步了 Key", err)
	}
	d.ValidationID = v.ID
	if _, err = a.SaveProviderAccount(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || independent.Load() != 0 {
		t.Fatal("保存进行了上游查询")
	}
	if err = a.RefreshReusedProviderKeys(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	keys, err = a.store.Keys(ctx, p.ID)
	if err != nil || keys[0].Name != "remote-2" || keys[1].Name != "remote-1" || calls.Load() != 2 || independent.Load() != 0 {
		t.Fatalf("后台同步范围错误：%+v %v", keys, err)
	}
}

// TestAccountValidationFailureAndRotation 验证轮换凭据跨取消、成功保存及余额验证失败后保存均保持有效，不查询订阅。
func TestAccountValidationFailureAndRotation(t *testing.T) {
	var refreshes, extras atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			fmt.Fprintf(w, `{"data":{"access_token":"fixture-access","refresh_token":"rotated-%d"}}`, refreshes.Add(1))
		case "/api/v1/auth/me":
			if fail.Load() {
				http.Error(w, "fixture-unavailable", 503)
			} else {
				fmt.Fprint(w, `{"data":{"balance":7}}`)
			}
		default:
			extras.Add(1)
			http.Error(w, "unexpected query", 404)
		}
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx := context.Background()
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "fixture-original"}}
	if _, err := a.SaveProviderAccount(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	d.Config.RefreshToken = ""
	v, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || v.Balance.Status.State != "success" {
		t.Fatal("验证失败", err)
	}
	a.DiscardProviderAccountValidation(p.ID, v.ID)
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-1")
	account, err := a.ProviderAccount(ctx, p.ID)
	if err != nil || account.Balance.Amount != nil {
		t.Fatal("取消验证应用了余额", err)
	}
	v, err = a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	d.ValidationID = v.ID
	account, err = a.SaveProviderAccount(ctx, p.ID, d)
	if err != nil || account.Balance.Amount == nil || *account.Balance.Amount != 7 {
		t.Fatal("保存验证余额失败", err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-2")
	fail.Store(true)
	d.ValidationID = ""
	d.Config.RefreshToken = "fixture-new-input"
	v, err = a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || v.Balance.Status.State != "failed" {
		t.Fatal("失败验证没有返回结果", err)
	}
	d.ValidationID = v.ID
	account, err = a.SaveProviderAccount(ctx, p.ID, d)
	if err != nil || account.Balance.Amount == nil || *account.Balance.Amount != 7 || account.Balance.Status.State != "success" {
		t.Fatal("失败验证阻止保存或覆盖了旧余额", err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-3")
	if refreshes.Load() != 3 || extras.Load() != 0 {
		t.Fatalf("验证/保存发起了额外请求：refresh=%d extra=%d", refreshes.Load(), extras.Load())
	}
}

// TestAccountValidationSingleUseRotation 旧 Token 立即失效时，重复验证及结果失效后的保存仍使用最新轮换凭据。
func TestAccountValidationSingleUseRotation(t *testing.T) {
	var expected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			want := "fixture-new-input"
			if n := expected.Load(); n > 0 {
				want = fmt.Sprintf("rotated-%d", n)
			}
			if body["refresh_token"] != want {
				http.Error(w, "refresh token already consumed", 401)
				return
			}
			fmt.Fprintf(w, `{"data":{"access_token":"fixture-access","refresh_token":"rotated-%d"}}`, expected.Add(1))
			return
		}
		fmt.Fprint(w, `{"data":{"balance":9}}`)
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx := context.Background()
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "fixture-new-input"}}
	first, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || first.Balance.Status.State != "success" {
		t.Fatal("首次验证失败", err)
	}
	a.DiscardProviderAccountValidation(p.ID, first.ID)
	second, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || second.Balance.Status.State != "success" {
		t.Fatalf("重复验证使用已失效凭据：%+v %v", second, err)
	}
	a.DiscardProviderAccountValidation(p.ID, second.ID)
	// 修改非认证字段后不再应用余额，但新认证不能丢弃或重发旧 Token。
	d.Config.Currency = "CNY"
	saved, err := a.SaveProviderAccount(ctx, p.ID, d)
	if err != nil || saved.Balance.Amount != nil {
		t.Fatal("直接保存应用了失效余额", err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-2")
	if expected.Load() != 2 {
		t.Fatal("直接保存意外访问上游")
	}
}

// TestAccountValidationRotationFollowsRefresh 验证失效后后台继续轮换时，草稿保存不能用旧缓存回退正式凭据。
func TestAccountValidationRotationFollowsRefresh(t *testing.T) {
	var expected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			want := "fixture-original"
			if n := expected.Load(); n > 0 {
				want = fmt.Sprintf("rotated-%d", n)
			}
			if body["refresh_token"] != want {
				http.Error(w, "refresh token already consumed", 401)
				return
			}
			fmt.Fprintf(w, `{"data":{"access_token":"fixture-access","refresh_token":"rotated-%d"}}`, expected.Add(1))
		case "/api/v1/subscriptions/summary":
			fmt.Fprint(w, `{"data":{"subscriptions":[]}}`)
		default:
			fmt.Fprint(w, `{"data":{"balance":9}}`)
		}
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx := context.Background()
	d := ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "fixture-original"}}
	if _, err := a.SaveProviderAccount(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	v, err := a.ValidateProviderAccount(ctx, p.ID, d)
	if err != nil || v.Balance.Status.State != "success" {
		t.Fatal("验证失败", err)
	}
	a.DiscardProviderAccountValidation(p.ID, v.ID)
	if _, err = a.RefreshProviderAccounts(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-2")
	if _, err = a.SaveProviderAccount(ctx, p.ID, d); err != nil {
		t.Fatal(err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "rotated-2")
	if expected.Load() != 2 {
		t.Fatal("直接保存发生网络查询")
	}
}
