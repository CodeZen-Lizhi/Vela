package gateway

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"time"
)

// httpLifecycle 跟踪单次监听已进入的 HTTP handler，升级后的连接由网关原有 WebSocket 生命周期管理。
type httpLifecycle struct {
	// Server 提供标准 HTTP 监听、自然关闭和连接强制取消。
	*http.Server
	// mu 将停止接入与 handler 登记互斥，保证 Wait 开始后不会再增加任务。
	mu sync.Mutex
	// stopping 表示当前监听已禁止进入业务 handler。
	stopping bool
	// handlers 只等待尚未返回且未成功升级的业务 handler。
	handlers sync.WaitGroup
}

// newHTTPLifecycle 为监听创建独立接入门闩和 handler 跟踪，保留流式刷新与升级能力。
func newHTTPLifecycle(handler http.Handler) *httpLifecycle {
	lifecycle := &httpLifecycle{Server: &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}}
	lifecycle.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lifecycle.mu.Lock()
		if lifecycle.stopping {
			lifecycle.mu.Unlock()
			http.Error(w, "网关正在停止", http.StatusServiceUnavailable)
			return
		}
		lifecycle.handlers.Add(1)
		lifecycle.mu.Unlock()
		var once sync.Once
		finish := func() { once.Do(lifecycle.handlers.Done) }
		defer finish()
		handler.ServeHTTP(&lifecycleWriter{ResponseWriter: w, finish: finish}, r)
	})
	return lifecycle
}

// stopAdmission 禁止后续 handler 进入业务逻辑，不依赖连接关闭与 HTTP 调度的先后顺序。
func (l *httpLifecycle) stopAdmission() {
	l.mu.Lock()
	l.stopping = true
	l.mu.Unlock()
}

// lifecycleWriter 保留 HTTP 响应能力，并在成功劫持连接时将清理责任移交给 WebSocket 管理。
type lifecycleWriter struct {
	// ResponseWriter 是未改变头部、状态码与正文语义的原始响应出口。
	http.ResponseWriter
	// finish 幂等结束当前普通 HTTP handler 的生命周期登记。
	finish func()
}

// Unwrap 让标准 ResponseController 仍能访问底层连接的期限等能力。
func (w *lifecycleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush 保留 SSE 逐段刷新能力，沿用 http.Flusher 不返回错误的契约。
func (w *lifecycleWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// Hijack 仅在连接成功升级后结束普通 HTTP 跟踪，失败仍等待 handler 返回。
func (w *lifecycleWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.finish()
	}
	return conn, buffer, err
}
