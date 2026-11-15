package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/coder/websocket"
)

// TestGatewayStreamIdleTimeout 验证真实 HTTP 入口持续收到工具增量或心跳时不中断，停流后超时且下一请求仍可进入。
func TestGatewayStreamIdleTimeout(t *testing.T) {
	for _, mode := range []string{"delta", "heartbeat", "fragment"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecutionFixture(t)
			policy := f.provider.Request
			policy.StreamTotalMillis = 1000
			if _, err := f.vendors.Update(context.Background(), f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: policy}); err != nil {
				t.Fatal(err)
			}
			f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"custom_tool_call\",\"name\":\"exec\",\"call_id\":\"call-test\"}}\n\n")
				w.(http.Flusher).Flush()
				if body["input"] != "short" {
					if mode == "fragment" {
						_, _ = io.WriteString(w, `data: {"type":"response.custom_tool_call_input.delta","delta":"`)
					}
					for range 6 {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(250 * time.Millisecond):
						}
						chunk := "data: {\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\"x\"}\n\n"
						if mode == "heartbeat" {
							chunk = ": heartbeat\n\n"
						} else if mode == "fragment" {
							chunk = "x"
						}
						if _, err := io.WriteString(w, chunk); err != nil {
							return
						}
						w.(http.Flusher).Flush()
					}
				}
				if mode == "fragment" && body["input"] != "short" {
					_, _ = io.WriteString(w, "\"}\n\n")
					w.(http.Flusher).Flush()
				}
				if body["input"] == "stall" {
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.custom_tool_call_input.done\",\"input\":\"x\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}
			base := startCompatibilityGateway(t, f)
			for _, input := range []string{"long", "stall", "short"} {
				start := time.Now()
				status, body := compatibilityRequest(t, base, "/v1/responses", fmt.Sprintf(`{"model":"logical-model","stream":true,"input":%q}`, input), nil)
				completed := strings.Contains(string(body), `"type":"response.completed"`)
				if status != 200 || completed != (input != "stall") {
					t.Fatalf("input=%s status=%d completed=%v elapsed=%s", input, status, completed, time.Since(start))
				}
				if input == "stall" && (!strings.Contains(string(body), `"code":"upstream_stream_error"`) || time.Since(start) < 2400*time.Millisecond || time.Since(start) > 4*time.Second) {
					t.Fatalf("空闲超时未从最后数据计时: elapsed=%s bodyBytes=%d", time.Since(start), len(body))
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.calls) != 3 {
				t.Fatalf("已提交输出不得重放，实际尝试=%d", len(f.calls))
			}
		})
	}
}

// TestWebSocketStreamActivity 验证真实 Responses WebSocket 的工具增量可持续超过空闲期限而正常完成。
func TestWebSocketStreamActivity(t *testing.T) {
	f := newExecutionFixture(t)
	f.provider.Request.StreamTotalMillis = 1000
	enableWebSocket(t, f, true)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, r.Context(), conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-active", "status": "in_progress"}})
		for range 6 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			data := []byte(`{"type":"response.custom_tool_call_input.delta","delta":"x"}`)
			if conn.Write(r.Context(), websocket.MessageText, data) != nil {
				return
			}
		}
		writeSocketJSON(t, r.Context(), conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-active", "status": "completed", "output": []any{}}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"long"}`)
	if socketReceive(t, conn)["type"] != "response.created" {
		t.Fatal("缺少 created")
	}
	for range 6 {
		if socketReceive(t, conn)["type"] != "response.custom_tool_call_input.delta" {
			t.Fatal("持续输出被提前中断")
		}
	}
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("持续输出缺少完整终态")
	}
}
