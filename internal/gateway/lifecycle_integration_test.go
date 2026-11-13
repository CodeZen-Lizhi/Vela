package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/app"
	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// lifecycleKeys 仅保存隔离测试的主密钥，不访问用户钥匙串或日常数据。
type lifecycleKeys struct {
	// data 是测试专用的主密钥文本。
	data []byte
}

// Load 读取隔离主密钥，尚未生成时使用供应商模块的标准缺失错误。
func (k *lifecycleKeys) Load() ([]byte, error) {
	if k.data == nil {
		return nil, providers.ErrNoMasterKey
	}
	return bytes.Clone(k.data), nil
}

// Save 保存测试主密钥的独立副本。
func (k *lifecycleKeys) Save(data []byte) error { k.data = bytes.Clone(data); return nil }

// lifecycleFixture 连接真实网关、调度、凭据、SQLite 和请求记录，唯一外部服务是本机测试上游。
type lifecycleFixture struct {
	// server 和 port 是真实监听入口及恢复时使用的端口。
	server *gateway.Server
	port   int
	// history 用于在生命周期方法返回后重读落库结果。
	history *records.Store
	// execution 提供实际 HTTP runner 与可控收尾观察点。
	execution *app.SchedulingExecution
}

// freeLifecyclePort 为公开 Listen 接口选择本机测试端口，绑定失败仍由真实 Listen 报错。
func freeLifecyclePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// newLifecycleFixture 通过正式模块接口创建一个模型、一把测试密钥和直连供应商，并启动鉴权网关。
func newLifecycleFixture(t *testing.T, upstream string) *lifecycleFixture {
	t.Helper()
	ctx := context.Background()
	catalog, err := models.Open(filepath.Join(t.TempDir(), "lifecycle.sqlite"), http.DefaultClient, upstream)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := catalog.Close(); err != nil {
			t.Error(err)
		}
	})
	model, err := catalog.Create(ctx, models.Create{ModelID: "lifecycle-model", Name: "Lifecycle", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	vendors, err := providers.New(catalog.DB(), &lifecycleKeys{}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := vendors.InitModels(); err != nil {
		t.Fatal(err)
	}
	provider, err := vendors.Create(ctx, providers.ProviderDraft{Name: "Lifecycle", URL: upstream, Request: providers.RequestPolicy{Attempts: 1, ResponsesWebSocket: true, Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}})
	if err != nil {
		t.Fatal(err)
	}
	multiplier := 1.0
	if _, err := vendors.CreateKey(ctx, provider.ID, providers.ProviderKeyDraft{Name: "Test", Secret: "local-upstream-key", Multiplier: &multiplier, CacheMinutes: 5, ProbeMinutes: 10, Proxy: providers.ProxyChoice{Mode: providers.ProxyProvider}}); err != nil {
		t.Fatal(err)
	}
	if err := vendors.SaveModels(ctx, provider.ID, providers.ProviderModelsDraft{ModelIDs: []string{model.ID}}); err != nil {
		t.Fatal(err)
	}
	policy, err := scheduling.New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	history, err := records.New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := app.NewSchedulingExecution(ctx, policy, vendors, catalog, history)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(execution.Close)
	server := gateway.New(catalog, execution.Executor)
	server.SetAccessKey("lifecycle-key")
	port := freeLifecyclePort(t)
	if err := server.Listen(ctx, port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &lifecycleFixture{server: server, port: port, history: history, execution: execution}
}

// lifecycleURL 构造公开 HTTP 入口地址。
func lifecycleURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

// awaitLifecycle 等待真实 IO 或收尾同步点，超时给出具体缺失阶段。
func awaitLifecycle(t *testing.T, signal <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(8 * time.Second):
		t.Fatalf("等待%s超时", stage)
	}
}

// assertLifecycleHealth 验证指定端口仍能完成真实鉴权请求。
func assertLifecycleHealth(t *testing.T, port int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, lifecycleURL(port, "/v1/health"), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer lifecycle-key")
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"status":"ok"`)) {
		t.Fatalf("健康请求失败: status=%d body=%s err=%v", response.StatusCode, body, err)
	}
}

// assertLifecycleStopped 验证旧监听已经释放，不仅仅是状态标记改变。
func assertLifecycleStopped(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("已停止的监听仍接受连接")
	}
}

// TestHTTPShutdownCancelsAndFinishesRecords 通过真实 HTTP/SSE 和 SQLite 验证关闭、暂停、切端口及交错关闭等待取消后的落库收尾。
func TestHTTPShutdownCancelsAndFinishesRecords(t *testing.T) {
	for _, operation := range []string{"close", "pause", "switch", "switch-close"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", operation, stream), func(t *testing.T) {
				t.Parallel()
				started, canceled := make(chan struct{}), make(chan struct{})
				abort := make(chan struct{})
				defer close(abort)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-abort:
					}
				}))
				t.Cleanup(upstream.Close)
				f := newLifecycleFixture(t, upstream.URL)
				finishing, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				persist := f.execution.Executor.Persist
				f.execution.Executor.Persist = func(ctx context.Context) error {
					close(finishing)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					return persist(ctx)
				}
				clientDone, streaming := make(chan struct{}), make(chan struct{})
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, lifecycleURL(f.port, "/v1/chat/completions"), strings.NewReader(fmt.Sprintf(`{"model":"lifecycle-model","stream":%t,"messages":[]}`, stream)))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer lifecycle-key")
				go func() {
					defer close(clientDone)
					response, err := http.DefaultClient.Do(request)
					if err != nil {
						return
					}
					defer response.Body.Close()
					reader := bufio.NewReader(response.Body)
					if stream {
						if line, err := reader.ReadString('\n'); err == nil && strings.Contains(line, "partial") {
							close(streaming)
						}
					}
					_, _ = io.Copy(io.Discard, reader)
				}()
				awaitLifecycle(t, started, "上游进入请求")
				if stream {
					awaitLifecycle(t, streaming, "SSE 实际刷新")
				}
				stopped := make(chan error, 1)
				shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
				shutdownCancel()
				nextPort := freeLifecyclePort(t)
				go func() {
					switch operation {
					case "close":
						stopped <- f.server.Close(shutdownCtx)
					case "pause":
						stopped <- f.server.Pause(shutdownCtx)
					default:
						stopped <- f.server.Listen(shutdownCtx, nextPort)
					}
				}()
				awaitLifecycle(t, canceled, "超时强制取消上游")
				awaitLifecycle(t, finishing, "业务收尾开始")
				assertLifecycleStopped(t, f.port)
				if strings.HasPrefix(operation, "switch") {
					assertLifecycleHealth(t, nextPort)
				}
				// 第二个生命周期操作必须等待前一个操作的旧请求收尾，不能因 server 已清空而提前成功。
				queued := make(chan error, 1)
				go func() {
					if operation == "pause" {
						queued <- f.server.Resume(context.Background())
					} else if operation == "switch" {
						queued <- f.server.Listen(context.Background(), nextPort)
					} else {
						queued <- f.server.Close(context.Background())
					}
				}()
				select {
				case err := <-stopped:
					t.Fatalf("业务收尾前已返回: %v", err)
				case err := <-queued:
					t.Fatalf("交错操作越过旧请求收尾: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				page, err := f.history.Query(context.Background(), records.Query{})
				if err != nil || len(page.Records) != 1 || page.Records[0].State != "active" {
					t.Fatalf("未保留真实待收尾状态: %+v %v", page, err)
				}
				unblock()
				for _, result := range []<-chan error{stopped, queued} {
					select {
					case err := <-result:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(8 * time.Second):
						t.Fatal("生命周期操作收尾死锁")
					}
				}
				page, err = f.history.Query(context.Background(), records.Query{})
				if err != nil || len(page.Records) != 1 || page.Records[0].State != "failed" || page.Records[0].EndedAt == 0 || len(page.Records[0].Attempts) != 1 || page.Records[0].Attempts[0].DurationMillis == 0 {
					t.Fatalf("返回时记录尚未完成: %+v %v", page, err)
				}
				awaitLifecycle(t, clientDone, "客户端结束")
				switch operation {
				case "pause":
					assertLifecycleHealth(t, f.port)
				case "switch":
					assertLifecycleHealth(t, nextPort)
				default:
					if f.server.Running() {
						t.Fatal("关闭完成后仍在运行")
					}
				}
			})
		}
	}
}

// TestHTTPShutdownAllowsGracefulCompletion 验证自然等待窗口内的真实请求不被取消，并在成功记录落库后返回。
func TestHTTPShutdownAllowsGracefulCompletion(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"completed"},"finish_reason":"stop"}]}`)
		case <-r.Context().Done():
			t.Error("自然等待窗口内请求被取消")
		}
	}))
	t.Cleanup(upstream.Close)
	f := newLifecycleFixture(t, upstream.URL)
	responseDone := make(chan error, 1)
	request, err := http.NewRequest(http.MethodPost, lifecycleURL(f.port, "/v1/chat/completions"), strings.NewReader(`{"model":"lifecycle-model","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer lifecycle-key")
	go func() {
		client := &http.Client{Timeout: 8 * time.Second}
		response, err := client.Do(request)
		if err == nil {
			defer response.Body.Close()
			var body []byte
			body, err = io.ReadAll(response.Body)
			if err == nil && (response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("completed"))) {
				err = fmt.Errorf("自然完成响应不符: status=%d body=%s", response.StatusCode, body)
			}
		}
		responseDone <- err
	}()
	awaitLifecycle(t, started, "请求进入自然等待")
	stopped := make(chan error, 1)
	go func() { stopped <- f.server.Close(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("自然请求完成前已返回: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	for _, result := range []<-chan error{responseDone, stopped} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("自然完成流程未结束")
		}
	}
	page, err := f.history.Query(context.Background(), records.Query{})
	if err != nil || len(page.Records) != 1 || page.Records[0].State != "success" || page.Records[0].EndedAt == 0 {
		t.Fatalf("自然完成后记录不符: %+v %v", page, err)
	}
}

// TestHTTPListenFailureAndRestart 验证绑定失败保留原监听和端口，并支持暂停恢复、重复关闭及重新监听。
func TestHTTPListenFailureAndRestart(t *testing.T) {
	f := newLifecycleFixture(t, "http://127.0.0.1:1")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := f.server.Listen(context.Background(), occupied.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("占用端口绑定应失败")
	}
	assertLifecycleHealth(t, f.port)
	if err := f.server.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertLifecycleStopped(t, f.port)
	if !f.server.Paused() || !f.server.Configured() {
		t.Fatal("暂停丢失可恢复状态")
	}
	if err := f.server.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertLifecycleHealth(t, f.port)
	for range 2 {
		if err := f.server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	assertLifecycleStopped(t, f.port)
	if err := f.server.Listen(context.Background(), f.port); err != nil {
		t.Fatal(err)
	}
	assertLifecycleHealth(t, f.port)
}
