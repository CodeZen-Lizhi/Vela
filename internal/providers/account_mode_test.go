package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAccountModeCurrency 验证从自定义原币种切换到内置方式后使用适配器币种，不残留其他方式的金额细项。
func TestAccountModeCurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			fmt.Fprint(w, `{"data":{"access_token":"fixture"}}`)
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"balance":12,"points":3}}`)
		case "/api/v1/subscriptions/summary":
			fmt.Fprint(w, `{"data":{"subscriptions":[]}}`)
		default:
			fmt.Fprint(w, `{"remaining":7,"data":{"balance":42}}`)
		}
	}))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx := context.Background()
	_, err := saveAndRefreshProviderAccount(a, ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "custom", URL: server.URL, Method: "GET", BalancePath: "data.balance", Currency: "CNY"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := saveAndRefreshProviderAccount(a, ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "sub2api", RefreshToken: "fixture", Currency: "CNY"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Balance.Currency != "USD" || result.Balance.Cash == nil || *result.Balance.Amount != 15 {
		t.Fatalf("内置方式错误继承旧币种：%+v", result.Balance)
	}
	result, err = saveAndRefreshProviderAccount(a, ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture", Currency: "CNY"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Balance.Currency != "USD" || result.Balance.Cash != nil || result.Balance.Points != nil {
		t.Fatalf("切换后保留旧细项：%+v", result.Balance)
	}
}
