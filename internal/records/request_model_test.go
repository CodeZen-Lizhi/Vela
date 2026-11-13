package records

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLargeRequestUpstreamModel 验证大请求仍记录实际出站模型，原请求完整转发且诊断快照继续受限。
func TestLargeRequestUpstreamModel(t *testing.T) {
	padding := strings.Repeat("x", captureLimit+256)
	bodies := map[string]string{
		"模型在截断点前": `{"model":"actual-upstream-model","input":"` + padding + `"}`,
		"模型在截断点后": `{"input":"` + padding + `","model":"actual-upstream-model"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != body {
					t.Error("采集改变了实际转发正文", err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"usage":{"input_tokens":100,"output_tokens":20}}`)
			}))
			defer upstream.Close()
			ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "responses", Type: "chat", Source: "business", Body: []byte(`{"model":"logical"}`)})
			if err != nil {
				t.Fatal(err)
			}
			one := 1.0
			req, err := http.NewRequestWithContext(WithTarget(ctx, Target{KeyID: "test-key", Multiplier: &one}), http.MethodPost, upstream.URL+"/v1/responses", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Transport: &Transport{Base: http.DefaultTransport}}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if err = finish(nil); err != nil {
				t.Fatal(err)
			}
			page, err := s.Query(context.Background(), Query{})
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("列表查询失败: %v", err)
			}
			row := page.Records[0]
			if row.UpstreamModel != "actual-upstream-model" {
				t.Errorf("上游模型丢失: got %q", row.UpstreamModel)
			}
			detail, err := s.Get(context.Background(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.UpstreamModel != "actual-upstream-model" {
				t.Errorf("详情上游模型丢失: got %q", detail.UpstreamModel)
			}
			if len(detail.Attempts) != 1 || detail.Attempts[0].Payload == nil {
				t.Fatal("缺少出站诊断")
			}
			payload := detail.Attempts[0].Payload
			if payload.BodyState != "truncated" || len(payload.RequestBody) > captureLimit+len("\n[正文已截断]") {
				t.Fatalf("快照未保留截断边界: %s / %d", payload.BodyState, len(payload.RequestBody))
			}
		})
	}
}
