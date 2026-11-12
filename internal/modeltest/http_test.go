package modeltest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunHTTPProtocols 验证真实 HTTP 路径、认证、实际模型及八种协议的有效终态。
func TestRunHTTPProtocols(t *testing.T) {
	cases := []struct {
		// protocol、path、auth、response 描述各适配器的外部契约。
		protocol, path, auth, response string
	}{
		{"chat", "/v1/chat/completions", "Authorization", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`},
		{"responses", "/v1/responses", "Authorization", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`},
		{"anthropic", "/v1/messages", "X-Api-Key", `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`},
		{"gemini", "/v1beta/models/upstream:generateContent", "X-Goog-Api-Key", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}]}`},
		{"image", "/v1/images/generations", "Authorization", `{"data":[{"b64_json":"aW1hZ2U="}]}`},
		{"embedding", "/v1/embeddings", "Authorization", `{"data":[{"embedding":[0.1,0.2]}]}`},
		{"rerank", "/v1/rerank", "Authorization", `{"results":[{"index":0,"relevance_score":0.9}]}`},
		{"video", "/v1/videos", "Authorization", `{"id":"video-1","status":"completed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Method != "POST" {
					t.Errorf("unexpected route: %s %s", r.Method, r.URL.Path)
				}
				auth := "test-secret"
				if tc.auth == "Authorization" {
					auth = "Bearer " + auth
				}
				if r.Header.Get(tc.auth) != auth {
					t.Error("missing protocol authentication")
				}
				if tc.protocol == "anthropic" && r.Header.Get("Anthropic-Version") != "2023-06-01" {
					t.Error("missing anthropic version")
				}
				if tc.protocol == "video" {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
					}
					if r.FormValue("model") != "upstream" || r.FormValue("prompt") != "hello" || r.FormValue("stream") != "" {
						t.Errorf("invalid video fields: %v", r.Form)
					}
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if tc.protocol != "gemini" && body["model"] != "upstream" {
						t.Error("model not pinned")
					}
					if _, ok := body["stream"]; ok {
						t.Error("stream present")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			got := runHTTP(context.Background(), server.Client(), Request{Protocol: tc.protocol, Message: "hello"}, server.URL, "upstream", "test-secret")
			if got.Error != nil || got.Status != 200 {
				t.Fatalf("unexpected result: %+v", got)
			}
			if strings.Contains(got.RequestHeaders, "test-secret") {
				t.Error("authentication leaked")
			}
		})
	}
}

// TestRunHTTPCustomInput 验证自定义正文替代默认输入、固定模型且删除 stream。
func TestRunHTTPCustomInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/chat/completions" || r.Header.Get("X-Test") != "yes" {
			t.Error("custom route or header lost")
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if json.Unmarshal(data, &body) != nil || body["model"] != "fixed" || body["custom"] != true || len(body) != 2 {
			t.Errorf("unexpected body: %s", data)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat", RequestBody: `{"model":"wrong","stream":true,"custom":true}`, RequestHeaders: `{"X-Test":"yes"}`}, server.URL+"/gateway/v1/", "fixed", "secret")
	if got.Error != nil {
		t.Fatal(got.Error)
	}
}

// TestRunHTTPRejectInput 验证不合法输入在发送前失败，包含认证头与 1 MiB 正文上限。
func TestRunHTTPRejectInput(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, body := range []string{"null", "[]", `{} {}`, `{"bad":`, `{"x":"` + strings.Repeat("a", 1<<20) + `"}`} {
		got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat", RequestBody: body}, server.URL, "m", "s")
		if got.Error == nil {
			t.Errorf("accepted invalid body: %.30s", body)
		}
	}
	for _, headers := range []string{`null`, `[]`, `{"X-Test":null}`, `{"X-Test":1}`, `{"Bad Header":"x"}`, `{"X-Test":"a\r\nb"}`, `{"X-Test":"a\u0001b"}`, `{"X-Test":"a","x-test":"b"}`} {
		got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat", RequestHeaders: headers}, server.URL, "m", "s")
		if got.Error == nil {
			t.Errorf("accepted invalid headers: %s", headers)
		}
	}
	for _, header := range []string{"Authorization", "Host", "Cookie", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "X-Auth-Token", "Proxy-Authorization", "Connection", "Transfer-Encoding", "Content-Length", "Upgrade", "Te"} {
		raw, _ := json.Marshal(map[string]string{header: "evil"})
		got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat", RequestHeaders: string(raw)}, server.URL, "m", "s")
		if got.Error == nil {
			t.Errorf("accepted forbidden header: %s", header)
		}
	}
	for _, base := range []string{"file:///tmp/test", server.URL + "?key=secret", "http://user:pass@localhost", server.URL + "#fragment"} {
		if got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat"}, base, "m", "s"); got.Error == nil {
			t.Errorf("accepted invalid base: %s", base)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input sent to upstream")
	}
}

// TestRunHTTPInvalidResponses 验证 HTTP 成功不掩盖错误信封、截断、无效终态和响应大小上限。
func TestRunHTTPInvalidResponses(t *testing.T) {
	cases := []struct {
		// protocol、body、status 覆盖协议错误与 HTTP 错误。
		protocol, body string
		status         int
	}{
		{"chat", `{"error":{"message":"no"}}`, 200},
		{"chat", `{}`, 200},
		{"chat", `{"choices":[{"message":{"content":"ok"},"finish_reason":null}]}`, 200},
		{"chat", `{"choices":[{"message":{},"finish_reason":"stop"}]}`, 200},
		{"responses", `{"status":"in_progress","output":[{"x":1}]}`, 200},
		{"responses", `{"status":"completed","output":[]}`, 200},
		{"anthropic", `{"type":"message","stop_reason":"end_turn","content":[]}`, 200},
		{"gemini", `{"candidates":[{"finishReason":"SAFETY","content":{"parts":[{"text":"blocked"}]}}]}`, 200},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"unfinished"}]}}]}`, 200},
		{"image", `{"data":[{}]}`, 200},
		{"embedding", `{"data":[{"embedding":[]}]}`, 200},
		{"rerank", `{"results":[{"index":0}]}`, 200},
		{"video", `{"id":"v","status":"failed"}`, 200},
		{"video", `{"status":"completed"}`, 200},
		{"video", `{"id":"v","status":"unknown"}`, 200},
		{"chat", `{"error":"denied"}`, 401},
		{"chat", `not json`, 200},
		{"chat", strings.Repeat("x", (4<<20)+1), 200},
		// 无效结尾位于快照界限以外，必须验证完整响应。
		{"chat", `{"padding":"` + strings.Repeat("x", 40<<10) + `","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}garbage`, 200},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			got := runHTTP(context.Background(), server.Client(), Request{Protocol: tc.protocol}, server.URL, "model", "secret")
			if got.Error == nil || got.Status != tc.status {
				t.Fatalf("invalid response accepted: %+v", got)
			}
			if len(got.ResponseBody) > 32<<10 {
				t.Fatal("snapshot too large")
			}
		})
	}
}

// TestRunHTTPRedaction 验证完整诊断与超长快照中的明文、URL、JSON 和 Base64 凭据均脱敏。
func TestRunHTTPRedaction(t *testing.T) {
	secret := "sk-/+?<&secret"
	encoded, _ := json.Marshal(secret)
	variants := []string{secret, url.QueryEscape(secret), url.PathEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)), string(encoded[1 : len(encoded)-1])}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]any{"error": strings.Join(variants, " | "), "padding": strings.Repeat("x", 40<<10)})
		w.Write(payload)
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"custom": strings.Join(variants, " | ")})
	got := runHTTP(context.Background(), server.Client(), Request{Protocol: "chat", RequestBody: string(raw)}, server.URL, "model", secret)
	if got.Error == nil {
		t.Fatal("error response accepted")
	}
	for _, snapshot := range []string{got.RequestBody, got.RequestHeaders, got.ResponseBody, got.Error.Error()} {
		for _, variant := range variants {
			if strings.Contains(snapshot, variant) {
				t.Errorf("credential encoding leaked: %q", variant)
			}
		}
		if len(snapshot) > 32<<10 {
			t.Error("snapshot exceeds limit")
		}
	}
	if !strings.Contains(got.ResponseBody, "[已截断]") {
		t.Fatal("missing truncation marker")
	}
}

// TestRunHTTPAsyncVideo 验证视频提交后真正轮询、使用固定任务 ID 和认证，完成后才成功。
func TestRunHTTPAsyncVideo(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("poll lost auth")
		}
		if n == 1 {
			if r.Method != "POST" {
				t.Error("expected initial POST")
			}
			fmt.Fprint(w, `{"id":"video-123","status":"queued"}`)
		} else {
			if r.Method != "GET" || r.URL.Path != "/v1/videos/video-123" {
				t.Errorf("invalid poll: %s %s", r.Method, r.URL.Path)
			}
			fmt.Fprint(w, `{"id":"video-123","status":"completed"}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := runHTTP(ctx, server.Client(), Request{Protocol: "video"}, server.URL, "video-model", "secret")
	if got.Error != nil || calls.Load() != 2 || !strings.Contains(got.ResponseBody, "completed") {
		t.Fatalf("invalid async result: %+v", got)
	}
}

// TestRunHTTPCancellation 验证在响应读取及视频轮询中取消会退出，且提交状态不是成功。
func TestRunHTTPCancellation(t *testing.T) {
	for _, protocol := range []string{"chat", "video"} {
		t.Run(protocol, func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if protocol == "video" {
					fmt.Fprint(w, `{"id":"job","status":"queued"}`)
					close(entered)
					return
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan transportResult, 1)
			go func() { done <- runHTTP(ctx, server.Client(), Request{Protocol: protocol}, server.URL, "m", "secret") }()
			<-entered
			cancel()
			select {
			case got := <-done:
				if !errors.Is(got.Error, context.Canceled) {
					t.Fatalf("cancel not preserved: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel blocked")
			}
		})
	}
}

// TestRunHTTPRedirectAndTrace 验证禁止重定向的客户端不会把认证带到新目标，并实际记录首字节耗时。
func TestRunHTTPRedirectAndTrace(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	got := runHTTP(context.Background(), client, Request{Protocol: "chat"}, server.URL, "m", "secret")
	if got.Error == nil || got.Status != 307 || leaked.Load() || got.FirstByteMillis < 10 {
		t.Fatalf("redirect or trace incorrect: %+v", got)
	}
}

// TestValidateRequest 验证 Manager 可在候选遍历前统一预检，无效输入无需选择或调用 Key。
func TestValidateRequest(t *testing.T) {
	for _, request := range []Request{
		{Protocol: "chat", RequestHeaders: `{"Authorization":"bad"}`},
		{Protocol: "chat", RequestBody: `[]`},
		{Protocol: "video", RequestBody: `{"nested":{}}`},
		{Protocol: "video", RequestBody: `{"prompt":null}`},
		{Protocol: "unsupported"},
	} {
		if err := validateRequest(request); err == nil {
			t.Errorf("accepted invalid input: %+v", request)
		}
	}
	for _, protocol := range []string{"chat", "responses", "anthropic", "gemini", "image", "embedding", "rerank", "video"} {
		if err := validateRequest(Request{Protocol: protocol}); err != nil {
			t.Errorf("default %s rejected: %v", protocol, err)
		}
	}
}

// TestRunHTTPVideoPollFailure 确认已接受视频任务的后续失败或身份不一致不会标为成功。
func TestRunHTTPVideoPollFailure(t *testing.T) {
	for _, payload := range []string{`{"id":"job","status":"failed"}`, `{"id":"other","status":"completed"}`} {
		t.Run(payload, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					fmt.Fprint(w, `{"id":"job","status":"in_progress"}`)
				} else {
					fmt.Fprint(w, payload)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got := runHTTP(ctx, server.Client(), Request{Protocol: "video"}, server.URL, "m", "secret")
			if got.Error == nil || calls.Load() != 2 {
				t.Fatalf("poll failure accepted: %+v", got)
			}
		})
	}
}

// TestRunHTTPFirstByteTimeout 验证首响应超时独立于总期限，且收到首字节后不会错误取消慢正文。
func TestRunHTTPFirstByteTimeout(t *testing.T) {
	t.Run("headers-timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		got := runHTTP(ctx, server.Client(), Request{Protocol: "chat", firstByteSeconds: 1}, server.URL, "m", "secret")
		if got.Error == nil || !strings.Contains(got.Error.Error(), "首次响应超时") || ctx.Err() != nil {
			t.Fatalf("wrong first byte timeout: %+v", got)
		}
	})
	t.Run("body-uses-total-deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			time.Sleep(1100 * time.Millisecond)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		got := runHTTP(ctx, server.Client(), Request{Protocol: "chat", firstByteSeconds: 1}, server.URL, "m", "secret")
		if got.Error != nil {
			t.Fatalf("body cancelled by first byte timer: %+v", got)
		}
	})
}
