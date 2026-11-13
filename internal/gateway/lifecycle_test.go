package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStoppedLifecycleRejectsLateHandlers 验证连接关闭前已调度但迟到的请求也不能再进入使用数据库的业务逻辑。
func TestStoppedLifecycleRejectsLateHandlers(t *testing.T) {
	lifecycle := newHTTPLifecycle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("停止后的请求进入了业务 handler")
	}))
	lifecycle.stopAdmission()
	response := httptest.NewRecorder()
	lifecycle.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("迟到请求状态码 = %d", response.Code)
	}
	if err := shutdownServer(context.Background(), lifecycle); err != nil {
		t.Fatal(err)
	}
}
