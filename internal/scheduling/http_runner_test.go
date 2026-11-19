package scheduling

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestUpstreamConnectionError 验证网络细节可诊断，同时不把请求 URL、查询凭据或密钥带入错误。
func TestUpstreamConnectionError(t *testing.T) {
	secret := "test-secret+/"
	cause := &url.Error{Op: "Post", URL: "https://user:password@example.invalid/v1/responses?key=query-secret", Err: errors.New("TLS handshake failed: " + secret + " " + url.QueryEscape(secret))}
	message := upstreamConnectionError(cause, secret).Error()
	if !strings.Contains(message, "TLS handshake failed") {
		t.Fatalf("网络错误细节丢失: %q", message)
	}
	for _, forbidden := range []string{"password", "query-secret", "example.invalid", secret, url.QueryEscape(secret)} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("连接错误包含敏感数据: %q", message)
		}
	}
}

// TestResponsesLargePrelude 验证携带指令和工具定义的双事件前奏可完整转发，错误与缺失终态仍不会提交。
func TestResponsesLargePrelude(t *testing.T) {
	var prelude strings.Builder
	for _, kind := range []string{"response.created", "response.in_progress"} {
		prelude.WriteString("event: " + kind + "\ndata: {\"type\":\"" + kind + "\",\"response\":{\"status\":\"in_progress\",\"instructions\":\"" + strings.Repeat("i", 22000) + "\",\"tools\":[{\"description\":\"" + strings.Repeat("t", 27000) + "\"}]}}\n\n")
	}
	for _, tc := range []struct {
		// name 标识终态场景；ending 是原始事件；success 是预期成功与提交状态。
		name, ending string
		success      bool
	}{
		{"completed", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n", true},
		{"failed", "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n", false},
		{"missing-terminal", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := prelude.String() + tc.ending
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, body)
			}))
			defer upstream.Close()
			runner := NewHTTPRunner()
			defer runner.Close()
			var out bytes.Buffer
			result, err := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "m"}, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{}`), Stream: true}, "key", &out)
			if (err == nil) != tc.success || result.Outcome.Success != tc.success || result.Committed != tc.success {
				t.Fatalf("前奏判定: result=%+v error=%v", result, err)
			}
			if tc.success && out.String() != body || !tc.success && (out.Len() != 0 || !result.Retryable) {
				t.Fatalf("前奏转发或重试状态错误: bytes=%d retryable=%v", out.Len(), result.Retryable)
			}
		})
	}
}

// TestResponsesPrefetchBoundary 验证前奏超过字节或事件预读边界后继续完整转发，之后失败不允许重试。
func TestResponsesPrefetchBoundary(t *testing.T) {
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	failed := "data: {\"type\":\"response.failed\"}\n\n"
	for _, prelude := range []string{
		strings.Repeat("data: {\"type\":\"response.created\"}\n\n", 5),
		"data: {\"type\":\"response.created\",\"response\":{\"instructions\":\"" + strings.Repeat("i", 300*1024) + "\"}}\n\n",
		strings.Repeat("data: {\"type\":\"response.created\",\"response\":{\"instructions\":\""+strings.Repeat("i", 1024*1024)+"\"}}\n\n", 5),
	} {
		for _, ending := range []string{completed, failed, ""} {
			var out bytes.Buffer
			body := prelude + ending
			result, err := forwardSSE(context.Background(), strings.NewReader(body), &out, "openai:responses", AttemptResult{})
			success := ending == completed
			if (err == nil) != success || result.Outcome.Success != success || !result.Committed || result.Retryable || out.String() != body {
				t.Fatalf("预读边界处理错误: input=%d output=%d result=%+v err=%v", len(body), out.Len(), result, err)
			}
		}
	}
}

// TestResponsesOversizedEvent 验证提交后的单事件仍有 4 MiB 上限，过长事件不能无限累计。
func TestResponsesOversizedEvent(t *testing.T) {
	var out bytes.Buffer
	body := strings.Repeat("data: {\"type\":\"response.created\"}\n\n", 5) + strings.Repeat(": keepalive\n", 400000)
	result, err := forwardSSE(context.Background(), strings.NewReader(body), &out, "openai:responses", AttemptResult{})
	if err == nil || !result.Committed || result.Retryable || result.Outcome.Penalty != Heavy {
		t.Fatalf("单事件上限失效: %+v %v", result, err)
	}
}

// TestResponsesPrefetchStreamsBeforeEventEnd 验证大事件未结束时达到预读预算就向客户端转发，不等待后续终态。
func TestResponsesPrefetchStreamsBeforeEventEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	prefix := "data: {\"type\":\"response.created\",\"response\":{\"instructions\":\"" + strings.Repeat("i", 300*1024) + "\"}}\n"
	suffix := "\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, prefix)
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, suffix)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	runner := NewHTTPRunner()
	defer runner.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	finished := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "m"}, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{}`), Stream: true}, "key", writer)
		_ = writer.CloseWithError(err)
		finished <- err
	}()
	var first [1]byte
	_, firstErr := io.ReadFull(reader, first[:])
	close(release)
	rest, readErr := io.ReadAll(reader)
	runErr := <-finished
	if firstErr != nil || readErr != nil || runErr != nil || string(first[:])+string(rest) != prefix+suffix {
		t.Fatalf("预读未及时转发或丢失内容: first=%v read=%v run=%v", firstErr, readErr, runErr)
	}
}

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
		{"responses-json", "openai:responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, false, true, true},
		{"responses-failed", "openai:responses", `{"status":"failed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, false, false, false},
		{"anthropic-json", "claude:messages", `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`, false, true, true},
		{"gemini-json", "gemini:generate_content", `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`, false, true, true},
		{"chat-done", "openai:chat", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", true, true, true},
		{"chat-partial", "openai:chat", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", true, false, true},
		{"responses-stream", "openai:responses", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n", true, true, true},
		{"responses-item-is-not-terminal", "openai:responses", "data: {\"type\":\"response.output_item.done\",\"item\":{}}\n\n", true, false, true},
		{"anthropic-stop", "claude:messages", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true, true, true},
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
