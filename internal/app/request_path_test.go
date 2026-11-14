package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// TestRequestPathProtectionHistory 经带鉴权的真实网关、加密配置、调度和 SQLite 重读，核对冷却/熔断来源及无重复出站。
func TestRequestPathProtectionHistory(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newExecutionFixture(t)
			history, err := records.New(f.catalog.DB())
			if err != nil {
				t.Fatal(err)
			}
			f.execution.Executor.Records = history
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				w.Header().Set("Content-Type", "application/json")
				if status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "60")
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"test upstream rejected"}}`)
			}
			server := gateway.New(f.catalog, f.execution.Executor)
			server.SetAccessKey("isolated-path-test")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := "http://" + listener.Addr().String()
			port := listener.Addr().(*net.TCPAddr).Port
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			if err = server.Listen(context.Background(), port); err != nil {
				t.Fatal(err)
			}
			defer server.Close(context.Background())
			// 普通 401 每次真实扣分，直到两把 Key 均熔断；429 则首轮即进入一分钟冷却。
			rounds := 1
			if status == http.StatusUnauthorized {
				rounds = 3
			}
			for i := 0; i < rounds; i++ {
				requestPathCall(t, address)
			}
			f.mu.Lock()
			before := len(f.calls)
			f.mu.Unlock()
			requestPathCall(t, address)
			f.mu.Lock()
			after := len(f.calls)
			f.mu.Unlock()
			if before != after {
				t.Fatalf("本地阻断仍产生上游请求 %d -> %d", before, after)
			}
			page, err := history.Query(context.Background(), records.Query{})
			if err != nil {
				t.Fatal(err)
			}
			local, err := history.Get(context.Background(), page.Records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if local.ProviderID != f.provider.ID || local.ProviderName != f.provider.Name || local.KeyID == "" || local.KeyName == "" {
				t.Fatalf("本地阻断遗漏供应商或 Key 摘要: %+v", local.Target)
			}
			if local.State != "local" || len(local.Attempts) != 0 || len(local.Candidates) != 2 || local.Method != "POST" || local.Path != "/v1/chat/completions" {
				t.Fatalf("本地路径事实错误: %+v", local)
			}
			for _, candidate := range local.Candidates {
				if candidate.Reason == "" {
					t.Fatal("遗漏跳过原因")
				}
				if status == http.StatusTooManyRequests {
					if candidate.CooldownUntil <= time.Now().UnixMilli() || candidate.CircuitOpen {
						t.Fatalf("冷却时间错误: %+v", candidate)
					}
				} else {
					if !candidate.CircuitOpen || candidate.NextProbe <= time.Now().UnixMilli() || candidate.CircuitSourceID == "" {
						t.Fatalf("熔断快照错误: %+v", candidate)
					}
					source, err := history.Get(context.Background(), candidate.CircuitSourceID)
					if err != nil || len(source.Attempts) == 0 {
						t.Fatal("来源不可追溯", err)
					}
				}
			}
			// 真实失败尝试同样保留保护结果，重新读取不依赖实时 Key 状态。
			found := false
			for _, summary := range page.Records[1:] {
				record, err := history.Get(context.Background(), summary.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, attempt := range record.Attempts {
					if attempt.CooldownUntil > 0 || attempt.CircuitOpen {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("实际失败尝试遗漏保护结果")
			}
		})
	}
}

// requestPathCall 发起带认证的隔离 HTTP 请求并读完响应，不绕过网关或调度。
func requestPathCall(t *testing.T, endpoint string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/chat/completions", strings.NewReader(`{"model":"logical-model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer isolated-path-test")
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 400 {
		t.Fatalf("预期本地/上游失败，实际 %d", response.StatusCode)
	}
}
