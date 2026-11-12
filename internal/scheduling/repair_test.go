package scheduling

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// TestRepairChatAudioAggregate 验证真实 SSE 的逐段音频先解码合并，padding、转写及扩展字段不会丢失。
func TestRepairChatAudioAggregate(t *testing.T) {
	for _, tc := range []struct {
		// name 区分 padding 形状；first、second 是独立 base64 段，want 是合并后的原始音频。
		name, first, second string
		want                []byte
	}{
		{"without-padding", "AQID", "BAUG", []byte{1, 2, 3, 4, 5, 6}},
		{"with-padding", "AQ==", "AgM=", []byte{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"audio\":{\"id\":\"audio-1\",\"data\":\"" + tc.first + "\",\"transcript\":\"hello \",\"future\":true}}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"data\":\"" + tc.second + "\",\"transcript\":\"world\",\"expires_at\":123}},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{"stream":false}`)}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, events)
			})
			var response struct {
				// Choices 保留实际同步响应，Audio 只解码本次验收关心的字段。
				Choices []struct {
					Message struct {
						Audio struct {
							Data, Transcript, ID string
							Future               bool
							ExpiresAt            int `json:"expires_at"`
						}
					}
				}
			}
			if err != nil || !result.Outcome.Success || json.Unmarshal(out, &response) != nil || len(response.Choices) != 1 {
				t.Fatalf("同步聚合失败: result=%+v err=%v", result, err)
			}
			audio := response.Choices[0].Message.Audio
			decoded, err := base64.StdEncoding.DecodeString(audio.Data)
			if err != nil || !bytes.Equal(decoded, tc.want) || audio.Data != base64.StdEncoding.EncodeToString(tc.want) || audio.Transcript != "hello world" || audio.ID != "audio-1" || !audio.Future || audio.ExpiresAt != 123 {
				t.Fatalf("音频聚合丢失数据: audio=%+v decodeErr=%v", audio, err)
			}
		})
	}
}

// TestRepairChatAudioInvalidBase64 验证非法音频段不能生成被截断却标记成功的同步响应。
func TestRepairChatAudioInvalidBase64(t *testing.T) {
	result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{"stream":false}`)}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"id\":\"audio-1\",\"data\":\"invalid!\"}},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	if err == nil || result.Outcome.Success || result.Committed || len(out) != 0 {
		t.Fatalf("非法音频被作为完整响应提交: result=%+v err=%v", result, err)
	}
}

// runRepairHTTP 通过真实本地 HTTP 运行同格式适配器，保留认证、请求兼容及正文读取链路。
func runRepairHTTP(t *testing.T, req ExecutionRequest, handler http.HandlerFunc) (AttemptResult, []byte, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	runner := NewHTTPRunner()
	defer runner.Close()
	c := runtimeCandidate("a", 0, 0, nil)
	c.Provider.URL = server.URL
	c.Model = "mapped-model"
	var out bytes.Buffer
	result, err := runner.Run(context.Background(), c, req, "upstream-secret", &out)
	return result, out.Bytes(), err
}

// TestRepairToolTerminals 验证合法的纯工具及多模态终态不会当作空回答，也不会丢失原始输出。
func TestRepairToolTerminals(t *testing.T) {
	for _, tc := range []struct {
		// protocol 和 response 是真实上游返回的合法工具终态。
		protocol, response string
	}{
		{"openai:responses", `{"id":"resp-tool","status":"completed","output":[{"type":"computer_call","id":"cu_1","call_id":"c1","action":{"type":"screenshot"}}]}`},
		{"openai:responses", `{"status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"weather"}}]}`},
		{"openai:responses", `{"status":"completed","output":[{"type":"custom_tool_call","name":"check","input":""}]}`},
		{"claude:messages", `{"type":"message","stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}}]}`},
		{"gemini:generate_content", `{"candidates":[{"content":{"parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]},"finishReason":"STOP"}]}`},
	} {
		t.Run(tc.protocol+tc.response[:12], func(t *testing.T) {
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: "/v1/responses", Body: []byte(`{}`)}, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.response) })
			if err != nil || !result.Outcome.Success || string(out) != tc.response {
				t.Fatalf("result=%+v err=%v output=%s", result, err, out)
			}
		})
	}
}

// TestRepairGeminiRequest 验证 mixed tools、函数结果 ID 清理和大整数均在真实出站正文中符合旧版契约。
func TestRepairGeminiRequest(t *testing.T) {
	body := `{"tools":[{"googleSearch":{}},{"functionDeclarations":[{"name":"weather"}]}],"toolConfig":{"include_server_side_tool_invocations":false,"functionCallingConfig":{"mode":"AUTO"}},"contents":[{"parts":[{"functionResponse":{"id":"c1","name":"weather","response":{"n":9007199254740993}}}]}]}`
	_, _, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "gemini:generate_content"}, Path: "/v1/models/logical:generateContent", Body: []byte(body)}, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/models/mapped-model:generateContent" || r.Header.Get("X-Goog-Api-Key") != "upstream-secret" {
			t.Errorf("path/auth=%s %v", r.URL.Path, r.Header)
		}
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		config, _ := got["toolConfig"].(map[string]any)
		if config["includeServerSideToolInvocations"] != true || config["include_server_side_tool_invocations"] != nil || !bytes.Contains(raw, []byte("9007199254740993")) || bytes.Contains(raw, []byte(`"id":"c1"`)) {
			t.Errorf("body=%s", raw)
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRepairReasoningReplay 通过真实 HTTP 核对 rs 引用、合成摘要、DeepSeek 不透明状态及 compact 例外。
func TestRepairReasoningReplay(t *testing.T) {
	body := `{"input":[{"type":"reasoning","summary":[]},{"type":"reasoning","id":"item_foreign"},{"type":"reasoning","id":"rs_aether_1"},{"type":"reasoning","id":"rs_real"},{"type":"reasoning","id":"rs_aether_2","encrypted_content":"state"},{"type":"reasoning","content":[{"type":"reasoning_text","text":""}],"encrypted_content":"opaque"},{"type":"message","role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		// host/path/protocol 决定策略；want 为最终上游 input 数量。
		host, path, protocol string
		want                 int
	}{{"localhost", "/v1/responses", "openai:responses", 3}, {"api.deepseek.com", "/v1/responses", "openai:responses", 4}, {"api.deepseek.com", "/v1/responses/compact", "openai:responses:compact", 3}, {"deepseek.private.test", "/v1/responses", "openai:responses", 3}} {
		t.Run(tc.host+tc.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got struct {
					Input []json.RawMessage `json:"input"`
				}
				_ = json.NewDecoder(r.Body).Decode(&got)
				if len(got.Input) != tc.want {
					t.Errorf("input=%s", got.Input)
				}
				if tc.protocol == "openai:responses:compact" {
					_, _ = io.WriteString(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"state"}]}`)
				} else {
					_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":"ok"}]}`)
				}
			}))
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}}
			defer transport.CloseIdleConnections()
			runner := &HTTPRunner{Client: &http.Client{Transport: transport}}
			c := runtimeCandidate("a", 0, 0, nil)
			c.Provider.URL = "http://" + tc.host
			result, err := runner.Run(context.Background(), c, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: tc.path, Body: []byte(body)}, "secret", io.Discard)
			if err != nil || !result.Outcome.Success {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

// TestRepairSpecialTerminals 通过真实 HTTP 验证计数及压缩的非生成成功终态、认证和 Gemini 路径映射。
func TestRepairSpecialTerminals(t *testing.T) {
	for _, tc := range []struct {
		// protocol、path、response、auth 是标准附属操作的 wire 契约。
		protocol, path, response, auth string
	}{
		{"openai:responses:compact", "/v1/responses/compact", `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"state"}],"usage":{"input_tokens":20,"output_tokens":3}}`, "Authorization"},
		{"claude:count_tokens", "/v1/messages/count_tokens", `{"input_tokens":0}`, "X-Api-Key"},
		{"gemini:count_tokens", "/v1/models/logical:countTokens", `{"totalTokens":0,"cachedContentTokenCount":0}`, "X-Goog-Api-Key"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: tc.path, Body: []byte(`{}`)}, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(tc.auth) == "" {
					t.Error("缺少原生认证")
				}
				if tc.protocol == "gemini:count_tokens" && r.URL.Path != "/v1/models/mapped-model:countTokens" {
					t.Error(r.URL.Path)
				}
				_, _ = io.WriteString(w, tc.response)
			})
			if err != nil || !result.Outcome.Success || string(out) != tc.response {
				t.Fatalf("%+v %v %s", result, err, out)
			}
		})
	}
}

// TestRepairHTTPFailures 验证错误正文读断仍按已知 HTTP 状态分类，并保留 Retry-After；SSE 限流采用轻故障。
func TestRepairHTTPFailures(t *testing.T) {
	result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`)}, func(w http.ResponseWriter, r *http.Request) {
		conn, rw, e := w.(http.Hijacker).Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 503 Service Unavailable\r\nContent-Length: 100\r\nRetry-After: 30\r\nConnection: close\r\n\r\n{}")
		_ = rw.Flush()
		_ = conn.Close()
	})
	if err == nil || len(out) != 0 || result.Outcome.Penalty != Light || !result.Retryable || result.Outcome.RetryAfter.Seconds() != 30 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, protocol := range []string{"openai:chat", "openai:responses", "claude:messages", "gemini:generate_content"} {
		t.Run(protocol, func(t *testing.T) {
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: protocol}, Path: "/v1/chat/completions", Body: []byte(`{}`), Stream: true}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Retry-After", "15")
				_, _ = io.WriteString(w, "data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"busy\"}}\n\n")
			})
			if err == nil || len(out) != 0 || result.Committed || !result.Retryable || result.Outcome.Penalty != Light || !result.Outcome.RateLimited || result.Outcome.RetryAfter.Seconds() != 15 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

// TestRepairBusinessHeaders 核对多值业务头转发以及凭据、Cookie、逐跳头和原压缩状态的过滤。
func TestRepairBusinessHeaders(t *testing.T) {
	headers := http.Header{"Authorization": {"Bearer client"}, "Cookie": {"session=secret"}, "X-Api-Key": {"client"}, "X-Goog-Api-Key": {"client"}, "Content-Encoding": {"gzip"}, "Connection": {"x-hop"}, "X-Hop": {"secret"}, "Openai-Organization": {"org-a"}, "Openai-Project": {"project-a"}, "Idempotency-Key": {"idem-a"}, "Openai-Beta": {"feature-a", "feature-b"}}
	_, _, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`), Headers: headers}, func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Cookie", "X-Api-Key", "X-Goog-Api-Key", "Content-Encoding", "X-Hop"} {
			if r.Header.Get(name) != "" {
				t.Errorf("泄漏头 %s", name)
			}
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("OpenAI-Organization") != "org-a" || r.Header.Get("OpenAI-Project") != "project-a" || r.Header.Get("Idempotency-Key") != "idem-a" || len(r.Header.Values("OpenAI-Beta")) != 2 {
			t.Errorf("业务头=%v", r.Header)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRepairFinalizeEarlyReturn 验证提前退出也只回调一次，错误经采集出口写入后再完成记录。
func TestRepairFinalizeEarlyReturn(t *testing.T) {
	catalog, err := models.Open(filepath.Join(t.TempDir(), "records.sqlite"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	db := catalog.DB()
	if _, err := providers.New(db, nil); err != nil {
		t.Fatal(err)
	}
	history, err := records.New(db)
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{Records: history}
	var out bytes.Buffer
	calls := 0
	_, err = executor.Execute(context.Background(), ExecutionRequest{Request: Request{Model: "logical", Protocol: "openai:chat"}, Type: "chat", Body: []byte(`{}`), Finalize: func(w io.Writer, result ExecutionResult, cause error) {
		calls++
		if cause == nil {
			t.Error("缺少失败原因")
		}
		_, _ = io.WriteString(w, `{"error":"final client error"}`)
	}}, &out)
	if err == nil || calls != 1 || out.String() != `{"error":"final client error"}` {
		t.Fatalf("calls=%d err=%v out=%s", calls, err, out.String())
	}
	page, err := history.Query(context.Background(), records.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].State != "local" || page.Records[0].Type != "chat" {
		t.Fatalf("records=%+v", page.Records)
	}
	detail, err := history.Get(context.Background(), page.Records[0].ID)
	payload := detail.Client
	if err != nil || payload == nil || !strings.Contains(payload.ResponseBody, "final client error") {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
}

// TestRepairBackgroundNoOutbound 确认 background 在 HTTP 出站前明确拒绝，避免异步任务被调度重建。
func TestRepairBackgroundNoOutbound(t *testing.T) {
	calls := 0
	result, _, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{"background":true}`)}, func(w http.ResponseWriter, r *http.Request) { calls++ })
	if !errors.Is(err, ErrUnsupportedBackground) || calls != 0 || result.Retryable || result.Status != 400 {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
}

// TestRepairSyncSSE 通过真实 HTTP 核对四种协议聚合后的完整工具、推理、扩展字段和用量。
func TestRepairSyncSSE(t *testing.T) {
	for _, tc := range []struct {
		// name/protocol/events 指定真实 SSE；contains 验收聚合结果中不可丢失的结构与值。
		name, protocol, events string
		contains               []string
	}{
		{"chat", "openai:chat", "data: {\"id\":\"chat-1\",\"model\":\"m\",\"system_fingerprint\":\"fp-a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"he\",\"reasoning_content\":\"think\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"n\\\":\"}}]}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"9007199254740993}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4,\"total_tokens\":16}}\n\ndata: [DONE]\n\n", []string{`"content":"hello"`, `"reasoning_content":"think"`, `"id":"call-1"`, `"arguments":"{\"n\":9007199254740993}"`, `"total_tokens":16`, `"system_fingerprint":"fp-a"`, `"object":"chat.completion"`}},
		{"responses", "openai:responses", "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"computer_call\",\"call_id\":\"c1\",\"action\":{\"type\":\"screenshot\"}}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"computer_call\",\"call_id\":\"c1\",\"action\":{\"type\":\"screenshot\"}}],\"usage\":{\"input_tokens\":12,\"output_tokens\":4},\"metadata\":{\"custom\":true}}}\n\n", []string{`"type":"computer_call"`, `"call_id":"c1"`, `"input_tokens":12`, `"custom":true`, `"id":"resp-1"`}},
		{"anthropic", "claude:messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":12}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srv-1\",\"name\":\"weather\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"n\\\":9007199254740993}\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"pause_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", []string{`"type":"server_tool_use"`, `"n":9007199254740993`, `"input_tokens":12`, `"output_tokens":4`, `"stop_reason":"pause_turn"`}},
		{"gemini", "gemini:generate_content", "data: {\"responseId\":\"gem-1\",\"modelVersion\":\"gemini-m\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\",\"thought\":true}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"c1\",\"name\":\"weather\",\"args\":{\"n\":9007199254740993}},\"thoughtSignature\":\"opaque\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":4}}\n\n", []string{`"functionCall":{"args":{"n":9007199254740993}`, `"thoughtSignature":"opaque"`, `"thought":true`, `"promptTokenCount":12`, `"candidatesTokenCount":4`, `"responseId":"gem-1"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "gemini" {
				tc.events += "data: {\"usageMetadata\":{\"toolUsePromptTokenCount\":7}}\n\n"
				tc.contains = append(tc.contains, `"toolUsePromptTokenCount":7`)
			}
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: "/v1/responses", Body: []byte(`{"stream":false}`)}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.events)
			})
			if err != nil || !result.Outcome.Success || !json.Valid(out) {
				t.Fatalf("result=%+v err=%v out=%s", result, err, out)
			}
			for _, value := range tc.contains {
				if !bytes.Contains(out, []byte(value)) {
					t.Errorf("missing %s in %s", value, out)
				}
			}
		})
	}
}

// TestRepairLongPayloads 验证超过旧 4 MiB 单事件、32 MiB 同步响应的合法上下文保持完整传输。
func TestRepairLongPayloads(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "sse-event", false: "sync-response"}[stream], func(t *testing.T) {
			size := 33 * 1024 * 1024
			if stream {
				size = 5 * 1024 * 1024
			}
			response := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + strings.Repeat("x", size) + `"}]}]}`
			wire := response
			if stream {
				wire = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
			}
			result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{}`), Stream: stream}, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, wire)
			})
			if err != nil || !result.Outcome.Success || len(out) != len(wire) {
				t.Fatalf("size=%d got=%d result=%+v err=%v", len(wire), len(out), result, err)
			}
		})
	}
}

// TestRepairSyncSSEIndexedAndSnapshots 验证多 choice/工具索引与仅 done 快照事件，不让聚合丢项或重复拼接。
func TestRepairSyncSSEIndexedAndSnapshots(t *testing.T) {
	for _, tc := range []struct {
		// protocol/events 是需要保留索引或快照的真实事件，contains/absent 是交付 JSON 的具体边界。
		protocol, events string
		contains, absent []string
	}{
		{"openai:chat", "data: {\"choices\":[{\"index\":1,\"delta\":{\"role\":\"assistant\",\"content\":\"second\"}},{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call-2\",\"type\":\"function\",\"function\":{\"name\":\"second\",\"arguments\":\"{}\"}},{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"first\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", []string{`"choices":[{"finish_reason":"tool_calls","index":0`, `"tool_calls":[{"function":{"arguments":"{}","name":"first"},"id":"call-1"`, `"index":1`, `"content":"second"`, `"id":"call-2"`}, nil},
		{"openai:responses", "event: response.output_text.done\ndata: {\"output_index\":0,\"content_index\":0,\"text\":\"snapshot\"}\n\nevent: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":8}}}\n\n", []string{`"text":"snapshot"`, `"output_tokens":8`}, nil},
		{"openai:responses", "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"c1\",\"name\":\"weather\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{}\"}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":\"{}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", []string{`"arguments":"{}"`, `"call_id":"c1"`}, []string{`"arguments":"{}{}"`}},
	} {
		result, out, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: tc.protocol}, Path: "/v1/responses", Body: []byte(`{}`)}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, tc.events)
		})
		if err != nil || !result.Outcome.Success {
			t.Fatalf("result=%+v err=%v out=%s", result, err, out)
		}
		for _, value := range tc.contains {
			if !bytes.Contains(out, []byte(value)) {
				t.Errorf("missing %s in %s", value, out)
			}
		}
		for _, value := range tc.absent {
			if bytes.Contains(out, []byte(value)) {
				t.Errorf("unexpected %s in %s", value, out)
			}
		}
	}
}

// TestRepairResponseCommit 验证 JSON、SSE、同步聚合在真实写出前仅提交一次正确响应头，协议失败不提前提交。
func TestRepairResponseCommit(t *testing.T) {
	for _, tc := range []struct {
		// name/type/body 指定上游响应；stream 是客户端模式，wantCalls 为真正提交次数。
		name, contentType, body string
		stream                  bool
		wantCalls               int
	}{
		{"json", "application/json", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, false, 1},
		{"sse", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, 1},
		{"sync-sse", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false, 1},
		{"invalid", "application/json", `{"choices":[]}`, false, 0},
		{"sse-error", "text/event-stream", "data: {\"error\":{\"type\":\"rate_limit_error\"}}\n\n", true, 0},
		{"sync-sse-error", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, _, err := runRepairHTTP(t, ExecutionRequest{Request: Request{Protocol: "openai:chat"}, Path: "/v1/chat/completions", Body: []byte(`{}`), Stream: tc.stream, Response: func(status int, headers http.Header) {
				calls++
				wantType := "application/json"
				if tc.stream {
					wantType = "text/event-stream"
				}
				if status != 201 || headers.Get("Content-Type") != wantType || headers.Get("X-Request-ID") != "upstream-1" {
					t.Errorf("status=%d headers=%v", status, headers)
				}
			}}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-ID", "upstream-1")
				w.WriteHeader(201)
				_, _ = io.WriteString(w, tc.body)
			})
			if calls != tc.wantCalls || (err == nil) != (tc.wantCalls == 1) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
