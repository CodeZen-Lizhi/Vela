package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/coder/websocket"
)

// TestCompactRejectsNonCompactionOutput 验证真实 compact 入口不能把普通文字或工具调用当作压缩成功。
func TestCompactRejectsNonCompactionOutput(t *testing.T) {
	for _, output := range []string{
		`[{"type":"message","content":[{"type":"output_text","text":"not a checkpoint"}]}]`,
		`[{"type":"compaction","encrypted_content":""}]`,
		`[{"type":"custom_tool_call","name":"exec","input":"x"}]`,
	} {
		f := newExecutionFixture(t)
		f.handle = func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			_, _ = io.WriteString(w, `{"object":"response.compaction","output":`+output+`}`)
		}
		status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/responses/compact", `{"model":"logical-model","input":[]}`, nil)
		if status != http.StatusBadGateway || strings.Contains(string(body), `"object":"response.compaction"`) {
			t.Fatalf("无有效压缩项仍被接受: status=%d body=%s", status, body)
		}
	}
}

// TestCompactCheckpointBinding 验证压缩生成后仍使用原 Key，原 Key 停用则拒绝，不把密文交给备用目标。
func TestCompactCheckpointBinding(t *testing.T) {
	f := newExecutionFixture(t)
	seen := make(chan string, 4)
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses/compact" {
			_, _ = io.WriteString(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"sealed-owned"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp-next","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}
	base := startCompatibilityGateway(t, f)
	if status, _ := compatibilityRequest(t, base, "/v1/responses/compact", `{"model":"logical-model","input":[]}`, nil); status != 200 {
		t.Fatal("压缩失败", status)
	}
	if key := <-seen; key != "Bearer key-a" {
		t.Fatal("初始 Key 错误", key)
	}
	body := `{"model":"logical-model","input":[{"type":"compaction","encrypted_content":"sealed-owned"}]}`
	if status, _ := compatibilityRequest(t, base, "/v1/responses", body, nil); status != 200 {
		t.Fatal("原绑定续接失败", status)
	}
	if key := <-seen; key != "Bearer key-a" {
		t.Fatal("密文错误交给其他 Key", key)
	}
	if err := f.vendors.SetKeyEnabled(context.Background(), f.keys[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if status, _ := compatibilityRequest(t, base, "/v1/responses", body, nil); status != 409 {
		t.Fatal("原绑定失效应拒绝", status)
	}
	select {
	case key := <-seen:
		t.Fatal("失效密文发送到备用", key)
	default:
	}
}

// TestWebSocketCompactionLongWait 验证原生 WS 压缩跨越普通空闲值仍能收到完整检查点与终态。
func TestWebSocketCompactionLongWait(t *testing.T) {
	f := newExecutionFixture(t)
	f.provider.Request.StreamTotalMillis = 1000
	f.provider.Request.RequestSeconds = 3
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
		writeSocketJSON(t, r.Context(), conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-ws-compact", "status": "in_progress"}})
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		writeSocketJSON(t, r.Context(), conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-ws-compact", "status": "completed", "output": []any{map[string]any{"type": "compaction", "encrypted_content": "sealed-ws"}}}})
	}
	conn := dialGatewaySocket(t, startCompatibilityGateway(t, f))
	socketSend(t, conn, `{"type":"response.create","model":"logical-model","input":[{"type":"compaction_trigger"}]}`)
	if socketReceive(t, conn)["type"] != "response.created" || socketReceive(t, conn)["type"] != "response.completed" {
		t.Fatal("WS 压缩被普通空闲超时中断")
	}
}

// TestCompactionTriggerLongWait 验证真正的压缩标记采用独立等待，不受普通首次响应与流式空闲值误杀。
func TestCompactionTriggerLongWait(t *testing.T) {
	f := newExecutionFixture(t)
	policy := f.provider.Request
	policy.Attempts, policy.FirstByteSeconds, policy.StreamTotalMillis, policy.RequestSeconds = 1, 1, 1000, 3
	if _, err := f.vendors.Update(context.Background(), f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: policy}); err != nil {
		t.Fatal(err)
	}
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-compact\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"sealed-checkpoint\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-compact\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}
	status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/responses", `{"model":"logical-model","stream":true,"input":[{"type":"compaction_trigger"}]}`, nil)
	if status != 200 || !strings.Contains(string(body), `"encrypted_content":"sealed-checkpoint"`) || !strings.Contains(string(body), `"type":"response.completed"`) {
		t.Fatalf("压缩被普通等待期限中断: status=%d bytes=%d", status, len(body))
	}
}

// TestCompactionIncompleteFailsOverWhole 验证不完整压缩不泄漏给客户端，备用成功后只交付完整结果。
func TestCompactionIncompleteFailsOverWhole(t *testing.T) {
	f := newExecutionFixture(t)
	policy := f.provider.Request
	policy.Attempts = 1
	if _, err := f.vendors.Update(context.Background(), f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: policy}); err != nil {
		t.Fatal(err)
	}
	f.handle = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Authorization") == "Bearer key-a" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"partial-checkpoint\"}}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[]}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-complete\",\"status\":\"completed\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"complete-checkpoint\"}]}}\n\n")
	}
	status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/responses", `{"model":"logical-model","stream":true,"input":[{"type":"compaction_trigger"}]}`, nil)
	if status != 200 || strings.Contains(string(body), "partial-checkpoint") || strings.Contains(string(body), "response.incomplete") || !strings.Contains(string(body), "complete-checkpoint") {
		t.Fatalf("不完整压缩被提交或未正确切换: status=%d body=%s", status, body)
	}
}
