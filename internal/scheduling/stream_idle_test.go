package scheduling

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestStreamIdleBeforeOutput 验证成功响应头之后始终无正文也会超时，未提交时可重试，调用方取消保持中性。
func TestStreamIdleBeforeOutput(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "cancel"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				if canceled {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			runner := NewHTTPRunner()
			defer runner.Close()
			var output bytes.Buffer
			start := time.Now()
			result, err := runner.Run(ctx, Candidate{Provider: providers.Provider{URL: upstream.URL, Request: providers.RequestPolicy{StreamTotalMillis: 100}}, Model: "test"}, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{}`), Stream: true}, "test-key", &output)
			if canceled {
				if !errors.Is(err, context.Canceled) || result.Outcome.Penalty != Neutral {
					t.Fatalf("取消不应误算上游故障: result=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || result.Status != 504 || result.Outcome.Penalty != Light || !result.Retryable || result.Committed || output.Len() != 0 || time.Since(start) > time.Second {
				t.Fatalf("无正文空闲处理错误: result=%+v err=%v elapsed=%s", result, err, time.Since(start))
			}
		})
	}
}

// TestInteractionsStreamIdle 验证原生 Interactions 的持续数据不受非流式总期限影响，空闲计时与聊天流一致。
func TestInteractionsStreamIdle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event_type\":\"interaction.start\",\"interaction\":{\"id\":\"interaction-test\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		for range 6 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, "data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"interaction-test\",\"status\":\"completed\"}}\n\n")
	}))
	defer upstream.Close()
	runner := NewHTTPRunner()
	defer runner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	result, err := runner.Run(ctx, Candidate{Provider: providers.Provider{URL: upstream.URL, Request: providers.RequestPolicy{StreamTotalMillis: 500, RequestSeconds: 1}}, Model: "test"}, ExecutionRequest{Request: Request{Protocol: "gemini:interactions"}, Type: "interactions", Path: "/v1beta/interactions", Body: []byte(`{"input":"test"}`), Stream: true}, "test-key", &output)
	if err != nil || !result.Outcome.Success || result.ResponseID != "interaction-test" {
		t.Fatalf("持续 Interactions 数据被中断: result=%+v err=%v", result, err)
	}
}
