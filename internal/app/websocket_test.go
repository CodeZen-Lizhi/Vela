package app

import (
	"context"
	"encoding/json"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
)

// enableWebSocket 通过真实供应商配置保存开关，不绕过加密文档与运行时快照。
func enableWebSocket(t *testing.T, f *executionFixture, enabled bool) {
	t.Helper()
	draft := providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: f.provider.Request}
	draft.Request.ResponsesWebSocket = enabled
	provider, err := f.vendors.Update(context.Background(), f.provider.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	f.provider = provider
}

// dialGatewaySocket 连接真实带鉴权网关，测试结束即关闭，不占用真实供应商服务。
func dialGatewaySocket(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer gateway-test-key"}}})
	if err != nil {
		t.Fatalf("客户端握手失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// socketSend 发送一个标准 response.create JSON 文本消息。
func socketSend(t *testing.T, conn *websocket.Conn, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

// socketReceive 读取一条真实 WebSocket 事件并校验文本 JSON，不把 HTTP/SSE 结果当作 WebSocket 验收。
func socketReceive(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if kind != websocket.MessageText || json.Unmarshal(raw, &event) != nil {
		t.Fatalf("非法事件: %s", raw)
	}
	return event
}

// writeSocketJSON 由严格本地上游发送原生事件，不经过模拟执行器。
func writeSocketJSON(t *testing.T, ctx context.Context, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Error(err)
		return
	}
	if err = conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Error(err)
	}
}

// TestWebSocketToolRoundTripAndRecords 验证实际双端 WebSocket、原连接复用、R52、模型继承、工具往返、用量与传输类型落库。
func TestWebSocketToolRoundTripAndRecords(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	seen := make(chan map[string]any, 2)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer key-a" {
			t.Error("上游握手方法或认证错误")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		for i := 0; i < 2; i++ {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			var body map[string]any
			if json.Unmarshal(data, &body) != nil {
				t.Error("正文不是 JSON")
				return
			}
			seen <- body
			if body["model"] != "upstream-model" || body["type"] != "response.create" || body["stream"] != nil || body["background"] != nil {
				t.Error("WebSocket 请求整理错误")
			}
			id := []string{"resp-ws-1", "resp-ws-2"}[i]
			writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress"}})
			var output any = []any{map[string]any{"type": "function_call", "call_id": "call-ws", "name": "shell", "arguments": "{}"}}
			if i == 1 {
				output = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}
			}
			writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}}})
		}
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","stream":true,"background":false,"input":[{"type":"function_call_output","output":"{\"page_id\":null}"}]}`)
	if socketReceive(t, conn)["type"] != "response.created" || socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("第一轮协议不完整")
	}
	first := <-seen
	input := first["input"].([]any)
	if input[0].(map[string]any)["role"] != "user" {
		t.Fatal("R52 未接入 WebSocket")
	}
	socketSend(t, conn, `{"type":"response.create","previous_response_id":"resp-ws-1","input":[{"type":"function_call_output","call_id":"call-ws","output":"tool complete"}]}`)
	if socketReceive(t, conn)["type"] != "response.created" || socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("工具结果续接失败")
	}
	second := <-seen
	if second["previous_response_id"] != "resp-ws-1" || second["input"].([]any)[0].(map[string]any)["call_id"] != "call-ws" {
		t.Fatal("工具配对或响应 ID 被改写")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		page, err := history.Query(context.Background(), records.Query{Transports: []string{"websocket"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) == 2 && page.Records[0].State == "success" && page.Records[1].State == "success" {
			for _, row := range page.Records {
				if row.Transport != "websocket" || row.Usage == nil || row.Usage.Output != 2 || row.Usage.Input != 10 {
					t.Fatalf("记录或用量错误: %+v", row)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("WebSocket 记录未完成")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("后续轮次未复用物理连接: %d", len(f.calls))
	}
}

// TestWebSocketSwitchAndHTTPBoundary 验证开关关闭不会调用上游 WS，开启后也不改变普通 HTTP Responses 的传输。
func TestWebSocketSwitchAndHTTPBoundary(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != "POST" {
			t.Error("HTTP 请求错误升级到 WebSocket")
		}
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}
	base := startCompatibilityGateway(t, f)
	conn := dialGatewaySocket(t, base)
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if event := socketReceive(t, conn); event["type"] != "error" || event["status"] != float64(503) {
		t.Fatal(event)
	}
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("开关关闭仍访问上游")
	}
	enableWebSocket(t, f, true)
	status, _ := compatibilityRequest(t, base, "/v1/responses", `{"model":"logical-model","input":"hi"}`, nil)
	if status != 200 {
		t.Fatal("启用开关破坏 HTTP 路径")
	}
}

// TestWebSocketWarmupAndAuthRefresh 验证 generate:false 保留到上游并接受无生成终态，下一轮必须重新校验网关密钥。
func TestWebSocketWarmupAndAuthRefresh(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	candidates, err := f.execution.Executor.Source.Candidates(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.execution.Executor.Runtime.Acquire(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish(scheduling.Outcome{Penalty: scheduling.Heavy})
	var turns atomic.Int32
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		if body["generate"] != false || body["store"] != false {
			t.Error("WebSocket 预热控制字段被改写")
		}
		turns.Add(1)
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-warm", "status": "completed", "output": []any{}}})
		_, _, _ = conn.Read(ctx)
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","generate":false,"store":false,"input":[]}`)
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("预热失败")
	}
	f.gateway.SetAccessKey("new-access-key")
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"next"}`)
	if event := socketReceive(t, conn); event["status"] != float64(401) {
		t.Fatal("长连接未重验认证", event)
	}
	keys, err := f.vendors.Keys(context.Background(), f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].HealthScore != 8 {
		t.Fatal("预热不应给模型健康回分", keys[0].HealthScore)
	}
	if turns.Load() != 1 {
		t.Fatal("已撤销认证仍发送上游")
	}
}

// TestWebSocketReplayBoundary 验证握手前 503 可换 Key，而 response.created 已公开响应 ID 后断开绝不重放。
func TestWebSocketReplayBoundary(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "handshake-retry", true: "created-no-replay"}[committed], func(t *testing.T) {
			f := newExecutionFixture(t)
			enableWebSocket(t, f, true)
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				if !committed && r.Header.Get("Authorization") == "Bearer key-a" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"type":"overloaded_error"}}`)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				if _, _, err = conn.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-boundary", "status": "in_progress"}})
				if !committed {
					writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-boundary", "status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
				}
			}
			conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
			socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
			if socketReceive(t, conn)["type"] != "response.created" {
				t.Fatal("缺少 created")
			}
			terminal := socketReceive(t, conn)
			if committed && terminal["type"] != "error" || !committed && terminal["type"] != "response.completed" {
				t.Fatal(terminal)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 1
			if !committed {
				want = 3
			}
			if len(f.calls) != want {
				t.Fatalf("重放边界错误: calls=%d want=%d", len(f.calls), want)
			}
		})
	}
}

// TestWebSocketPauseAndDisconnect 通过真实断连和暂停验证上游 IO 被取消，并发资格和升级连接被释放。
func TestWebSocketPauseAndDisconnect(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "pause"}[pause], func(t *testing.T) {
			f := newExecutionFixture(t)
			enableWebSocket(t, f, true)
			closed := make(chan struct{})
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				defer close(closed)
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				if _, _, err = conn.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-active", "status": "in_progress"}})
				_, _, _ = conn.Read(ctx)
			}
			conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
			socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
			if socketReceive(t, conn)["type"] != "response.created" {
				t.Fatal("未启动生成")
			}
			if pause {
				if err := f.gateway.Pause(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = conn.CloseNow()
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("上游连接未及时释放")
			}
		})
	}
}

// TestWebSocketToggleRefresh 验证已有连接的下一轮会读取最新供应商开关，不冻结握手时的配置。
func TestWebSocketToggleRefresh(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	var turns atomic.Int32
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		turns.Add(1)
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-toggle", "status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
		_, _, _ = conn.Read(ctx)
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("未完成")
	}
	enableWebSocket(t, f, false)
	socketSend(t, conn, `{"type":"response.create","previous_response_id":"resp-toggle","input":"next"}`)
	if event := socketReceive(t, conn); event["status"] != float64(409) {
		t.Fatal("关闭后仍可续接", event)
	}
	if turns.Load() != 1 {
		t.Fatal("开关关闭仍访问上游")
	}
}

// TestWebSocketBindingChangeRequiresReconnect 验证已成功会话在模型、凭据或地址变化后返回 409 并断开，拒绝透明切换；新连接仍可调用。
func TestWebSocketBindingChangeRequiresReconnect(t *testing.T) {
	for _, change := range []string{"model", "credential", "url"} {
		t.Run(change, func(t *testing.T) {
			f := newExecutionFixture(t)
			enableWebSocket(t, f, true)
			var turns atomic.Int32
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				if _, _, err = conn.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				turns.Add(1)
				writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-binding", "status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
				_, _, _ = conn.Read(ctx)
			}
			base := startCompatibilityGateway(t, f)
			conn := dialGatewaySocket(t, base)
			socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
			if event := socketReceive(t, conn); event["type"] != "response.completed" {
				t.Fatal(event)
			}
			model := "logical-model"
			ctx := context.Background()
			switch change {
			case "model":
				model = "other-model"
				if _, err := f.catalog.DB().Exec(`INSERT INTO models VALUES('model-2','other-model','{"id":"model-2","modelId":"other-model","name":"Other","kind":"chat"}')`); err != nil {
					t.Fatal(err)
				}
				if err := f.vendors.SaveModels(ctx, f.provider.ID, providers.ProviderModelsDraft{ModelIDs: []string{"model-1", "model-2"}}); err != nil {
					t.Fatal(err)
				}
			case "credential":
				key := f.keys[0]
				if _, err := f.vendors.UpdateKey(ctx, key.ID, providers.ProviderKeyDraft{Name: key.Name, Secret: "rotated-key", Multiplier: key.Multiplier, Priority: key.Priority, CacheMinutes: key.CacheMinutes, ProbeMinutes: key.ProbeMinutes, Proxy: key.Proxy}); err != nil {
					t.Fatal(err)
				}
			case "url":
				if _, err := f.vendors.Update(ctx, f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: strings.Replace(f.provider.URL, "127.0.0.1", "localhost", 1), Request: f.provider.Request}); err != nil {
					t.Fatal(err)
				}
			}
			body := `{"type":"response.create","model":"` + model + `","input":"next"}`
			socketSend(t, conn, body)
			if event := socketReceive(t, conn); event["type"] != "error" || event["status"] != float64(409) {
				t.Fatal("绑定失效未明确返回 409", event)
			}
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, _, err := conn.Read(readCtx); err == nil || readCtx.Err() != nil {
				t.Fatalf("失效会话未主动断开: %v", err)
			}
			if turns.Load() != 1 {
				t.Fatal("失效会话透明调用了新目标")
			}
			fresh := dialGatewaySocket(t, base)
			socketSend(t, fresh, body)
			if event := socketReceive(t, fresh); event["type"] != "response.completed" || turns.Load() != 2 {
				t.Fatal("重新连接仍无法调用", event)
			}
		})
	}
}

// TestWebSocketUsesGlobalProxy 验证握手实际经过项目已有代理客户端，原生 WebSocket 不绕过供应商出口继承。
func TestWebSocketUsesGlobalProxy(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	target, err := url.Parse(f.provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	var proxied atomic.Int32
	reverse := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); reverse.ServeHTTP(w, r) }))
	defer proxy.Close()
	if err = f.catalog.SetProxy(context.Background(), models.Proxy{Enabled: true, URL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	f.provider.Request.Proxy = providers.ProxyChoice{Mode: providers.ProxyGlobal}
	enableWebSocket(t, f, true)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "proxied"}}}}}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.completed" || proxied.Load() != 1 {
		t.Fatal("WebSocket 未经过全局代理", proxied.Load())
	}
}

// TestWebSocketLargeTerminalUsage 验证超过正文快照上限的终态仍可提取实际用量，不因诊断截断丢失计费事实。
func TestWebSocketLargeTerminalUsage(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": strings.Repeat("a", 2<<20)}}}}, "usage": map[string]any{"input_tokens": 23, "output_tokens": 17}}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	conn.SetReadLimit(16 << 20)
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("大终态未完整送达")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		page, err := history.Query(context.Background(), records.Query{Transports: []string{"websocket"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) == 1 && page.Records[0].State == "success" {
			if page.Records[0].Usage == nil || page.Records[0].Usage.Output != 17 {
				t.Fatal("大终态用量丢失")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("大终态记录未完成")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWebSocketIdlePing 验证两轮生成之间上游 Ping 也能得到 Pong，不依赖下一轮请求唤醒读取。
func TestWebSocketIdlePing(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	pinged := make(chan error, 1)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
		finished := make(chan struct{})
		go func() { defer close(finished); _, _, _ = conn.Read(ctx) }()
		pingCtx, pingCancel := context.WithTimeout(ctx, time.Second)
		err = conn.Ping(pingCtx)
		pingCancel()
		pinged <- err
		<-finished
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("生成未完成")
	}
	select {
	case err := <-pinged:
		if err != nil {
			t.Fatal("空闲 Ping 未应答", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("空闲 Ping 超时")
	}
	_ = conn.CloseNow()
}

// TestWebSocketClientErrorIsNeutral 验证结构化的客户端请求错误不误扣上游健康，也不重复提交生成。
func TestWebSocketClientErrorIsNeutral(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "error", "status": 400, "error": map[string]any{"type": "invalid_request_error", "message": "bad client input"}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":null}`)
	if event := socketReceive(t, conn); event["status"] != float64(400) {
		t.Fatal("客户端错误分类不正确", event)
	}
	keys, err := f.vendors.Keys(context.Background(), f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].HealthScore != 10 {
		t.Fatal("客户端请求错误误扣上游健康分")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatal("客户端请求错误被重试")
	}
}

// TestWebSocketValidationAndOverlap 验证不支持的命名通道和重叠生成明确拒绝，不额外发送供应商请求。
func TestWebSocketValidationAndOverlap(t *testing.T) {
	f := newExecutionFixture(t)
	enableWebSocket(t, f, true)
	finish := make(chan struct{})
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-overlap", "status": "in_progress"}})
		select {
		case <-finish:
		case <-ctx.Done():
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","stream_id":"named-lane","input":"hi"}`)
	if socketReceive(t, conn)["error"].(map[string]any)["code"] != "named_stream_unsupported" {
		t.Fatal("命名通道未拒绝")
	}
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.created" {
		t.Fatal("未开始生成")
	}
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"overlap"}`)
	if socketReceive(t, conn)["error"].(map[string]any)["code"] != "response_in_progress" {
		t.Fatal("重叠生成未拒绝")
	}
	close(finish)
	if socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("拒绝重叠请求破坏原生成")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatal("无效事件发往供应商")
	}
}

// TestWebSocketTurnIdleTimeout 验证真实连接停止返回数据后按空闲期限中断，并向客户端明确报告失败。
func TestWebSocketTurnIdleTimeout(t *testing.T) {
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
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		writeSocketJSON(t, ctx, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-timeout", "status": "in_progress"}})
		_, _, _ = conn.Read(ctx)
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	started := time.Now()
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":"hi"}`)
	if socketReceive(t, conn)["type"] != "response.created" || socketReceive(t, conn)["type"] != "error" {
		t.Fatal("期限未中断生成")
	}
	if elapsed := time.Since(started); elapsed < 800*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatal("单轮空闲超时未生效", elapsed)
	}
}
