package scheduling

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestHTTPProtocolTerminals 通过真实HTTP验证各协议成功终态、200错误体和SSE中断，不复制被测解析逻辑。
func TestHTTPProtocolTerminals(t *testing.T) {
	cases := []struct {
		// name、protocol、body 是原始上游协议输入；stream 控制SSE路径。
		name, protocol, body       string
		stream, success, committed bool
	}{
		{"responses-output-object", "openai:responses", `{"status":"completed","output":{}}`, false, false, false},
		{"gemini-empty-stream", "gemini:generate_content", "data: {\"candidates\":[{\"content\":{}}]}\n\n", true, false, false},
		{"chat-json", "openai:chat", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, false, true, true},
		{"200-error", "openai:chat", `{"error":{"message":"failed"}}`, false, false, false},
		{"chat-missing-terminal", "openai:chat", `{"choices":[{"message":{"content":"partial"}}]}`, false, false, false},
		{"responses-json", "openai:responses", `{"status":"completed","output":[]}`, false, true, true},
		{"responses-failed", "openai:responses", `{"status":"failed","output":[]}`, false, false, false},
		{"anthropic-json", "claude:messages", `{"type":"message","stop_reason":"end_turn","content":[]}`, false, true, true},
		{"gemini-json", "gemini:generate_content", `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`, false, true, true},
		{"chat-done", "openai:chat", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", true, true, true},
		{"chat-partial", "openai:chat", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", true, false, true},
		{"responses-stream", "openai:responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", true, true, true},
		{"responses-item-is-not-terminal", "openai:responses", "data: {\"type\":\"response.output_item.done\",\"item\":{}}\n\n", true, false, true},
		{"anthropic-stop", "claude:messages", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true, true, true},
		{"anthropic-done-is-not-terminal", "claude:messages", "data: [DONE]\n\n", true, false, false},
		{"gemini-stream", "gemini:generate_content", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n\n", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			runner := NewHTTPRunner()
			defer runner.Close()
			var out bytes.Buffer
			result, err := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "model"}, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: "/v1/chat/completions", Body: []byte(`{"model":"model"}`), Stream: tc.stream}, "test-key", &out)
			if result.Outcome.Success != tc.success || result.Committed != tc.committed || (err == nil) != tc.success {
				t.Fatalf("原始协议判定: result=%+v error=%v output=%s", result, err, out.String())
			}
		})
	}
}

// TestHTTPRedirectNeverForwardsSecret 验证携带上游凭据的请求不会跟随跨地址重定向。
func TestHTTPRedirectNeverForwardsSecret(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer upstream.Close()
	runner := NewHTTPRunner()
	defer runner.Close()
	var out bytes.Buffer
	result, err := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "model"}, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`)}, "private", &out)
	if err == nil || result.Status != 307 || called {
		t.Fatalf("不应跟随重定向: %+v %v called=%v", result, err, called)
	}
}

// TestRetryAfterAndErrorScope 验证有效503等待、模糊鉴权不归零，以及生成正文不作为错误信封。
func TestRetryAfterAndErrorScope(t *testing.T) {
	for _, status := range []int{429, 503, 401, 403, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "1200")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"code":"invalid_api_key"}}`)
			}))
			defer upstream.Close()
			runner := NewHTTPRunner()
			defer runner.Close()
			var out bytes.Buffer
			result, _ := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "m"}, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`)}, "private", &out)
			if result.Outcome.Penalty == Unavailable || result.Outcome.RetryAfter.Seconds() != 600 {
				t.Fatalf("模糊鉴权/冷却上限: %+v", result)
			}
		})
	}
	if outcome := classifyHTTP(200, []byte(`{"choices":[{"message":{"content":"invalid_api_key rate_limit_error"}}]}`)); outcome.Penalty != Neutral || outcome.RateLimited {
		t.Fatal("生成文字不能成为错误分类依据")
	}
	if delay := retryAfter("1garbage", time.Now()); delay != 0 {
		t.Fatal(delay)
	}
}

// TestSSEEventIsBounded 验证没有空行的短keepalive不能绕过单行限制并无界累积。
func TestSSEEventIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 10000; i++ {
			if _, err := io.WriteString(w, ": keepalive\n"); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	runner := NewHTTPRunner()
	defer runner.Close()
	var output bytes.Buffer
	result, err := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: server.URL}, Model: "m"}, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`), Stream: true}, "key", &output)
	if err == nil || result.Outcome.Penalty != Heavy || result.Committed || output.Len() != 0 {
		t.Fatalf("无界前奏未被拒绝: %+v %v", result, err)
	}
}
