package records

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSSEAudioPrivacy 通过真实 HTTP 与 SQLite 验证多行及残缺音频事件不落库，转发字节与普通文本诊断保持完整。
func TestSSEAudioPrivacy(t *testing.T) {
	for _, shape := range []string{"multiline", "crlf", "malformed", "truncated"} {
		t.Run(shape, func(t *testing.T) {
			s, catalog := fixture(t)
			encodedAudio := "UFJJVkFURV9BVURJT19QQVlMT0FE"
			if shape == "truncated" {
				encodedAudio = strings.Repeat(encodedAudio, captureLimit/len(encodedAudio)+100)
			}
			audio := "event: audio\ndata: {\"choices\":[{\"delta\":{\"audio\":{\ndata: \"data\":\"" + encodedAudio + "\",\"transcript\":\"spoken words\"}}}]}\n\n"
			if shape == "malformed" {
				audio = "event: audio\ndata: {\"audio\":{\ndata: \"data\":\"" + encodedAudio + "\"\n\n"
			}
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"normal diagnostic\"}}]}\n\n" + audio + "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20}}\n\ndata: [DONE]\n\n"
			if shape == "crlf" {
				body = strings.ReplaceAll(body, "\n", "\r\n")
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, body)
			}))
			defer upstream.Close()
			ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:chat", Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(WithTarget(ctx, Target{KeyID: "audio"}), http.MethodPost, upstream.URL, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &Transport{}}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if _, err := io.Copy(Writer(ctx, &output), response.Body); err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if output.String() != body {
				t.Fatal("快照脱敏改变了实际转发字节")
			}
			if err := finish(nil); err != nil {
				t.Fatal(err)
			}
			run := ctx.Value(sessionKey{}).(*session)
			r, err := s.Get(context.Background(), run.record.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range []string{r.Client.ResponseBody, r.Attempts[0].Payload.ResponseBody} {
				if strings.Contains(saved, "UFJJVkFURV9BVURJT19QQVlMT0FE") || !strings.Contains(saved, "normal diagnostic") {
					t.Fatal("音频内容落库或正常文本诊断被丢弃")
				}
				if (shape == "multiline" || shape == "crlf") && !strings.Contains(saved, "spoken words") {
					t.Fatal("可安全解析事件中的转写文本丢失")
				}
			}
			if r.Usage == nil || r.Usage.Context != 100 || r.Usage.Output != 20 {
				t.Fatal("快照脱敏影响了真实用量采集")
			}
			var leaked int
			if err := catalog.DB().QueryRow(`SELECT COUNT(*) FROM request_payloads WHERE instr(document,'UFJJVkFURV9BVURJT19QQVlMT0FE')>0`).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatalf("Client或Attempt正文泄漏音频: count=%d err=%v", leaked, err)
			}
		})
	}
}
