package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/verification/fingerprint"
)

// TestSamplingProtocols 通过真实 HTTP 请求验证四协议路径、认证、提示词及输出解析。
func TestSamplingProtocols(t *testing.T) {
	text := strings.Repeat("17, ", 165)
	for _, protocol := range []string{"chat_completions", "responses", "messages", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := map[string]string{"chat_completions": "/v1/chat/completions", "responses": "/v1/responses", "messages": "/v1/messages", "gemini": "/v1beta/models/test:streamGenerateContent"}[protocol]
				if r.URL.Path != path || r.Method != http.MethodPost {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				auth := "Authorization"
				wantAuth := "Bearer test-secret"
				if protocol == "messages" {
					auth, wantAuth = "x-api-key", "test-secret"
				}
				if protocol == "gemini" {
					auth, wantAuth = "x-goog-api-key", "test-secret"
					if r.URL.RawQuery != "alt=sse" {
						t.Errorf("query = %s", r.URL.RawQuery)
					}
				}
				if r.Header.Get(auth) != wantAuth {
					t.Error("incorrect authentication")
				}
				if protocol == "messages" && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Error("missing anthropic version")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				for _, key := range []string{"reasoning", "reasoning_effort", "thinking", "output_config", "generationConfig"} {
					if _, ok := body[key]; ok {
						t.Errorf("auto unexpectedly sets %s", key)
					}
				}
				if protocol == "gemini" {
					contents := body["contents"].([]any)
					got := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
					if got != " unchanged\nprompt " {
						t.Errorf("prompt = %q", got)
					}
				} else {
					if body["model"] != "test" || body["stream"] != true {
						t.Errorf("request body = %v", body)
					}
					key := "messages"
					if protocol == "responses" {
						key = "input"
					}
					got := body[key].([]any)[0].(map[string]any)["content"]
					if got != " unchanged\nprompt " {
						t.Errorf("prompt = %q", got)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "chat_completions":
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}})
				case "responses":
					json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}})
				case "messages":
					json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": text}}})
				case "gemini":
					fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]},"finishReason":"STOP"}]}`, text)
				}
			}))
			defer server.Close()
			sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol, Effort: "auto"}, URL: server.URL, RequestModel: "test"}, "test-secret", fingerprint.Challenge{ExpectedCount: 300, Prompt: " unchanged\nprompt "})
			if sample.State != "complete" || sample.Count != 165 || sample.Text != text {
				t.Fatalf("sample = %+v", sample)
			}
		})
	}
}

// TestSamplingGeminiSSE 验证多行事件、跨事件数字拼接、首候选选择、推理过滤与模型路径转义。
func TestSamplingGeminiSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1beta/models/vendor%2Fmodel%20x:streamGenerateContent" {
			t.Errorf("path = %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\ndata: {\ndata: \"candidates\":[{\"content\":{\"parts\":[{\"text\":\"99\",\"thought\":true},{\"text\":\"1\"}]}},{\"content\":{\"parts\":[{\"text\":\"888\"}]}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]},\"finishReason\":\"STOP\"}]}\n\n", "7, "+strings.Repeat("17, ", 164))
	}))
	defer server.Close()
	sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: "gemini"}, URL: server.URL, RequestModel: "vendor/model x"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
	if sample.State != "complete" || sample.Count != 165 || sample.Text != strings.Repeat("17, ", 165) || sample.FirstByteMs == nil {
		t.Fatalf("sample = %+v", sample)
	}
}

// TestSamplingGeminiThinking 验证官方两代思考参数互斥，2.5 的预算映射与 auto 省略。
func TestSamplingGeminiThinking(t *testing.T) {
	for _, tc := range []struct {
		model, effort, key string
		value              any
	}{
		{"gemini-2.5-pro", "low", "thinkingBudget", float64(1024)},
		{"gemini-2.5-flash", "medium", "thinkingBudget", float64(8192)},
		{"gemini-2.5-flash-lite", "high", "thinkingBudget", float64(24576)},
		{"gemini-3.1-pro", "low", "thinkingLevel", "low"},
		{"gemini-3-flash", "medium", "thinkingLevel", "medium"},
		{"gemini-3-flash", "high", "thinkingLevel", "high"},
	} {
		t.Run(tc.model+tc.effort, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				thinking := body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
				if len(thinking) != 1 || thinking[tc.key] != tc.value {
					t.Errorf("thinking = %v", thinking)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]},"finishReason":"STOP"}]}`, strings.Repeat("17, ", 80))
			}))
			defer server.Close()
			sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: "gemini", Effort: tc.effort}, URL: server.URL, RequestModel: tc.model}, "secret", fingerprint.Challenge{ExpectedCount: 100})
			if sample.State != "complete" {
				t.Fatalf("sample = %+v", sample)
			}
		})
	}
}

// TestSamplingSSEProtocols 验证前三协议真实 SSE 事件由内置上游解析，不把推理片段计入输出。
func TestSamplingSSEProtocols(t *testing.T) {
	text := strings.Repeat("17, ", 165)
	for _, protocol := range []string{"chat_completions", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case "chat_completions":
					fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", text)
				case "responses":
					fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", text)
				case "messages":
					fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", text)
				}
			}))
			defer server.Close()
			sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
			if sample.State != "complete" || sample.Count != 165 {
				t.Fatalf("sample = %+v", sample)
			}
		})
	}
}

// TestSamplingErrors 验证状态码重试边界、单次尝试及错误响应中的凭据脱敏。
func TestSamplingErrors(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "messages", "gemini"} {
		for _, code := range []int{400, 401, 403, 408, 429, 500, 503} {
			t.Run(fmt.Sprintf("%s/%d", protocol, code), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(code)
					fmt.Fprint(w, `{"error":{"message":"test-secret"}}`)
				}))
				defer server.Close()
				sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "test-secret", fingerprint.Challenge{ExpectedCount: 300})
				want := code == 408 || code == 429 || code >= 500
				if sample.State != "failed" || sampleRetryable(sample) != want || calls != 1 || sample.Error == nil || strings.Contains(*sample.Error, "test-secret") {
					t.Fatalf("sample=%+v calls=%d retry=%v", sample, calls, sampleRetryable(sample))
				}
			})
		}
	}
}

// TestSamplingCancelAndTotalDeadline 验证主动取消不重试，首字节到达后的总超时失败可重试。
func TestSamplingCancelAndTotalDeadline(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "messages", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": first byte\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			sample := requestSample(ctx, server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
			if sample.State != "failed" || !sampleRetryable(sample) {
				t.Fatalf("deadline sample = %+v", sample)
			}
			ctx2, cancel2 := context.WithCancel(context.Background())
			cancel2()
			sample = requestSample(ctx2, server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
			if sample.State != "cancelled" || sampleRetryable(sample) {
				t.Fatalf("pre-cancelled sample = %+v", sample)
			}
		})
	}
}

// TestSamplingLimitsAndMalformed 验证总响应限额与非法 JSON 不会被当成暂时错误。
func TestSamplingLimitsAndMalformed(t *testing.T) {
	for _, protocol := range []string{"responses", "gemini"} {
		for _, mode := range []string{"oversize-json", "oversize-sse", "malformed", "few", "unfinished", "stream-error"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch mode {
					case "oversize-json":
						fmt.Fprint(w, strings.Repeat(" ", sampleResponseLimit+1))
					case "oversize-sse":
						w.Header().Set("Content-Type", "text/event-stream")
						for i := 0; i < 1025; i++ {
							fmt.Fprint(w, ":"+strings.Repeat("x", 4096)+"\n\n")
						}
					case "malformed":
						fmt.Fprint(w, "{invalid")
					case "few":
						fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"1, 2"}]},"finishReason":"STOP"}],"status":"completed","output":[]}`)
					case "unfinished":
						fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"17"}]},"finishReason":"MAX_TOKENS"}]}`)
					case "stream-error":
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"error\":{\"code\":503,\"message\":\"test-secret\"}}\n\n")
					}
				}))
				defer server.Close()
				sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "test-secret", fingerprint.Challenge{ExpectedCount: 300})
				wantRetry := mode == "stream-error" && protocol == "gemini"
				if sample.State != "failed" || sampleRetryable(sample) != wantRetry || sample.Error == nil || strings.Contains(*sample.Error, "test-secret") {
					t.Fatalf("sample=%+v retry=%v", sample, sampleRetryable(sample))
				}
			})
		}
	}
}

// TestSamplingNetworkAndRedaction 验证连接故障可重试，成功回答和元数据中的凭据也不会保留。
func TestSamplingNetworkAndRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":"test-secret","id":"test-secret","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, "test-secret "+strings.Repeat("17, ", 165))
	}))
	target := VerificationTarget{VerificationLine: VerificationLine{Protocol: "responses"}, URL: server.URL, RequestModel: "test"}
	sample := requestSample(context.Background(), server.Client(), target, "test-secret", fingerprint.Challenge{ExpectedCount: 300})
	encoded, _ := json.Marshal(sample)
	if sample.State != "complete" || strings.Contains(string(encoded), "test-secret") {
		t.Fatalf("sample=%s", encoded)
	}
	server.Close()
	for _, protocol := range []string{"responses", "gemini"} {
		target.Protocol = protocol
		sample = requestSample(context.Background(), server.Client(), target, "test-secret", fingerprint.Challenge{ExpectedCount: 300})
		if sample.State != "failed" || !sampleRetryable(sample) {
			t.Fatalf("sample=%+v", sample)
		}
	}
}

// TestSamplingNumberBoundaries 验证上游 55% 与 80 下限、最长有效数字段以及超量截断行为。
func TestSamplingNumberBoundaries(t *testing.T) {
	for _, tc := range []struct {
		expected, count, wantCount int
		state                      string
	}{
		{300, 164, 164, "failed"}, {300, 165, 165, "complete"}, {100, 79, 79, "failed"}, {100, 80, 80, "complete"}, {100, 101, 100, "truncated"},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.expected, tc.count), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]},"finishReason":"STOP"}]}`, "1,2 explanation "+strings.Repeat("17, ", tc.count)+"9999")
			}))
			defer server.Close()
			sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: "gemini"}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: tc.expected})
			if sample.State != tc.state || sample.Count != tc.wantCount || sampleRetryable(sample) {
				t.Fatalf("sample=%+v", sample)
			}
		})
	}
}

// TestSamplingProtocolEffort 验证前三协议使用上游各自的思考字段而非本地重新拼装请求。
func TestSamplingProtocolEffort(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				switch protocol {
				case "chat_completions":
					if body["reasoning_effort"] != "high" {
						t.Errorf("body=%v", body)
					}
				case "responses":
					if body["reasoning"].(map[string]any)["effort"] != "high" {
						t.Errorf("body=%v", body)
					}
				case "messages":
					if body["thinking"].(map[string]any)["type"] != "adaptive" || body["output_config"].(map[string]any)["effort"] != "high" {
						t.Errorf("body=%v", body)
					}
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()
			requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol, Effort: "high"}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
		})
	}
}

// TestSamplingBrokenBody 验证 JSON 响应读取中断在上游转为解析错误后仍可被识别为网络错误。
func TestSamplingBrokenBody(t *testing.T) {
	for _, protocol := range []string{"responses", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", "1000")
				fmt.Fprint(w, "{")
			}))
			defer server.Close()
			sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
			if sample.State != "failed" || !sampleRetryable(sample) {
				t.Fatalf("sample=%+v", sample)
			}
		})
	}
}

// TestSamplingActiveCancellation 验证请求进行中主动取消不会作为暂时失败重试。
func TestSamplingActiveCancellation(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses", "messages", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": first byte\n\n")
				w.(http.Flusher).Flush()
				cancel()
				<-r.Context().Done()
			}))
			defer server.Close()
			sample := requestSample(ctx, server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: protocol}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
			if sample.State != "cancelled" || sampleRetryable(sample) {
				t.Fatalf("sample=%+v", sample)
			}
		})
	}
}

// TestSamplingGeminiCompletion 验证数字足够也必须有首候选 STOP 终态，异常终态、断流和推理文本不能冒充有效回答。
func TestSamplingGeminiCompletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, finish := range []string{"", "STOP", "MAX_TOKENS", "SAFETY"} {
			for _, thought := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v/finish=%s/thought=%v", stream, finish, thought), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%q,"thought":%v}]},"finishReason":%q},{"content":{"parts":[{"text":%q}]},"finishReason":"STOP"}]}`, strings.Repeat("17, ", 165), thought, finish, strings.Repeat("17, ", 165))
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: %s\n\n", body)
						} else {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, body)
						}
					}))
					defer server.Close()
					sample := requestSample(context.Background(), server.Client(), VerificationTarget{VerificationLine: VerificationLine{Protocol: "gemini"}, URL: server.URL, RequestModel: "test"}, "secret", fingerprint.Challenge{ExpectedCount: 300})
					want := "failed"
					if finish == "STOP" && !thought {
						want = "complete"
					}
					if sample.State != want || sampleRetryable(sample) {
						t.Fatalf("sample=%+v", sample)
					}
				})
			}
		}
	}
}
