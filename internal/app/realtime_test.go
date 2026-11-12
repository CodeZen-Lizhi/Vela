package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
)

// dialRealtime 通过带 Vela 单密钥的真实入口连接标准 Realtime，不依赖 Responses WebSocket 开关。
func dialRealtime(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, base+"/v1/realtime?model=logical-model&trace=keep", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer gateway-test-key"}, "Openai-Beta": {"realtime=v1"}, "Cookie": {"private-cookie"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// realtimeRead 读取原始帧，便于比较未知事件字段及二进制内容没有被重编码。
func realtimeRead(t *testing.T, conn *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return kind, data
}

// realtimeHistory 接通实际记录库，保持真实 Executor、transport 和上游 WebSocket。
func realtimeHistory(t *testing.T, f *executionFixture) *records.Store {
	t.Helper()
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	return history
}

// waitRealtimeRecord 等待连接级逻辑记录结束并读取含实际尝试和诊断正文的详情。
func waitRealtimeRecord(t *testing.T, history *records.Store, state string) records.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := history.Query(context.Background(), records.Query{Protocol: "openai:realtime"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) == 1 && page.Records[0].State != "active" {
			record, err := history.Get(context.Background(), page.Records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.State != state || record.Transport != "websocket" || record.Type != "realtime" {
				t.Fatalf("Realtime 结算身份或状态错误: %+v", record)
			}
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Realtime 连接记录未完成，或连接产生多条逻辑记录")
	return records.Record{}
}

// TestRealtimeRoundTripAndUsage 验证两轮累计去重、真实映射和认证、原始帧透传、音频隐私及正常关闭。
func TestRealtimeRoundTripAndUsage(t *testing.T) {
	f := newExecutionFixture(t)
	history := realtimeHistory(t, f)
	const created = `{"type":"session.created","session":{"model":"upstream-model"},"future":{"large":9007199254740993}}`
	const audio = `{"type":"input_audio_buffer.append","audio":"YXVkaW8tcHJpdmF0ZS1maXh0dXJl","future":{"large":9007199254740993}}`
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/realtime" || r.URL.Query().Get("model") != "upstream-model" || r.URL.Query().Get("trace") != "keep" || r.Header.Get("Authorization") != "Bearer key-a" || r.Header.Get("OpenAI-Beta") != "realtime=v1" {
			t.Error("Realtime 出站路径、映射、认证或协议头错误")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err = conn.Write(ctx, websocket.MessageText, []byte(created)); err != nil {
			t.Error(err)
			return
		}
		round := 0
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if kind == websocket.MessageBinary {
				if !bytes.Equal(data, []byte{0, 255, 2, 128}) {
					t.Error("二进制音频被改写")
				}
				_ = conn.Write(ctx, kind, data)
				continue
			}
			var event map[string]json.RawMessage
			_ = json.Unmarshal(data, &event)
			var eventType string
			_ = json.Unmarshal(event["type"], &eventType)
			switch eventType {
			case "session.update":
				if !bytes.Contains(data, []byte(`"model":"upstream-model"`)) || !bytes.Contains(data, []byte("9007199254740993")) || !bytes.Contains(data, []byte(`"future_session"`)) {
					t.Error("session 模型别名未映射或未知字段丢失")
				}
				_ = conn.Write(ctx, kind, data)
			case "response.create":
				round++
				done := fmt.Sprintf(`{"type":"response.done","future":"keep-raw","response":{"id":"realtime-%d","status":"completed","output":[{"audio":"YXVkaW8tcHJpdmF0ZS1maXh0dXJl"}],"usage":{"input_tokens":10,"output_tokens":4,"input_token_details":{"cached_tokens":2,"audio_tokens":3},"output_token_details":{"audio_tokens":2}}}}`, round)
				_ = conn.Write(ctx, websocket.MessageText, []byte(done))
				if round == 1 {
					_ = conn.Write(ctx, websocket.MessageText, []byte(done))
				} else {
					_ = conn.Close(websocket.StatusNormalClosure, "completed")
					return
				}
			default:
				if string(data) != audio && string(data) != "opaque future text" {
					t.Error("普通事件发生了额外重编码")
				}
				_ = conn.Write(ctx, kind, data)
			}
		}
	}
	conn := dialRealtime(t, startCompatibilityGateway(t, f))
	if kind, raw := realtimeRead(t, conn); kind != websocket.MessageText || string(raw) != created {
		t.Fatal("上游事件未原样透传")
	}
	socketSend(t, conn, `{"type":"session.update","session":{"model":"logical-model","future_session":{"large":9007199254740993}},"future":true}`)
	_, _ = realtimeRead(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0, 255, 2, 128}); err != nil {
		t.Fatal(err)
	}
	if kind, raw := realtimeRead(t, conn); kind != websocket.MessageBinary || !bytes.Equal(raw, []byte{0, 255, 2, 128}) {
		t.Fatal("二进制帧未透传")
	}
	for _, raw := range []string{audio, "opaque future text"} {
		socketSend(t, conn, raw)
		if _, received := realtimeRead(t, conn); string(received) != raw {
			t.Fatal("普通文本帧未原样透传")
		}
	}
	for i := 0; i < 2; i++ {
		socketSend(t, conn, `{"type":"response.create","response":{"modalities":["audio","text"]}}`)
		if event := socketReceive(t, conn); event["type"] != "response.done" || event["future"] != "keep-raw" {
			t.Fatal("response.done 未保持真实字段")
		}
		if i == 0 {
			_ = socketReceive(t, conn)
		}
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("正常关闭未传播: %v", err)
	}
	record := waitRealtimeRecord(t, history, "success")
	if record.UpstreamModel != "upstream-model" || len(record.Attempts) != 1 || record.Attempts[0].Status != 101 || record.Usage == nil || record.Usage.Input != 16 || record.Usage.Output != 8 || record.Usage.CacheRead != 4 || record.Usage.AudioInput != 6 || record.Usage.AudioOutput != 4 {
		t.Fatalf("连接累计用量或去重错误: %+v", record)
	}
	data, _ := json.Marshal(record)
	if bytes.Contains(data, []byte("YXVkaW8tcHJpdmF0ZS1maXh0dXJl")) || bytes.Contains(data, []byte("future_session")) || bytes.Contains(data, []byte("keep-raw")) || bytes.Contains(data, []byte("private-cookie")) {
		t.Fatal("音频或事件正文落入记录")
	}
}

// TestRealtimeLifecycle 验证连接无用量时不伪造计数，正常、异常、暂停及客户端断开按真实结果结算。
func TestRealtimeLifecycle(t *testing.T) {
	for _, mode := range []string{"normal-client", "disconnect", "pause", "upstream-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecutionFixture(t)
			history := realtimeHistory(t, f)
			upstreamDone := make(chan struct{})
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				defer close(upstreamDone)
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.created"}`))
				_, _, _ = conn.Read(ctx)
			}
			conn := dialRealtime(t, startCompatibilityGateway(t, f))
			_ = socketReceive(t, conn)
			state := "failed"
			switch mode {
			case "normal-client":
				state = "success"
				_ = conn.Close(websocket.StatusNormalClosure, "done")
			case "disconnect":
				_ = conn.CloseNow()
			case "pause":
				if err := f.gateway.Pause(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "upstream-failure":
				socketSend(t, conn, `{"type":"response.create"}`)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, _, err := conn.Read(ctx); err == nil {
					t.Fatal("上游异常被伪装为有效事件")
				}
			}
			select {
			case <-upstreamDone:
			case <-time.After(3 * time.Second):
				t.Fatal("连接终止后上游未释放")
			}
			record := waitRealtimeRecord(t, history, state)
			if len(record.Attempts) != 1 || record.Attempts[0].Status != 101 || record.Usage != nil {
				t.Fatal("连接没有真实用量却产生计数，或连接结束后重试")
			}
			if (mode == "disconnect" || mode == "pause") && !strings.Contains(record.Error, "canceled") {
				t.Fatalf("取消原因未保留: %s", record.Error)
			}
		})
	}
}

// TestRealtimeModelBoundary 验证 session.update 和 response.create 都不能改变既有模型绑定。
func TestRealtimeModelBoundary(t *testing.T) {
	for _, event := range []string{`{"type":"session.update","session":{"model":"forbidden-model"}}`, `{"type":"response.create","response":{"model":"forbidden-model"}}`} {
		t.Run(event, func(t *testing.T) {
			f := newExecutionFixture(t)
			history := realtimeHistory(t, f)
			var leaked atomic.Bool
			f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.created"}`))
				if _, _, err := conn.Read(ctx); err == nil {
					leaked.Store(true)
				}
			}
			conn := dialRealtime(t, startCompatibilityGateway(t, f))
			_ = socketReceive(t, conn)
			socketSend(t, conn, event)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("越权模型未拒绝: %v", err)
			}
			record := waitRealtimeRecord(t, history, "failed")
			if leaked.Load() || len(record.Attempts) != 1 || record.Usage != nil {
				t.Fatal("越权模型到达上游、发生重试或生成假用量")
			}
		})
	}
}

// TestRealtimeHandshakeRetry 验证只有握手失败可有限换 Key；实际握手状态与成功连接用量均保存。
func TestRealtimeHandshakeRetry(t *testing.T) {
	f := newExecutionFixture(t)
	history := realtimeHistory(t, f)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Header.Get("Authorization") == "Bearer key-a" {
			w.WriteHeader(http.StatusServiceUnavailable)
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
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.done","response":{"id":"retry-success","usage":{"input_tokens":9,"output_tokens":2}}}`))
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}
	conn := dialRealtime(t, startCompatibilityGateway(t, f))
	if socketReceive(t, conn)["type"] != "response.done" {
		t.Fatal("握手失败后未进入可用 Key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)
	record := waitRealtimeRecord(t, history, "success")
	if len(record.Attempts) < 2 || len(record.Attempts) > 3 || record.Attempts[0].Status != 503 || record.Attempts[len(record.Attempts)-1].Status != 101 || record.Usage == nil || record.Usage.Input != 9 || record.Usage.Output != 2 {
		t.Fatalf("握手重试与记录错误: %+v", record)
	}
}

// TestRealtimeProxyAndPermission 验证真实 Key 权限过滤和供应商继承全局代理，不能绕过源配置。
func TestRealtimeProxyAndPermission(t *testing.T) {
	f := newExecutionFixture(t)
	if err := f.vendors.SaveModelPermissions(context.Background(), f.keys[0].ID, providers.ProviderModelPermissions{AllModels: false}); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(f.provider.URL)
	var proxied atomic.Int32
	reverse := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); reverse.ServeHTTP(w, r) }))
	defer proxy.Close()
	if err := f.catalog.SetProxy(context.Background(), models.Proxy{Enabled: true, URL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	request := f.provider.Request
	request.Proxy = providers.ProxyChoice{Mode: providers.ProxyGlobal}
	if _, err := f.vendors.Update(context.Background(), f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: request}); err != nil {
		t.Fatal(err)
	}
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Header.Get("Authorization") != "Bearer key-b" {
			t.Error("Realtime 绕过了 Key 模型权限")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.created"}`))
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}
	conn := dialRealtime(t, startCompatibilityGateway(t, f))
	_ = socketReceive(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)
	if proxied.Load() != 1 {
		t.Fatalf("Realtime 未经过有效代理: %d", proxied.Load())
	}
}

// TestRealtimeAuthenticationAndQuery 验证握手前的统一密钥、模型清单、唯一模型及敏感参数边界。
func TestRealtimeAuthenticationAndQuery(t *testing.T) {
	f := newExecutionFixture(t)
	base := startCompatibilityGateway(t, f)
	for _, tc := range []struct {
		// query 为客户端 URL 查询串；key 是 Vela 单密钥；status 为预期的握手拒绝状态。
		query, key string
		status     int
	}{
		{"model=logical-model", "wrong-key", 401},
		{"model=unknown", "gateway-test-key", 404},
		{"", "gateway-test-key", 400},
		{"model=logical-model&MODEL=other", "gateway-test-key", 400},
		{"model=logical-model&api_key=private", "gateway-test-key", 400},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, response, err := websocket.Dial(ctx, base+"/v1/realtime?"+tc.query, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tc.key}}})
		cancel()
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Errorf("握手边界错误: response=%v err=%v", response, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("无效认证或模型参数触发上游调用")
	}
}
