package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// auditRecords 使用隔离数据库接入真实网关记录。
func auditRecords(t *testing.T, f *executionFixture) *records.Store {
	t.Helper()
	s, e := records.New(f.catalog.DB())
	if e != nil {
		t.Fatal(e)
	}
	f.execution.Executor.Records = s
	return s
}

// auditDetail 重读真实落库终态。
func auditDetail(t *testing.T, s *records.Store) records.Record {
	t.Helper()
	p, e := s.Query(context.Background(), records.Query{})
	if e != nil || len(p.Records) != 1 {
		t.Fatalf("page=%+v err=%v", p, e)
	}
	r, e := s.Get(context.Background(), p.Records[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if r.Client == nil || len(r.Attempts) == 0 || r.Attempts[0].Payload == nil {
		t.Fatalf("缺少客户端或真实上游快照: %+v", r)
	}
	return r
}

// TestAuditLargeJSONUsage 验证超过快照上限的合法 JSON 完整转发且真实用量和有界快照均落库。
func TestAuditLargeJSONUsage(t *testing.T) {
	f := newExecutionFixture(t)
	s := auditRecords(t, f)
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("a", (1<<20)+20) + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`
	f.handle = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
	status, out := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","messages":[]}`, nil)
	r := auditDetail(t, s)
	t.Logf("HTTP=%d bytes=%d state=%s usage=%+v attemptUsage=%+v bodyState=%s", status, len(out), r.State, r.Usage, r.Attempts[0].Usage, r.Attempts[0].Payload.BodyState)
	if status != 200 || r.State != "success" || !bytes.Equal(out, []byte(body)) || r.Usage == nil || r.Usage.Input != 100 || r.Usage.Output != 20 || r.Attempts[0].Usage == nil || r.Attempts[0].Payload.BodyState != "truncated" || len(r.Attempts[0].Payload.ResponseBody) > 1<<20 {
		t.Fatal("完整合法响应携带usage，但持久化丢失usage")
	}
}

// TestAuditOpenAICacheWrite 验证 Chat 与 Responses 的缓存写入沿旧映射语义持久化，并从普通输入中扣除。
func TestAuditOpenAICacheWrite(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			f := newExecutionFixture(t)
			s := auditRecords(t, f)
			path, body := "/v1/chat/completions", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}`
			if protocol == "responses" {
				path = "/v1/responses"
				body = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":30,"cached_creation_tokens":40}}}`
			}
			f.handle = func(w http.ResponseWriter, r *http.Request, b map[string]any) { io.WriteString(w, body) }
			status, _ := compatibilityRequest(t, startCompatibilityGateway(t, f), path, `{"model":"logical-model","messages":[]}`, nil)
			r := auditDetail(t, s)
			t.Logf("HTTP=%d state=%s usage=%+v", status, r.State, r.Usage)
			if status != 200 || r.Usage == nil || r.Usage.CacheWrite != 40 || r.Usage.CacheRead != 30 || r.Usage.Input != 30 || r.Usage.Context != 100 {
				t.Fatal("已报告的OpenAI缓存写入量落成零")
			}
		})
	}
}

// TestAuditGatewayErrorBody 验证客户端实际收到的最终错误正文与失败上游正文分别落库。
func TestAuditGatewayErrorBody(t *testing.T) {
	f := newExecutionFixture(t)
	s := auditRecords(t, f)
	f.handle = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":"test_bad_input","message":"upstream detail"}}`)
	}
	status, out := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","messages":[]}`, nil)
	r := auditDetail(t, s)
	t.Logf("HTTP=%d received=%s state=%s clientBody=%q upstreamBody=%q", status, out, r.State, r.Client.ResponseBody, r.Attempts[0].Payload.ResponseBody)
	var received, saved any
	if err := json.Unmarshal(out, &received); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(r.Client.ResponseBody), &saved); err != nil {
		t.Fatal(err)
	}
	if status != 400 || !reflect.DeepEqual(received, saved) || !strings.Contains(r.Attempts[0].Payload.ResponseBody, "upstream detail") {
		t.Fatal("客户端实际错误正文未记录")
	}
}

// TestAuditSSEMultilineUsage 验证多行 data 合并成完整 SSE 事件后提取用量，转发内容保持完整。
func TestAuditSSEMultilineUsage(t *testing.T) {
	f := newExecutionFixture(t)
	s := auditRecords(t, f)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":\ndata: {\"prompt_tokens\":100,\"completion_tokens\":20}}\n\ndata: [DONE]\n\n"
	f.handle = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}
	status, out := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","stream":true,"messages":[]}`, nil)
	r := auditDetail(t, s)
	t.Logf("HTTP=%d state=%s usage=%+v", status, r.State, r.Usage)
	if status != 200 || !bytes.Equal(out, []byte(body)) || r.Usage == nil || r.Usage.Input != 100 || r.Usage.Output != 20 {
		t.Fatal("合法多行SSE已转发成功但用量未保存")
	}
}

// TestAuditRequestCaptureState 验证原始请求超过快照上限时，即使客户端响应很短仍保留截断状态。
func TestAuditRequestCaptureState(t *testing.T) {
	f := newExecutionFixture(t)
	s := auditRecords(t, f)
	b, _ := json.Marshal(map[string]any{"model": "logical-model", "messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", (1<<20)+100)}}})
	status, _ := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", string(b), nil)
	r := auditDetail(t, s)
	t.Logf("HTTP=%d originalBytes=%d savedBytes=%d state=%s validJSON=%v", status, len(b), len(r.Client.RequestBody), r.Client.BodyState, json.Valid([]byte(r.Client.RequestBody)))
	if status != 200 || r.Client.BodyState != "truncated" || len(r.Client.RequestBody) > (1<<20)+64 {
		t.Fatal("已截断的客户端请求被标记为available")
	}
}

// TestAuditRetriedProtocolFailure 验证最终成功不能掩盖已失败尝试的协议错误。
func TestAuditRetriedProtocolFailure(t *testing.T) {
	f := newExecutionFixture(t)
	s := auditRecords(t, f)
	f.handle = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Authorization") == "Bearer key-a" {
			io.WriteString(w, "data: {\"error\":{\"code\":\"test_stream_failure\",\"message\":\"failure before commit\"}}\n\n")
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}
	status, _ := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","stream":true,"messages":[]}`, nil)
	r := auditDetail(t, s)
	t.Logf("HTTP=%d state=%s attempts=%d firstStatus=%d firstError=%q finalError=%q firstBody=%q", status, r.State, len(r.Attempts), r.Attempts[0].Status, r.Attempts[0].Error, r.Error, r.Attempts[0].Payload.ResponseBody)
	if status != 200 || r.State != "success" || len(r.Attempts) != 3 || r.Attempts[2].Error != "" || r.Error != "" {
		t.Fatal("失败尝试缺少执行错误事实，仅正文暂存了错误")
	}
	for _, attempt := range r.Attempts[:2] {
		if attempt.Status != 200 || attempt.Error == "" {
			t.Fatalf("重试前协议失败未记录: %+v", attempt)
		}
	}
}
