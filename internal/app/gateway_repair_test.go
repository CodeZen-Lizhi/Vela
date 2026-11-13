package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// nativeFixture 在真实配置和权限之上替换本地上游，允许验证原生 multipart 和资源 GET。
func nativeFixture(t *testing.T, kind string, handler http.HandlerFunc) *executionFixture {
	t.Helper()
	f := newExecutionFixture(t)
	if _, err := f.catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.kind',?) WHERE id='model-1'`, kind); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	var err error
	f.provider, err = f.vendors.Update(context.Background(), f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: upstream.URL, Request: f.provider.Request})
	if err != nil {
		t.Fatal(err)
	}
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	return f
}

// TestGatewayNativeAuxiliaryOperations 通过正式鉴权 HTTP 入口验证压缩和计数，不能仅测试路由存在。
func TestGatewayNativeAuxiliaryOperations(t *testing.T) {
	for _, tc := range []struct {
		// path 与 response 是同格式附属操作的真实请求路径和结果。
		path, response string
	}{
		{"/v1/responses/compact", `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"compact-state"}]}`},
		{"/v1/messages/count_tokens", `{"input_tokens":12}`},
		{"/v1beta/models/logical-model:countTokens", `{"totalTokens":12}`},
		{"/v1/models/logical-model:generateContent", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			f := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.Contains(tc.path, "/models/") && !strings.Contains(r.URL.Path, "/models/upstream-model:") {
					t.Error("Gemini 附属操作丢失模型映射")
				}
				_, _ = io.WriteString(w, tc.response)
			})
			status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), tc.path, `{"model":"logical-model","input":[],"messages":[],"contents":[]}`, nil)
			if status != 200 || string(body) != tc.response || calls.Load() != 1 {
				t.Fatalf("附属操作失败: status=%d calls=%d body=%s", status, calls.Load(), body)
			}
		})
	}
}

// TestGatewayMediaProtocols 验证向量、图片与搜索通过真实调度出站，并保存正确业务用量。
func TestGatewayMediaProtocols(t *testing.T) {
	for _, tc := range []struct {
		// kind、path、body、response 固定本次原生业务类型和协议载荷。
		kind, path, body, response string
	}{
		{"vector", "/v1/embeddings", `{"model":"logical-model","input":"hello"}`, `{"data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`},
		{"vector", "/v1/rerank", `{"model":"logical-model","query":"q","documents":["a"]}`, `{"results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":4}}`},
		{"image", "/v1/images/generations", `{"model":"logical-model","prompt":"a tree","size":"1024x1024"}`, `{"data":[{"url":"https://image.invalid/result.png"}]}`},
		{"vector", "/v1beta/models/logical-model:embedContent", `{"content":{"parts":[{"text":"hi"}]}}`, `{"embedding":{"values":[0.1,0.2]}}`},
		{"vector", "/v1beta/models/logical-model:batchEmbedContents", `{"requests":[{"model":"models/logical-model","content":{"parts":[{"text":"hi"}]}}]}`, `{"embeddings":[{"values":[0.1,0.2]}]}`},
		{"chat", "/v1/alpha/search", `{"model":"logical-model","query":"hello"}`, `{"results":[{"title":"result"}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var calls atomic.Int32
			f := nativeFixture(t, tc.kind, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !strings.Contains(r.URL.Path, "/models/") && body["model"] != "upstream-model" {
					t.Error("非聊天出站丢失模型映射")
				}
				if r.Header.Get("OpenAI-Organization") != "org-test" || r.Header.Get("Cookie") != "" {
					t.Error("业务头遗漏或客户端 Cookie 外泄")
				}
				if requests, ok := body["requests"].([]any); ok && requests[0].(map[string]any)["model"] != "models/upstream-model" {
					t.Error("批量向量请求未映射内部模型")
				}
				_, _ = io.WriteString(w, tc.response)
			})
			status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), tc.path, tc.body, http.Header{"Openai-Organization": []string{"org-test"}, "Cookie": []string{"private=x"}})
			if status != 200 || string(body) != tc.response || calls.Load() != 1 {
				t.Fatalf("媒体请求失败: %d %s", status, body)
			}
			page, err := f.execution.Executor.Records.Query(context.Background(), records.Query{})
			if err != nil || len(page.Records) != 1 || page.Records[0].State != "success" || page.Records[0].Type == "chat" && tc.kind != "chat" {
				t.Fatalf("媒体记录分类或结算错误: %+v %v", page, err)
			}
		})
	}
}

// TestGatewayVideoLifecycle 验证只提交一次、原 Key 轮询到终态、下载绑定及绑定失效后零出站。
func TestGatewayVideoLifecycle(t *testing.T) {
	var creates, polls, downloads atomic.Int32
	f := nativeFixture(t, "video", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key-a" {
			t.Error("视频资源改变了原 Key")
		}
		switch {
		case r.Method == http.MethodPost:
			creates.Add(1)
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("model") != "upstream-model" {
				t.Error("视频未按原生表单和映射发送")
			}
			_, _ = io.WriteString(w, `{"id":"video-1","status":"queued"}`)
		case strings.HasSuffix(r.URL.Path, "/content"):
			downloads.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{0, 1, 2, 3})
		default:
			polls.Add(1)
			_, _ = io.WriteString(w, `{"id":"video-1","status":"completed","seconds":"4","size":"1280x720"}`)
		}
	})
	base := startCompatibilityGateway(t, f)
	status, body := compatibilityRequest(t, base, "/v1/videos", `{"model":"logical-model","prompt":"test","seconds":"4","size":"1280x720"}`, nil)
	if status != 200 || !bytes.Contains(body, []byte("completed")) || creates.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("视频终态失败: %d %s", status, body)
	}
	request, _ := http.NewRequest(http.MethodGet, base+"/v1/videos/video-1/content", nil)
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(data, []byte{0, 1, 2, 3}) || downloads.Load() != 1 {
		t.Fatal("视频二进制未原样返回")
	}
	if _, err := f.catalog.DB().Exec(`UPDATE provider_keys SET enabled=0 WHERE id=?`, f.keys[0].ID); err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 409 || downloads.Load() != 1 {
		t.Fatal("绑定失效后切换其他 Key 下载")
	}
}

// TestGatewayImageMultipart 保留图片原始文件字节，同时映射模型名。
func TestGatewayImageMultipart(t *testing.T) {
	image := []byte{0, 1, 255, 7}
	f := nativeFixture(t, "image", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, image) || r.FormValue("model") != "upstream-model" {
			t.Error("图片字节或模型映射丢失")
		}
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"AQID"}]}`)
	})
	var wire bytes.Buffer
	writer := multipart.NewWriter(&wire)
	_ = writer.WriteField("model", "logical-model")
	part, _ := writer.CreateFormFile("image", "image.png")
	_, _ = part.Write(image)
	_ = writer.Close()
	request, _ := http.NewRequest(http.MethodPost, startCompatibilityGateway(t, f)+"/v1/images/edits", &wire)
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(data, []byte("AQID")) {
		t.Fatalf("图片编辑失败: %d %s", response.StatusCode, data)
	}
}

// TestGatewayGeminiFileLifecycle 验证 resumable URL 本机化、二进制上传、文件查询与未知资源零出站。
func TestGatewayGeminiFileLifecycle(t *testing.T) {
	var calls, uploads atomic.Int32
	f := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Goog-Api-Key") != "key-a" {
			t.Error("文件请求改变了原 Key")
		}
		switch r.URL.Path {
		case "/upload/v1beta/files":
			if r.Header.Get("X-Goog-Upload-Protocol") != "resumable" {
				t.Error("上传协议头丢失")
			}
			w.Header().Set("X-Goog-Upload-URL", "http://"+r.Host+"/upload-session?upload_id=test")
			w.WriteHeader(200)
		case "/upload-session":
			data, _ := io.ReadAll(r.Body)
			if !bytes.Equal(data, []byte{0, 1, 255}) || r.URL.Query().Get("upload_id") != "test" || r.Header.Get("X-Goog-Upload-Command") != "upload, finalize" {
				t.Error("文件分段或上传状态丢失")
			}
			uploads.Add(1)
			_, _ = io.WriteString(w, `{"file":{"name":"files/file-1","mimeType":"image/png","state":"ACTIVE"}}`)
		case "/v1beta/files/file-1":
			_, _ = io.WriteString(w, `{"name":"files/file-1","state":"ACTIVE"}`)
		default:
			t.Errorf("意外文件路径 %s", r.URL.Path)
		}
	})
	base := startCompatibilityGateway(t, f)
	request, _ := http.NewRequest(http.MethodPost, base+"/upload/v1beta/files", strings.NewReader(`{"file":{"display_name":"sample"}}`))
	request.Host = "untrusted.example:1234"
	request.Header.Set("X-Goog-Api-Key", "gateway-test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Goog-Upload-Protocol", "resumable")
	request.Header.Set("X-Goog-Upload-Command", "start")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	uploadURL := response.Header.Get("X-Goog-Upload-URL")
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(uploadURL, base+"/upload/v1beta/files/") {
		t.Fatalf("上传会话未绑定本机: %d %s %s", response.StatusCode, uploadURL, data)
	}
	request, _ = http.NewRequest(http.MethodPost, uploadURL, bytes.NewReader([]byte{0, 1, 255}))
	request.Header.Set("X-Goog-Api-Key", "gateway-test-key")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	request.Header.Set("X-Goog-Upload-Offset", "0")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || uploads.Load() != 1 || !bytes.Contains(data, []byte("file-1")) {
		t.Fatalf("上传失败: %d %s", response.StatusCode, data)
	}
	for _, tc := range []struct {
		id     string
		status int
	}{{"file-1", 200}, {"unknown", 409}} {
		request, _ = http.NewRequest(http.MethodGet, base+"/v1beta/files/"+tc.id, nil)
		request.Header.Set("X-Goog-Api-Key", "gateway-test-key")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("文件查询 %s status=%d", tc.id, response.StatusCode)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("未知文件仍出站: %d", calls.Load())
	}
}

// TestGatewayCommittedFailureEvent 验证流式失败向客户端明确回传并进入同一条记录的实际正文。
func TestGatewayCommittedFailureEvent(t *testing.T) {
	f := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
	})
	status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","stream":true,"messages":[]}`, nil)
	if status != 200 || !bytes.Contains(body, []byte("event: error")) || bytes.Contains(body, []byte("[DONE]")) {
		t.Fatalf("中断未向客户端明示: %d %s", status, body)
	}
	page, err := f.execution.Executor.Records.Query(context.Background(), records.Query{})
	if err != nil || len(page.Records) != 1 {
		t.Fatal(err)
	}
	record, err := f.execution.Executor.Records.Get(context.Background(), page.Records[0].ID)
	if err != nil || record.State != "failed" || record.Client.ResponseBody == "" || !strings.Contains(record.Client.ResponseBody, "upstream_stream_error") {
		t.Fatalf("客户端错误事件未入记录: %+v %v", record, err)
	}
}

// TestGatewayInteractionOwnership 验证 model/agent、原生 SSE 与后续请求的原 Key 绑定。
func TestGatewayInteractionOwnership(t *testing.T) {
	for _, mode := range []string{"model", "agent", "stream"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			f := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if r.Method == http.MethodPost {
					_ = json.NewDecoder(r.Body).Decode(&body)
					key := "model"
					if mode == "agent" {
						key = "agent"
					}
					if body[key] != "upstream-model" {
						t.Errorf("Interactions身份未映射: %v", body)
					}
					if mode == "agent" && body["model"] != nil {
						t.Error("agent请求被错误加入model")
					}
				}
				if mode == "stream" && r.Method == http.MethodPost {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction-1\",\"status\":\"in_progress\"}}\n\ndata: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"hello\"}}\n\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"interaction-1\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":100,\"total_cached_tokens\":30,\"total_output_tokens\":20,\"total_thought_tokens\":5}}}\n\n")
				} else {
					_, _ = io.WriteString(w, `{"id":"interaction-1","status":"completed","steps":[],"usage":{"total_input_tokens":100,"total_cached_tokens":30,"total_output_tokens":20,"total_thought_tokens":5}}`)
				}
			})
			base := startCompatibilityGateway(t, f)
			input := map[string]any{"model": "logical-model", "input": "hello"}
			if mode == "agent" {
				delete(input, "model")
				input["agent"] = "logical-model"
			}
			if mode == "stream" {
				input["stream"] = true
			}
			data, _ := json.Marshal(input)
			status, out := compatibilityRequest(t, base, "/v1beta/interactions", string(data), http.Header{"X-Goog-Api-Key": []string{"gateway-test-key"}})
			if status != 200 || !bytes.Contains(out, []byte("interaction-1")) || mode == "stream" && !bytes.Contains(out, []byte("interaction.completed")) {
				t.Fatalf("Interactions失败: %d %s", status, out)
			}
			page, queryErr := f.execution.Executor.Records.Query(context.Background(), records.Query{})
			if queryErr != nil || len(page.Records) != 1 || page.Records[0].Usage == nil || page.Records[0].Usage.Input != 70 || page.Records[0].Usage.Output != 25 || page.Records[0].Usage.CacheRead != 30 {
				t.Fatalf("交互用量未记录: %+v %v", page, queryErr)
			}
			request, _ := http.NewRequest(http.MethodGet, base+"/v1beta/interactions/interaction-1", nil)
			request.Header.Set("X-Goog-Api-Key", "gateway-test-key")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("资源查询失败: %d", response.StatusCode)
			}
			if _, err = f.catalog.DB().Exec(`UPDATE provider_keys SET enabled=0 WHERE id=?`, f.keys[0].ID); err != nil {
				t.Fatal(err)
			}
			input["previous_interaction_id"] = "interaction-1"
			data, _ = json.Marshal(input)
			status, _ = compatibilityRequest(t, base, "/v1beta/interactions", string(data), http.Header{"X-Goog-Api-Key": []string{"gateway-test-key"}})
			if status != 409 || calls.Load() != 2 {
				t.Fatalf("续轮越过原Key: status=%d calls=%d", status, calls.Load())
			}
		})
	}
}

// TestGatewayRejectsInvalidSpecialResults 防止 null 字段伪装成合法排序或交互结果。
func TestGatewayRejectsInvalidSpecialResults(t *testing.T) {
	for _, tc := range []struct {
		// kind、path、response 定义非法上游信封的业务类型、入口与内容。
		kind, path, response string
	}{{"vector", "/v1/rerank", `{"results":null}`}, {"chat", "/v1beta/interactions", `{"id":null}`}} {
		t.Run(tc.path, func(t *testing.T) {
			f := nativeFixture(t, tc.kind, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.response) })
			status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), tc.path, `{"model":"logical-model","query":"hi","documents":["a"],"input":"hi"}`, nil)
			if status != 502 {
				t.Fatalf("非法终态被接受: %d %s", status, body)
			}
		})
	}
}

// TestGatewayMediaFirstResponseDeadline 验证原生媒体实际执行供应商首响应期限，超时不重复创建任务。
func TestGatewayMediaFirstResponseDeadline(t *testing.T) {
	var calls atomic.Int32
	f := nativeFixture(t, "video", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 先读完请求，HTTP 服务才能观察远端关闭并取消请求上下文。
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	draft := providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: f.provider.Request}
	draft.Request.FirstByteSeconds = 1
	draft.Request.RequestSeconds = 5
	if _, err := f.vendors.Update(context.Background(), f.provider.ID, draft); err != nil {
		t.Fatal(err)
	}
	status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/videos", `{"model":"logical-model","prompt":"hello"}`, nil)
	if status != 504 || calls.Load() != 1 {
		t.Fatalf("媒体首响应超时未生效或重复创建: %d calls=%d %s", status, calls.Load(), body)
	}
}
