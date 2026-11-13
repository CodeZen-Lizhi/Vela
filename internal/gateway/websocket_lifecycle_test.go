package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
)

// TestWebSocketShutdownWaitsForSlowFinalization 通过真实双端 WebSocket 和 SQLite 验证取消后的收尾超过两秒时，关闭和暂停仍等待最终记录落库。
func TestWebSocketShutdownWaitsForSlowFinalization(t *testing.T) {
	for _, protocol := range []string{"responses", "realtime"} {
		for _, operation := range []string{"close", "pause"} {
			t.Run(protocol+"/"+operation, func(t *testing.T) {
				t.Parallel()
				canceled := make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
					defer cancel()
					event := `{"type":"session.created"}`
					if protocol == "responses" {
						if _, _, err := conn.Read(ctx); err != nil {
							t.Error(err)
							return
						}
						event = `{"type":"response.created","response":{"id":"slow-finalization","status":"in_progress"}}`
					}
					if err := conn.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
						t.Error(err)
						return
					}
					_, _, _ = conn.Read(ctx)
					close(canceled)
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
						return persist(ctx)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				path := "/v1/responses"
				if protocol == "realtime" {
					path = "/v1/realtime?model=lifecycle-model"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(lifecycleURL(f.port, path), "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer lifecycle-key"}}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				if protocol == "responses" {
					if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"lifecycle-model","input":"hello"}`)); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := conn.Read(ctx); err != nil {
					t.Fatal(err)
				}
				stopped := make(chan error, 1)
				go func() {
					if operation == "pause" {
						stopped <- f.server.Pause(context.Background())
					} else {
						stopped <- f.server.Close(context.Background())
					}
				}()
				awaitLifecycle(t, canceled, "WebSocket 上游取消")
				awaitLifecycle(t, finishing, "WebSocket 业务收尾")
				select {
				case err := <-stopped:
					t.Fatalf("超过两秒的收尾完成前已返回: %v", err)
				case <-time.After(2200 * time.Millisecond):
				}
				page, err := f.history.Query(context.Background(), records.Query{})
				if err != nil || len(page.Records) != 1 || page.Records[0].State != "active" {
					t.Fatalf("未保留实际待收尾记录: %+v %v", page, err)
				}
				unblock()
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(8 * time.Second):
					t.Fatal("WebSocket 收尾完成后关闭仍未返回")
				}
				page, err = f.history.Query(context.Background(), records.Query{})
				if err != nil || len(page.Records) != 1 || page.Records[0].State != "failed" || page.Records[0].EndedAt == 0 || page.Records[0].Transport != "websocket" || len(page.Records[0].Attempts) != 1 {
					t.Fatalf("返回时 WebSocket 记录尚未完成: %+v %v", page, err)
				}
				if f.server.Running() || f.server.Paused() != (operation == "pause") {
					t.Fatal("收尾后监听或暂停状态不符")
				}
				if operation == "pause" {
					if err := f.server.Resume(context.Background()); err != nil {
						t.Fatal(err)
					}
					assertLifecycleHealth(t, f.port)
				}
			})
		}
	}
}
