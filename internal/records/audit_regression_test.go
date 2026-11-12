package records

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLargeResponsesSSETerminalUsage 验证超过 2 MiB 的完整 Responses 终态事件用量落库，正文快照仍限制为 1 MiB。
func TestLargeResponsesSSETerminalUsage(t *testing.T) {
	s, _ := fixture(t)
	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + strings.Repeat("a", 2<<20) + "\"}]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":30}}}}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	defer upstream.Close()
	r, _ := exchange(t, s, upstream.URL, "openai:responses", true)
	if r.Usage == nil || r.Usage.Context != 100 || r.Usage.Input != 70 || r.Usage.Output != 20 || r.Usage.CacheRead != 30 || r.Attempts[0].Usage == nil {
		t.Fatalf("大终态事件的实际用量未完整落库: %+v", r.Usage)
	}
	payload := r.Attempts[0].Payload
	if payload == nil || payload.BodyState != "truncated" || len(payload.ResponseBody) > captureLimit {
		t.Fatal("大终态事件扩大了正文快照边界")
	}
}

// TestCanonicalMediaUsage 验证规范协议名与旧别名在真实传输、持久化后的媒体数量和完整用量一致。
func TestCanonicalMediaUsage(t *testing.T) {
	for _, tc := range []struct {
		// protocol 与 body 是实际上游协议及响应；unit、quantity、input 为明确预期。
		protocol, body, unit string
		quantity             float64
		input                int64
	}{
		{"openai:image", `{"data":[{"url":"https://example.test/a.png"},{"url":"https://example.test/b.png"}]}`, "image", 2, 0},
		{"openai:video", `{"status":"completed","seconds":"8","size":"1280x720"}`, "second", 8, 0},
		{"openai:embedding", `{"data":[],"usage":{"prompt_tokens":100,"total_tokens":100}}`, "", 0, 100},
		{"openai:rerank", `{"results":[],"usage":{"prompt_tokens":100,"total_tokens":100}}`, "", 0, 100},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			s, _ := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			r, _ := exchange(t, s, upstream.URL, tc.protocol, false)
			if r.Usage == nil || r.Usage.Unit != tc.unit || r.Usage.Quantity != tc.quantity || r.Usage.Input != tc.input {
				t.Fatalf("规范协议用量丢失: %+v", r.Usage)
			}
		})
	}
}

// TestCanonicalVideoPending 验证 canonical 视频协议的异步任务保留待计费状态，不伪造零用量。
func TestCanonicalVideoPending(t *testing.T) {
	for _, state := range []string{"queued", "in_progress"} {
		t.Run(state, func(t *testing.T) {
			s, _ := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"status":"`+state+`"}`)
			}))
			defer upstream.Close()
			r, _ := exchange(t, s, upstream.URL, "openai:video", false)
			if !r.Attempts[0].BillingPending || r.Attempts[0].Usage != nil || r.Usage != nil || r.UnknownCosts != 0 {
				t.Fatalf("待完成视频被当作已计费: %+v", r)
			}
		})
	}
}

// TestInteractionsUsage 验证官方 Interactions 用量字段在原生 JSON、资源信封和分片 SSE 中按 Gemini 口径落库。
func TestInteractionsUsage(t *testing.T) {
	usage := `{"total_input_tokens":100,"total_cached_tokens":30,"total_output_tokens":20,"total_thought_tokens":5}`
	for _, tc := range []struct {
		// name 与 body 区分真实响应形状；stream 控制原生事件流读取。
		name, body string
		stream     bool
	}{
		{"root", `{"usage":` + usage + `}`, false},
		{"interaction", `{"interaction":{"id":"i1","usage":` + usage + `}}`, false},
		{"sse", "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"usage\":{\"total_input_tokens\":100,\"total_cached_tokens\":30}}}\n\nevent: interaction.status_update\ndata: {\"event_type\":\"interaction.status_update\",\"usage\":{\"total_output_tokens\":20,\"total_thought_tokens\":5}}\n\nevent: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"usage\":" + usage + "}}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contentType := "application/json"
				if tc.stream {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			r, _ := exchange(t, s, upstream.URL, "gemini:interactions", tc.stream)
			if r.Usage == nil || r.Usage.Input != 70 || r.Usage.Context != 100 || r.Usage.CacheRead != 30 || r.Usage.Output != 25 {
				t.Fatalf("Interactions 用量未正确合并落库: %+v", r.Usage)
			}
		})
	}
}

// TestOpenAICacheWritePriority 验证两类 details 的旧字段、显式优先级和零值，普通输入不重复包含缓存。
func TestOpenAICacheWritePriority(t *testing.T) {
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		for _, fields := range []string{
			`"cached_creation_tokens":40`,
			`"cache_write_tokens":40`,
			`"cached_creation_tokens":1,"cache_write_tokens":40`,
			`"cached_creation_tokens":40,"cache_write_tokens":0`,
		} {
			t.Run(key+fields, func(t *testing.T) {
				counts, protocol := `"prompt_tokens":100,"completion_tokens":20`, "openai:chat"
				if key == "input_tokens_details" {
					counts, protocol = `"input_tokens":100,"output_tokens":20`, "openai:responses"
				}
				u := parseUsage([]byte(`{"usage":{`+counts+`,"`+key+`":{"cached_tokens":30,`+fields+`}}}`), protocol)
				wantWrite, wantInput := int64(40), int64(30)
				if strings.Contains(fields, `"cache_write_tokens":0`) {
					wantWrite, wantInput = 0, 70
				}
				if u == nil || u.CacheWrite != wantWrite || u.Input != wantInput || u.Context != 100 || u.CacheRead != 30 {
					t.Fatalf("缓存映射不符: %+v", u)
				}
			})
		}
	}
}

// TestAttemptResultScope 验证协议错误即时落库、凭据脱敏、未出站作用域不误写和 Writer 不重复采集。
func TestAttemptResultScope(t *testing.T) {
	s, _ := fixture(t)
	ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:chat"})
	if err != nil {
		t.Fatal(err)
	}
	if !HasSession(ctx) || HasSession(context.Background()) {
		t.Fatal("会话存在性不符")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	defer upstream.Close()
	attemptCtx := WithTarget(ctx, Target{KeyID: "first"})
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, upstream.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-secret")
	client := &http.Client{Transport: &Transport{}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := AttemptResult(attemptCtx, errors.New("协议失败 test-secret")); err != nil {
		t.Fatal(err)
	}
	run := ctx.Value(sessionKey{}).(*session)
	active, err := s.Get(ctx, run.record.ID)
	if err != nil || active.Attempts[0].Status != 200 || active.Attempts[0].Error != "协议失败 [REDACTED]" {
		t.Fatalf("执行结果未即时脱敏落库: %+v %v", active, err)
	}
	if err := AttemptResult(WithTarget(ctx, Target{KeyID: "not-sent"}), errors.New("未发出")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := Writer(ctx, &output)
	io.WriteString(writer, `{"error":`)
	io.WriteString(Writer(ctx, writer), `"final"}`)
	if err := finish(errors.New("最终失败")); err != nil {
		t.Fatal(err)
	}
	final, err := s.Get(ctx, run.record.ID)
	if err != nil || len(final.Attempts) != 1 || final.Attempts[0].Error != "协议失败 [REDACTED]" || final.Client.ResponseBody != output.String() {
		t.Fatalf("重入采集或未出站结果污染记录: %+v %v", final, err)
	}
}
