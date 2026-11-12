package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// encodeBody 使用真实编码器构造客户端请求，不复制网关的解码逻辑。
func encodeBody(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&encoded)
	case "deflate":
		writer = zlib.NewWriter(&encoded)
	case "raw-deflate":
		writer, _ = flate.NewWriter(&encoded, flate.DefaultCompression)
	case "zstd":
		writer, _ = zstd.NewWriter(&encoded, zstd.WithEncoderConcurrency(1))
	default:
		t.Fatalf("未知测试编码 %s", encoding)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// TestEncodedRequestsReachExecutor 验证所有旧版编码与叠加编码在正式入口解码，并移除过期传输头。
func TestEncodedRequestsReachExecutor(t *testing.T) {
	body := []byte(`{"model":"chat-model","messages":[{"role":"user","content":"你好"}]}`)
	for _, encoding := range []string{"gzip", "deflate", "raw-deflate", "zstd", "gzip, zstd"} {
		t.Run(encoding, func(t *testing.T) {
			data := body
			for _, item := range strings.Split(encoding, ", ") {
				data = encodeBody(t, item, data)
			}
			executor := &fakeExecutor{body: `{}`}
			server := newGatewayTestServer(t, executor)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(data))
			request.Header.Set("Authorization", "Bearer test-access-key")
			request.Header.Set("Content-Encoding", strings.ReplaceAll(encoding, "raw-deflate", "deflate"))
			response := httptest.NewRecorder()
			server.handler().ServeHTTP(response, request)
			if response.Code != 200 || !bytes.Equal(executor.request.Body, body) {
				t.Fatalf("解码结果: status=%d body=%s", response.Code, response.Body.String())
			}
			if executor.request.Headers.Get("Content-Encoding") != "" {
				t.Fatal("已解码正文仍携带原编码")
			}
		})
	}
}

// TestInvalidEncodingAndCapacity 保证非法压缩、未知编码及明显超限请求在出站前失败。
func TestInvalidEncodingAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		// name、encoding、length 和 status 定义入口错误及期望状态。
		name, encoding string
		length         int64
		status         int
	}{
		{"broken", "gzip", 3, 400},
		{"unsupported", "br", 3, 415},
		{"oversize", "", requestBodyLimit + 1, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &fakeExecutor{}
			server := newGatewayTestServer(t, executor)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("bad"))
			request.Header.Set("Authorization", "Bearer test-access-key")
			request.Header.Set("Content-Encoding", tc.encoding)
			request.ContentLength = tc.length
			response := httptest.NewRecorder()
			server.handler().ServeHTTP(response, request)
			if response.Code != tc.status || executor.request.Request.Model != "" {
				t.Fatalf("错误请求被执行: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

// TestLongContextExceedsFormerLimit 验证旧 16 MiB 边界不再拒绝容量范围内的合法上下文。
func TestLongContextExceedsFormerLimit(t *testing.T) {
	executor := &fakeExecutor{body: `{}`}
	server := newGatewayTestServer(t, executor)
	body := `{"model":"chat-model","messages":[{"role":"user","content":"` + strings.Repeat("x", 17<<20) + `"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-access-key")
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != 200 || len(executor.request.Body) != len(body) {
		t.Fatalf("长上下文被拒绝或截断: %d", response.Code)
	}
}
