package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// seedRotationConfig 经真实加密保存路径准备配置，跳过保存入口附带的同步以控制在途时序。
func seedRotationConfig(t *testing.T, a *Accounts, table, id string, c QueryConfig, doc any, rev string) {
	t.Helper()
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if err := a.writeAccount(context.Background(), table, id, c, doc, rev); err != nil {
		t.Fatal(err)
	}
}

// requireRotationToken 重读并解密真实数据库，验证轮换结果且不向失败日志输出凭据。
func requireRotationToken(t *testing.T, a *Accounts, table, id, expected string) {
	t.Helper()
	c, _, _, err := a.accountConfig(context.Background(), table, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken != expected {
		t.Error("持久化的 Refresh Token 不符合预期")
	}
}

// awaitRotationRequest 等待真实 HTTP 到达受控阶段，超时直接暴露未经过预期调用链。
func awaitRotationRequest(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到预期 HTTP 请求")
	}
}

// TestAccountRotationBeforeFollowup 验证认证后立即落库，余额或 Key 后续失败、取消均不丢轮换。
func TestAccountRotationBeforeFollowup(t *testing.T) {
	for _, target := range []string{"balance", "independent-key", "reused-key"} {
		for _, cancelQuery := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", target, cancelQuery), func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/auth/refresh" {
						fmt.Fprint(w, `{"data":{"access_token":"query-access","refresh_token":"next-refresh"}}`)
						return
					}
					if r.Header.Get("Authorization") != "Bearer query-access" {
						t.Error("后续查询未使用账户认证")
					}
					close(started)
					<-release
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer server.Close()
				defer close(release)
				a, p, k, _ := accountFixture(t, server.URL)
				c := QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "initial-refresh"}
				table, id := "provider_accounts", p.ID
				oldBalance := 7.0
				if target != "independent-key" {
					seedRotationConfig(t, a, table, id, c, ProviderAccount{ProviderID: p.ID, Balance: AccountBalance{Amount: &oldBalance}}, "account-generation")
				}
				if target != "balance" {
					q := ProviderKeyQuery{KeyID: k.ID, ReuseProvider: target == "reused-key", NameSource: "upstream_key", MultiplierSource: "upstream"}
					qc := QueryConfig{}
					if !q.ReuseProvider {
						qc = c
						table, id = "provider_key_queries", k.ID
					}
					seedRotationConfig(t, a, "provider_key_queries", k.ID, qc, q, "key-generation")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if target == "balance" {
						_, err := a.RefreshProviderAccounts(ctx, p.ID)
						done <- err
					} else {
						_, err := a.RefreshProviderKey(ctx, k.ID)
						done <- err
					}
				}()
				awaitRotationRequest(t, started)
				requireRotationToken(t, a, table, id, "next-refresh")
				if cancelQuery {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("取消未反馈: %v", err)
					}
				} else {
					release <- struct{}{}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if target == "balance" {
						account, err := a.ProviderAccount(context.Background(), p.ID)
						if err != nil || account.Balance.Status.State != "failed" || account.Balance.Amount == nil || *account.Balance.Amount != oldBalance {
							t.Fatal("后续失败未保留原余额及失败状态", err)
						}
					} else {
						query, err := a.ProviderKeyQuery(context.Background(), k.ID)
						if err != nil || query.NameStatus.State != "failed" || query.MultiplierStatus.State != "failed" {
							t.Fatal("后续失败未记录字段查询状态", err)
						}
					}
				}
				requireRotationToken(t, a, table, id, "next-refresh")
			})
		}
	}
}

// TestAccountRotationSharedCache 验证同批多个独立配置共享认证时均传播轮换，下一批使用新令牌。
func TestAccountRotationSharedCache(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			n := refreshes.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != fmt.Sprintf("refresh-%d", n-1) {
				t.Error("认证重复使用已失效的 Refresh Token")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"data":{"access_token":"access-%d","refresh_token":"refresh-%d"}}`, n, n)
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer access-%d", refreshes.Load()) {
			t.Error("查询认证被聊天 API Key 覆盖")
		}
		switch r.URL.Path {
		case "/api/v1/auth/me":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/api/v1/keys":
			fmt.Fprint(w, `{"data":{"total":2,"items":[{"key":"sk-wanted-full-secret","name":"remote-one","group_id":1},{"key":"sk-other-full-secret","name":"remote-two","group_id":1}]}}`)
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"data":[{"id":1,"name":"remote-group","rate_multiplier":2}]}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{"1":3}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a, p, k, other := accountFixture(t, server.URL)
	c := QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "refresh-0"}
	seedRotationConfig(t, a, "provider_accounts", p.ID, c, ProviderAccount{ProviderID: p.ID}, "account-generation")
	for _, id := range []string{k.ID, other.ID} {
		q := ProviderKeyQuery{KeyID: id, NameSource: "upstream_key", MultiplierSource: "upstream"}
		seedRotationConfig(t, a, "provider_key_queries", id, c, q, "key-generation")
	}
	for batch := int32(1); batch <= 2; batch++ {
		result, err := a.RefreshProviderAccounts(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if refreshes.Load() != batch || len(result.Keys) != 2 {
			t.Fatal("同批未复用认证或未处理全部 Key")
		}
		requireRotationToken(t, a, "provider_accounts", p.ID, fmt.Sprintf("refresh-%d", batch))
		for _, id := range []string{k.ID, other.ID} {
			requireRotationToken(t, a, "provider_key_queries", id, fmt.Sprintf("refresh-%d", batch))
			q, err := a.ProviderKeyQuery(context.Background(), id)
			if err != nil || q.NameStatus.State != "success" || q.MultiplierStatus.State != "success" {
				t.Fatalf("缓存命中后 Key 未同步: %+v %v", q, err)
			}
		}
	}
}

// TestAccountRotationCanceledSave 验证已收到真实 HTTP 认证结果后，调用方取消也不撤销必要的本地轮换保存。
func TestAccountRotationCanceledSave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"access_token":"query-access","refresh_token":"next-refresh"}}`)
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	c := QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "initial-refresh"}
	seedRotationConfig(t, a, "provider_accounts", p.ID, c, ProviderAccount{ProviderID: p.ID}, "auth-generation")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.beginQuery(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.endQuery()
	h, err := a.connection(ctx, c, ProxyChoice{Mode: ProxyDirect}, ProxyChoice{Mode: ProxyDirect})
	if err != nil {
		t.Fatal(err)
	}
	defer h.transport.CloseIdleConnections()
	if err = a.authenticate(ctx, h); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = a.saveRotation(ctx, h, "provider_accounts", p.ID, "auth-generation"); err != nil {
		t.Fatal(err)
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "next-refresh")
}

// TestAccountRotationSaveFailure 验证真实 SQLite 写入失败向调用方返回错误，不继续查询或假装轮换已保存。
func TestAccountRotationSaveFailure(t *testing.T) {
	var followups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			fmt.Fprint(w, `{"data":{"access_token":"query-access","refresh_token":"next-refresh"}}`)
			return
		}
		followups.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	c := QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "initial-refresh"}
	seedRotationConfig(t, a, "provider_accounts", p.ID, c, ProviderAccount{ProviderID: p.ID}, "auth-generation")
	// 只阻断本测试数据库的认证更新，保留读取以观察失败后已有数据。
	if _, err := a.store.db.Exec(`CREATE TRIGGER reject_rotation BEFORE UPDATE OF secret ON provider_accounts
		BEGIN SELECT RAISE(ABORT, 'fixture-write-denied'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RefreshProviderAccounts(context.Background(), p.ID); err == nil {
		t.Fatal("轮换保存失败未返回错误")
	}
	if followups.Load() != 0 {
		t.Fatal("轮换保存失败后仍继续业务查询")
	}
	requireRotationToken(t, a, "provider_accounts", p.ID, "initial-refresh")
}

// TestAccountRotationConcurrentKeyChange 验证认证在途时编辑/删除聊天 Key 不阻止认证所属配置保存，修改认证则拒绝旧轮换。
func TestAccountRotationConcurrentKeyChange(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		for _, change := range []string{"edit-key", "delete-key", "replace-auth", "delete-provider"} {
			t.Run(fmt.Sprintf("reuse=%t/%s", reuse, change), func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/auth/refresh" {
						close(started)
						<-release
						fmt.Fprint(w, `{"data":{"access_token":"query-access","refresh_token":"next-refresh"}}`)
						return
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer server.Close()
				defer close(release)
				a, p, k, _ := accountFixture(t, server.URL)
				c := QueryConfig{Mode: "sub2api", BaseURL: server.URL, RefreshToken: "initial-refresh"}
				q := ProviderKeyQuery{KeyID: k.ID, ReuseProvider: reuse, NameSource: "upstream_key", MultiplierSource: "upstream"}
				table, id := "provider_key_queries", k.ID
				qc := c
				if reuse {
					table, id = "provider_accounts", p.ID
					seedRotationConfig(t, a, table, id, c, ProviderAccount{ProviderID: p.ID}, "auth-generation")
					qc = QueryConfig{}
				}
				seedRotationConfig(t, a, "provider_key_queries", k.ID, qc, q, "auth-generation")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := a.RefreshProviderKey(ctx, k.ID); done <- err }()
				awaitRotationRequest(t, started)
				var err error
				switch change {
				case "edit-key":
					_, err = a.store.UpdateKey(context.Background(), k.ID, keyDraft("edited", "sk-replacement-chat-key", 1))
				case "delete-key":
					err = a.store.DeleteKey(context.Background(), k.ID)
				case "replace-auth":
					c.RefreshToken = "user-replacement"
					var doc any = q
					if reuse {
						doc = ProviderAccount{ProviderID: p.ID}
					}
					seedRotationConfig(t, a, table, id, c, doc, "new-auth-generation")
				case "delete-provider":
					err = a.store.Delete(context.Background(), p.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				release <- struct{}{}
				if err = <-done; err != nil && !accountObjectMissing(err) {
					t.Fatal(err)
				}
				if change == "delete-provider" || (change == "delete-key" && !reuse) {
					if _, _, _, err = a.accountConfig(context.Background(), table, id); !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("已删认证被恢复: %v", err)
					}
					return
				}
				expected := "next-refresh"
				if change == "replace-auth" {
					expected = "user-replacement"
				}
				requireRotationToken(t, a, table, id, expected)
				if change == "edit-key" {
					keys, err := a.store.Keys(context.Background(), p.ID)
					if err != nil || keys[0].Name != "edited" {
						t.Fatal("旧查询覆盖了用户编辑的 Key", err)
					}
					secret, err := a.store.Secret(context.Background(), k.ID)
					if err != nil || secret != "sk-replacement-chat-key" {
						t.Fatal("认证轮换误改聊天 API Key", err)
					}
				}
			})
		}
	}
}
