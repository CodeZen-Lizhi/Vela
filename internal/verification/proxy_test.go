package verification

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestKeyProxyIndependent 验证模型检测的真实 HTTP 出口不继承供应商直连，旧模式兼容全局。
func TestKeyProxyIndependent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "global") }))
	defer proxy.Close()
	m, _ := fixture(t, upstream.URL)
	ctx := context.Background()
	if err := m.catalog.SetProxy(ctx, models.Proxy{Enabled: true, URL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []providers.ProxyMode{providers.ProxyGlobal, providers.ProxyProvider, "", providers.ProxyDirect} {
		c := providers.ExecutionCandidate{Provider: providers.Provider{Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}}, Key: providers.ProviderKey{Proxy: providers.ProxyChoice{Mode: mode}}}
		client, err := m.client(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(upstream.URL)
		if err != nil {
			client.CloseIdleConnections()
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		client.CloseIdleConnections()
		want := "global"
		if mode == providers.ProxyDirect {
			want = "direct"
		}
		if err != nil || string(body) != want {
			t.Fatalf("模式 %q 出口 %q，期望 %q: %v", mode, body, want, err)
		}
	}
}
