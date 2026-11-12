package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// startCompatibilityGateway 以隔离数据库和真实执行器启动带认证的本机网关，测试结束关闭监听。
func startCompatibilityGateway(t *testing.T, f *executionFixture) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	server := gateway.New(f.catalog, f.execution.Executor)
	f.gateway = server
	server.SetAccessKey("gateway-test-key")
	if err = server.Listen(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return "http://" + address
}

// compatibilityRequest 发送真实本机 HTTP 请求并读完正文，使用固定测试认证，不访问真实供应商。
func compatibilityRequest(t *testing.T, base, path, body string, headers http.Header) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	if request.Header.Get("Authorization") == "" && request.Header.Get("x-api-key") == "" && request.Header.Get("x-goog-api-key") == "" && !strings.Contains(path, "key=") {
		request.Header.Set("Authorization", "Bearer gateway-test-key")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

// TestGatewayCodexStandaloneContext 通过严格上游验收 HTTP/SSE 两条实际发送路径的首轮快照、图片及正常工具往返。
func TestGatewayCodexStandaloneContext(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			f := newExecutionFixture(t)
			observed := make(chan map[string]any, 2)
			f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				observed <- body
				input, _ := body["input"].([]any)
				valid := len(input) > 0
				for _, raw := range input {
					item, _ := raw.(map[string]any)
					if item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output" {
						id, _ := item["call_id"].(string)
						valid = valid && id != ""
					}
				}
				if !valid {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"message":"function_call_output requires call_id"}}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-context\",\"status\":\"completed\",\"output\":[]}}\n\n")
				} else {
					_, _ = io.WriteString(w, `{"id":"resp-context","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)
				}
			}
			base := startCompatibilityGateway(t, f)
			body := `{"model":"logical-model","stream":STREAM,"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"{\"page_id\":null}"},{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"custom_tool_call_output","call_id":"","output":[{"type":"text","text":"snapshot"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},{"type":"function_call_output","call_id":"c1","output":"tool result"}]}`
			body = strings.ReplaceAll(body, "STREAM", map[bool]string{false: "false", true: "true"}[stream])
			status, data := compatibilityRequest(t, base, "/v1/responses", body, nil)
			if status != 200 || !bytes.Contains(data, []byte("OK")) {
				t.Fatalf("首轮上下文失败: %d %s", status, data)
			}
			got := <-observed
			input := got["input"].([]any)
			first := input[0].(map[string]any)
			if first["role"] != "user" || input[2].(map[string]any)["call_id"] != "c1" {
				t.Fatalf("独立通知或配对顺序不符: %v", input)
			}
			encoded, _ := json.Marshal(input[3])
			if !bytes.Contains(encoded, []byte("input_text")) || !bytes.Contains(encoded, []byte("data:image/png;base64,AQID")) {
				t.Fatal("文字或图片丢失")
			}
			next := strings.Replace(body, `"input":[`, `"previous_response_id":"resp-context","input":[`, 1)
			status, _ = compatibilityRequest(t, base, "/v1/responses", next, nil)
			if status != 200 {
				t.Fatalf("已绑定工具往返失败: %d", status)
			}
			if (<-observed)["previous_response_id"] != "resp-context" {
				t.Fatal("续接标识被改变")
			}
		})
	}
}

// TestGatewayNativeProtocols 通过同一本机网关验证 Anthropic/Gemini 原生认证、路径、正文和流式输出，不发生跨格式转换。
func TestGatewayNativeProtocols(t *testing.T) {
	for _, tc := range []struct {
		// name、path、body、header、response 描述客户端协议样例；stream 控制上游 SSE。
		name, path, body, header, response string
		stream                             bool
	}{
		{"anthropic", "/v1/messages", `{"model":"logical-model","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, "x-api-key", `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`, false},
		{"anthropic-sse", "/v1/messages", `{"model":"logical-model","stream":true,"messages":[],"max_tokens":16}`, "x-api-key", "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", true},
		{"gemini", "/v1beta/models/logical-model:generateContent?key=gateway-test-key", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`, false},
		{"gemini-sse", "/v1beta/models/logical-model:streamGenerateContent", `{"contents":[]}`, "x-goog-api-key", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				if strings.Contains(tc.name, "gemini") {
					if !strings.Contains(r.URL.Path, "/models/upstream-model:") || body["model"] != nil || r.Header.Get("x-goog-api-key") != "key-a" || r.URL.Query().Get("key") != "" {
						t.Error("Gemini 路径/模型/认证错误")
					}
					if tc.stream && r.URL.Query().Get("alt") != "sse" {
						t.Error("缺少 Gemini SSE 参数")
					}
				} else if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "key-a" || r.Header.Get("Anthropic-Version") == "" {
					t.Error("Anthropic 路径/认证/版本错误")
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, tc.response)
			}
			headers := make(http.Header)
			if tc.header != "" {
				headers.Set(tc.header, "gateway-test-key")
			}
			status, data := compatibilityRequest(t, startCompatibilityGateway(t, f), tc.path, tc.body, headers)
			if status != 200 || string(data) != tc.response {
				t.Fatalf("原生协议失败: %d %s", status, data)
			}
		})
	}
}

// TestGatewayUsageAndPersistenceIsolation 验证主动请求 Chat SSE 用量后可落库，本地健康保存故障不改写成功业务结果。
func TestGatewayUsageAndPersistenceIsolation(t *testing.T) {
	f := newExecutionFixture(t)
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	f.execution.Executor.Persist = func(context.Context) error { return errors.New("injected health save failure") }
	f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		options, _ := body["stream_options"].(map[string]any)
		if options["include_usage"] != true || options["custom"] != "keep" {
			t.Error("流式用量参数未合并")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: [DONE]\n\n")
	}
	status, _ := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","stream":true,"stream_options":{"custom":"keep","include_usage":false},"messages":[]}`, nil)
	if status != 200 {
		t.Fatal(status)
	}
	page, err := history.Query(context.Background(), records.Query{})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("记录查询: %v", err)
	}
	row := page.Records[0]
	if row.State != "success" || row.Usage == nil || row.Usage.Output != 2 || row.Usage.Input != 5 {
		t.Fatalf("业务结果或用量错误: %+v", row)
	}
}

// TestContinuationRejectsChangedBinding 验证未知主体、模型映射和凭据变更后不将连续请求转到其他 Key。
func TestContinuationRejectsChangedBinding(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		_, _ = io.WriteString(w, `{"id":"resp-owned","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}
	req := f.request()
	req.Request.Protocol = "openai:responses"
	req.Path = "/v1/responses"
	req.Body = []byte(`{"model":"logical-model","input":"hi"}`)
	req.Principal = "owner"
	var out bytes.Buffer
	if _, err := f.execution.Executor.Execute(context.Background(), req, &out); err != nil {
		t.Fatal(err)
	}
	req.Body = []byte(`{"model":"logical-model","previous_response_id":"resp-owned","input":"next"}`)
	req.Principal = "different"
	if _, err := f.execution.Executor.Execute(context.Background(), req, &out); !errors.Is(err, scheduling.ErrContinuation) {
		t.Fatal("跨主体续接未拒绝", err)
	}
	req.Principal = "owner"
	if _, err := f.catalog.DB().Exec(`UPDATE provider_keys SET enabled=0 WHERE id=?`, f.keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Executor.Execute(context.Background(), req, &out); !errors.Is(err, scheduling.ErrContinuation) {
		t.Fatal("失效原绑定不应换 Key", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("拒绝后仍出站: %d", len(f.calls))
	}
}

// TestLocalCaptureFailureDoesNotPenalizeUpstream 注入出站前的记录写失败，验证没有网络请求、没有重试且 Key 不扣分。
func TestLocalCaptureFailureDoesNotPenalizeUpstream(t *testing.T) {
	f := newExecutionFixture(t)
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	_, err = f.catalog.DB().Exec(`CREATE TRIGGER fail_attempt_save BEFORE UPDATE ON request_records WHEN json_array_length(json_extract(NEW.document,'$.attempts')) > 0 BEGIN SELECT RAISE(FAIL,'injected capture failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","messages":[]}`, nil)
	if status != 500 {
		t.Fatalf("本地错误状态=%d", status)
	}
	keys, err := f.vendors.Keys(context.Background(), f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.HealthScore != 10 {
			t.Fatalf("本地故障误扣分: %v", key.HealthScore)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("出站前故障不应重试或访问上游")
	}
}

// TestEmptyResponsesRetryBeforeCommit 验证空回答不能算成功，预算内可切换备用并只转发有效内容。
func TestEmptyResponsesRetryBeforeCommit(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Header.Get("Authorization") == "Bearer key-a" {
			_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fallback"}]}]}`)
	}
	status, data := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/responses", `{"model":"logical-model","input":"hi"}`, nil)
	if status != 200 || !bytes.Contains(data, []byte("fallback")) {
		t.Fatalf("空回答未回退: %d %s", status, data)
	}
	keys, err := f.vendors.Keys(context.Background(), f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].HealthScore != 6 {
		t.Fatalf("空回答健康判定错误: %v", keys[0].HealthScore)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 3 {
		t.Fatalf("调用次数=%d", len(f.calls))
	}
}

// TestRankedOutCandidatesKeepReasons 验证在排序阶段已被过滤的并发、RPM、冷却候选仍有明确未执行原因。
func TestRankedOutCandidatesKeepReasons(t *testing.T) {
	for _, kind := range []string{"concurrency", "rpm", "cooldown"} {
		t.Run(kind, func(t *testing.T) {
			f := newExecutionFixture(t)
			history, err := records.New(f.catalog.DB())
			if err != nil {
				t.Fatal(err)
			}
			f.execution.Executor.Records = history
			for _, key := range f.keys {
				rpm := 1
				if kind == "cooldown" {
					rpm = 0
				}
				_, err = f.vendors.UpdateKey(context.Background(), key.ID, providers.ProviderKeyDraft{Name: key.Name, Multiplier: key.Multiplier, Priority: key.Priority, RPMLimit: rpm, ConcurrencyLimit: 1, CacheMinutes: 5, ProbeMinutes: 10, Proxy: key.Proxy})
				if err != nil {
					t.Fatal(err)
				}
			}
			req := f.request()
			candidates, err := f.execution.Executor.Source.Candidates(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var leases []*scheduling.Attempt
			for _, c := range candidates {
				lease, err := f.execution.Executor.Runtime.Acquire(c)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "concurrency":
					leases = append(leases, lease)
				case "rpm":
					lease.Finish(scheduling.Outcome{})
				case "cooldown":
					lease.Finish(scheduling.Outcome{Penalty: scheduling.Light, RetryAfter: time.Minute})

				}
			}
			defer func() {
				for _, lease := range leases {
					lease.Finish(scheduling.Outcome{})
				}
			}()
			_, err = f.execution.Executor.Execute(context.Background(), req, io.Discard)
			if !errors.Is(err, scheduling.ErrNoCandidates) {
				t.Fatalf("准入未拒绝: %v", err)
			}
			page, err := history.Query(context.Background(), records.Query{})
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("记录缺失: %v", err)
			}
			detail, err := history.Get(context.Background(), page.Records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"concurrency": "并发", "rpm": "RPM", "cooldown": "冷却"}[kind]
			if len(detail.Candidates) != 2 {
				t.Fatal("候选丢失")
			}
			for _, c := range detail.Candidates {
				if !strings.Contains(c.Reason, expected) {
					t.Fatalf("跳过原因错误: %s", c.Reason)
				}
			}
		})
	}
}

// TestCodexRealToolRoundTrip 由严格上游先返回工具调用，再接收原 ID 的工具结果与独立通知，确认归一化未破坏真实往返。
func TestCodexRealToolRoundTrip(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["previous_response_id"] == nil {
			_, _ = io.WriteString(w, `{"id":"resp-tool","status":"completed","output":[{"type":"function_call","call_id":"call-real","name":"shell","arguments":"{}"}]}`)
			return
		}
		input, _ := body["input"].([]any)
		valid := body["previous_response_id"] == "resp-tool" && len(input) == 2
		if valid {
			result, _ := input[0].(map[string]any)
			notification, _ := input[1].(map[string]any)
			valid = result["call_id"] == "call-real" && result["output"] == "tool completed" && notification["role"] == "user"
		}
		if !valid {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"tool pairing mismatch"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp-final","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`)
	}
	base := startCompatibilityGateway(t, f)
	status, data := compatibilityRequest(t, base, "/v1/responses", `{"model":"logical-model","input":[{"type":"function_call_output","output":"{\"page_id\":null}"}]}`, nil)
	if status != 200 || !bytes.Contains(data, []byte("call-real")) {
		t.Fatalf("工具调用未返回: %d %s", status, data)
	}
	status, data = compatibilityRequest(t, base, "/v1/responses", `{"model":"logical-model","previous_response_id":"resp-tool","input":[{"type":"function_call_output","call_id":"call-real","output":"tool completed"},{"type":"custom_tool_call_output","output":"independent notification"}]}`, nil)
	if status != 200 || !bytes.Contains(data, []byte("done")) {
		t.Fatalf("工具结果未续接: %d %s", status, data)
	}
}

// TestCircuitDiagnosticFailureKeepsCommit 注入熔断关联保存失败，仍保留已提交部分输出的事实，不向 SSE 追加另一份错误响应或切备用。
func TestCircuitDiagnosticFailureKeepsCommit(t *testing.T) {
	f := newExecutionFixture(t)
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	candidates, err := f.execution.Executor.Source.Candidates(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		lease, err := f.execution.Executor.Runtime.Acquire(candidates[0])
		if err != nil {
			t.Fatal(err)
		}
		lease.Finish(scheduling.Outcome{Penalty: scheduling.Heavy})
	}
	_, err = f.catalog.DB().Exec(`CREATE TRIGGER fail_circuit BEFORE INSERT ON request_circuits BEGIN SELECT RAISE(FAIL,'injected circuit save failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	f.stream = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	req := f.request()
	req.Stream = true
	var out bytes.Buffer
	result, err := f.execution.Executor.Execute(context.Background(), req, &out)
	if err == nil || !result.Result.Committed || result.Attempts != 1 || out.String() != f.stream {
		t.Fatalf("诊断失败改写执行结果: %+v %v", result, err)
	}
}
