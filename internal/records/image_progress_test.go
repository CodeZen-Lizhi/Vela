package records

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestImageProgressCapture 通过真实 SSE 采集和 SQLite 重读核对图片事件，不将收到终态帧直接判为执行成功。
func TestImageProgressCapture(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "protocol-failed"}[failed], func(t *testing.T) {
			store, _ := fixture(t)
			ctx, finish, err := store.Begin(context.Background(), Input{ModelID: "m1", Type: "image", Protocol: "openai:image", Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx = WithTarget(ctx, Target{ProviderID: "p1", KeyID: "k1"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"PRIVATE_IMAGE\"}\n\ndata: {\"type\":\"image_generation.completed\"}\n\n")
			}))
			defer server.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"model":"logical"}`))
			if err != nil {
				t.Fatal(err)
			}
			response, err := (&http.Client{Transport: &Transport{}}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			if err = response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			var cause error
			if failed {
				cause = errors.New("图片流终态未通过协议验证")
			}
			if err = AttemptResult(ctx, cause); err != nil {
				t.Fatal(err)
			}
			if err = finish(cause); err != nil {
				t.Fatal(err)
			}
			page, err := store.Query(context.Background(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Get(context.Background(), page.Records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			progress := record.Attempts[0].ImageProgress
			expected := "completed"
			if failed {
				expected = "failed"
			}
			if progress == nil || progress.Phase != expected || progress.Frames != 2 || progress.PartialImages != 1 || progress.LastEvent != "image_generation.completed" || progress.LastEventAt == 0 {
				t.Fatalf("图片进度错误: %+v", progress)
			}
			if record.Attempts[0].FirstByteMillis == nil {
				t.Fatal("未保存图片首字")
			}
		})
	}
}
