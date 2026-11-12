package records

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBinaryCaptureMetadataOnly 通过真实 HTTP 下载检查字节完整、正文缓冲始终为空、SQLite 仅保留元信息。
func TestBinaryCaptureMetadataOnly(t *testing.T) {
	for _, contentType := range []string{"video/mp4", "audio/wav", "application/octet-stream", "application/pdf"} {
		t.Run(contentType, func(t *testing.T) {
			s, catalog := fixture(t)
			chunk := bytes.Repeat([]byte("\x00\xffPRIVATE_MEDIA_DOWNLOAD\x89"), 2048)
			const chunks = 96 // 约 5 MiB，显著超过正文快照上限，服务器逐块生成而非整体缓冲。
			want := sha256.New()
			for i := 0; i < chunks; i++ {
				want.Write(chunk)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				for i := 0; i < chunks; i++ {
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:video", Type: "video", ResourceQuery: true})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(WithTarget(ctx, Target{KeyID: "download"}), http.MethodGet, upstream.URL+"/v1/videos/job/content", nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &Transport{}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body := resp.Body.(*captureBody)
			got := sha256.New()
			output := Writer(ctx, got).(*responseWriter)
			var count int
			buffer := make([]byte, 32768)
			for {
				n, readErr := resp.Body.Read(buffer)
				if n > 0 {
					if _, err := output.Write(buffer[:n]); err != nil {
						t.Fatal(err)
					}
					count += n
				}
				if body.jsonBody.Cap() != 0 || body.raw.Cap() != 0 || len(body.pending) != 0 || output.raw.Cap() != 0 {
					t.Fatal("媒体字节仍进入上游或客户端正文缓冲")
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
			}
			if count != chunks*len(chunk) || !bytes.Equal(want.Sum(nil), got.Sum(nil)) {
				t.Fatal("二进制下载字节被改变或截断")
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if err := finish(nil); err != nil {
				t.Fatal(err)
			}
			run := ctx.Value(sessionKey{}).(*session)
			r, err := s.Get(context.Background(), run.record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Client.ResponseBody != omittedResponseBody || r.Attempts[0].Payload.ResponseBody != omittedResponseBody || !strings.Contains(r.Attempts[0].Payload.ResponseHeaders, contentType) {
				t.Fatalf("媒体快照未明确只保留元信息: %+v", r)
			}
			var leaked int
			if err := catalog.DB().QueryRow(`SELECT COUNT(*) FROM request_payloads WHERE instr(document,'PRIVATE_MEDIA_DOWNLOAD')>0`).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatalf("媒体内容泄漏到SQLite: count=%d err=%v", leaked, err)
			}
		})
	}
}

// TestResourceQueryDoesNotBill 验证独立 GET/DELETE 不重算历史生成用量及每秒或每请求价格，长期累计也保持零消耗。
func TestResourceQueryDoesNotBill(t *testing.T) {
	for _, tc := range []struct {
		// kind、method 为入口确认的资源操作；body 可能回显历史生成的秒数或 Token。
		kind, method, body string
	}{
		{"video", http.MethodGet, `{"status":"completed","seconds":"8","size":"720p"}`},
		{"video", http.MethodDelete, `{"status":"completed","seconds":"8","size":"720p"}`},
		{"files", http.MethodGet, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`},
		{"files", http.MethodDelete, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`},
		{"interactions", http.MethodGet, `{"usage":{"input_tokens":100,"output_tokens":20}}`},
		{"interactions", http.MethodDelete, `{"usage":{"input_tokens":100,"output_tokens":20}}`},
	} {
		t.Run(tc.kind+tc.method, func(t *testing.T) {
			s, catalog := fixture(t)
			if _, err := catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.pricing.base',json('[{"key":"per_second","price":0.1,"unit":"second"},{"key":"per_request","price":2,"unit":"request"}]'))`); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:" + tc.kind, Type: tc.kind, ResourceQuery: true})
			if err != nil {
				t.Fatal(err)
			}
			one := 1.0
			req, err := http.NewRequestWithContext(WithTarget(ctx, Target{KeyID: "query", Multiplier: &one}), tc.method, upstream.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &Transport{}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body := resp.Body.(*captureBody)
			if _, err := io.Copy(Writer(ctx, io.Discard), resp.Body); err != nil {
				t.Fatal(err)
			}
			if body.jsonBody.Cap() != 0 {
				t.Fatal("纯资源查询仍全量缓冲JSON以重算生成用量")
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if err := finish(nil); err != nil {
				t.Fatal(err)
			}
			run := ctx.Value(sessionKey{}).(*session)
			r, err := s.Get(context.Background(), run.record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Usage == nil || *r.Usage != (Usage{}) || r.Cost == nil || *r.Cost != 0 || r.UnknownCosts != 0 || len(r.Charges) != 0 || r.Attempts[0].Cost == nil || *r.Attempts[0].Cost != 0 {
				t.Fatalf("资源查询重复产生模型消耗: %+v", r)
			}
			encoded, err := json.Marshal(r)
			if err != nil || strings.Contains(string(encoded), "resourceQuery") || strings.Contains(string(encoded), "ResourceQuery") {
				t.Fatal("内部资源标记扩大公开记录契约", err)
			}
			var cost float64
			var unknown int
			if err := catalog.DB().QueryRow(`SELECT SUM(cost),SUM(unknown_usage+unknown_cost) FROM request_totals`).Scan(&cost, &unknown); err != nil || cost != 0 || unknown != 0 {
				t.Fatalf("资源查询污染长期累计: cost=%v unknown=%v err=%v", cost, unknown, err)
			}
			keys, err := s.KeyTotals(context.Background(), Query{})
			if err != nil || len(keys) != 1 || keys[0].Tokens != 0 || keys[0].Cost != 0 || keys[0].UnknownCost != 0 || keys[0].UnknownUsage != 0 {
				t.Fatalf("资源查询污染Key累计: %+v %v", keys, err)
			}
		})
	}
}
