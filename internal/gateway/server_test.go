package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// fakeExecutor 记录网关传递的请求并返回可控制的终态，不复制调度业务。
type fakeExecutor struct {
	request scheduling.ExecutionRequest
	result  scheduling.ExecutionResult
	err     error
	body    string
}

// Execute 保存请求，并按测试配置写出成功正文或返回错误。
func (f *fakeExecutor) Execute(_ context.Context, request scheduling.ExecutionRequest, writer io.Writer) (scheduling.ExecutionResult, error) {
	f.request = request
	if f.body != "" {
		_, _ = io.WriteString(writer, f.body)
		f.result.Result.Committed = true
	}
	return f.result, f.err
}

// newGatewayTestServer 创建带真实 SQLite 模型清单和可控执行器的网关。
func newGatewayTestServer(t *testing.T, executor *fakeExecutor) *Server {
	t.Helper()
	store, err := models.Open(filepath.Join(t.TempDir(), "vela.sqlite"), http.DefaultClient, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err = store.Close(); err != nil {
			t.Errorf("关闭数据库: %v", err)
		}
	})
	for _, draft := range []models.Create{
		{ModelID: "chat-model", Name: "Chat Model", Kind: "chat"},
		{ModelID: "image-model", Name: "Image Model", Kind: "image"},
	} {
		if _, err = store.Create(context.Background(), draft); err != nil {
			t.Fatal(err)
		}
	}
	server := New(store, executor)
	server.SetAccessKey("test-access-key")
	return server
}

// TestAuthorizationRejectsMissingAndWrongKeys 验证鉴权失败不会进入执行器。
func TestAuthorizationRejectsMissingAndWrongKeys(t *testing.T) {
	executor := &fakeExecutor{}
	server := newGatewayTestServer(t, executor)
	for _, authorization := range []string{"", "Bearer wrong"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d，期望 401", response.Code)
		}
	}
}

// TestModelsListsConfiguredModels 验证全部已添加类型以统一模型名公开，入口另行校验业务类别。
func TestModelsListsConfiguredModels(t *testing.T) {
	server := newGatewayTestServer(t, &fakeExecutor{})
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer test-access-key")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文 = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []modelObject `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 2 || payload.Data[0].ID != "chat-model" || payload.Data[1].ID != "image-model" {
		t.Fatalf("模型列表 = %+v", payload.Data)
	}
}

// TestChatCompletionsBuildsSchedulingRequest 验证正文、流式开关和会话头进入真实调度边界。
func TestChatCompletionsBuildsSchedulingRequest(t *testing.T) {
	executor := &fakeExecutor{body: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"}
	server := newGatewayTestServer(t, executor)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"chat-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer test-access-key")
	request.Header.Set("session-id", "client-session")
	request.Header.Set("Cookie", "private=value")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文 = %s", response.Code, response.Body.String())
	}
	if executor.request.Request.Model != "chat-model" || executor.request.Request.Protocol != "openai:chat" || !executor.request.Stream || executor.request.Path != "/v1/chat/completions" {
		t.Fatalf("调度请求 = %+v", executor.request)
	}
	if executor.request.Headers.Get("Authorization") != "" || executor.request.Headers.Get("Cookie") != "" || executor.request.Headers.Get("session-id") != "client-session" {
		t.Fatalf("转发头 = %v", executor.request.Headers)
	}
	if !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("响应正文 = %s", response.Body.String())
	}
}

// TestResponsesBuildsSchedulingRequest 验证 Responses 请求使用同格式协议并保留原始正文。
func TestResponsesBuildsSchedulingRequest(t *testing.T) {
	body := `{"model":"chat-model","input":"hello","stream":false}`
	executor := &fakeExecutor{body: `{"status":"completed","output":[]}`}
	server := newGatewayTestServer(t, executor)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-access-key")
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	request.Header.Set("Cookie", "private=value")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文 = %s", response.Code, response.Body.String())
	}
	if executor.request.Request.Model != "chat-model" || executor.request.Request.Protocol != "openai:responses" || executor.request.Stream || executor.request.Path != "/v1/responses" {
		t.Fatalf("调度请求 = %+v", executor.request)
	}
	if string(executor.request.Body) != body {
		t.Fatalf("正文 = %s，期望原样传递", executor.request.Body)
	}
	if executor.request.Headers.Get("Cookie") != "" || executor.request.Headers.Get("OpenAI-Beta") != "responses=experimental" {
		t.Fatalf("转发头 = %v", executor.request.Headers)
	}
}

// TestChatCompletionsRejectsUnknownModel 验证未出现在模型页的名称不会进入调度。
func TestChatCompletionsRejectsUnknownModel(t *testing.T) {
	executor := &fakeExecutor{}
	server := newGatewayTestServer(t, executor)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"unknown","messages":[]}`))
	request.Header.Set("Authorization", "Bearer test-access-key")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，正文 = %s", response.Code, response.Body.String())
	}
	if executor.request.Request.Model != "" {
		t.Fatal("未知模型进入了执行器")
	}
}

// TestNativeProtocolAuthenticationErrors 验证原生协议错误信封与错误密钥拒绝，不把客户端密钥作为上游凭据。
func TestNativeProtocolAuthenticationErrors(t *testing.T) {
	server := newGatewayTestServer(t, &fakeExecutor{})
	for _, path := range []string{"/v1/messages", "/v1beta/models/chat-model:generateContent"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		server.handler().ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatal(response.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		envelope, _ := body["error"].(map[string]any)
		if path == "/v1/messages" && (body["type"] != "error" || envelope["type"] != "authentication_error") {
			t.Fatal(body)
		}
		if strings.Contains(path, "v1beta") && envelope["status"] != "UNAUTHENTICATED" {
			t.Fatal(body)
		}
	}
}
