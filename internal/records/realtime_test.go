package records

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRealtimeCaptureUsageAndPrivacy 验证连接内多轮用量累加、终态重读去重、取消结算以及音频和事件正文不落库。
func TestRealtimeCaptureUsageAndPrivacy(t *testing.T) {
	s, catalog := fixture(t)
	ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:realtime", Type: "realtime", Stream: true, Transport: "websocket", Body: []byte(`{"model":"logical"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithTarget(ctx, Target{KeyID: "realtime-key"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/v1/realtime?model=upstream-model&trace=PRIVATE_QUERY", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-secret")
	observe, closeAttempt, err := PrepareRealtimeCapture(request)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"type":"response.done","response":{"id":"r1","output":[{"audio":"PRIVATE_AUDIO","transcript":"PRIVATE_TEXT"}],"usage":{"input_tokens":100,"output_tokens":20,"input_token_details":{"cached_tokens":40,"audio_tokens":10},"output_token_details":{"audio_tokens":5}}}}`)
	observe([]byte(`{"type":"response.audio.delta","delta":"PRIVATE_AUDIO"}`))
	observe([]byte(`{"type":"response.created","response":{"id":"r1","usage":{"input_tokens":999,"output_tokens":999}}}`))
	observe(first)
	observe([]byte(`{"type":"response.done","response":{"id":"r2","usage":{"input_tokens":50,"output_tokens":10,"input_token_details":{"cached_tokens":0,"audio_tokens":4},"output_token_details":{"audio_tokens":2}}}}`))
	observe(first)
	if err := closeAttempt(http.StatusSwitchingProtocols, context.Canceled); err != nil {
		t.Fatal(err)
	}
	if err := closeAttempt(0, nil); err != nil {
		t.Fatal(err)
	}
	if err := finish(context.Canceled); err != nil {
		t.Fatal(err)
	}
	run := ctx.Value(sessionKey{}).(*session)
	r, err := s.Get(context.Background(), run.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "realtime" || r.Protocol != "openai:realtime" || r.UpstreamModel != "upstream-model" || r.State != "failed" || len(r.Attempts) != 1 {
		t.Fatalf("连接结算错误: %+v", r)
	}
	if r.Usage == nil || r.Usage.Input != 110 || r.Usage.Context != 150 || r.Usage.Output != 30 || r.Usage.CacheRead != 40 || r.Usage.AudioInput != 14 || r.Usage.AudioOutput != 7 {
		t.Fatalf("多轮用量未去重累加: %+v", r.Usage)
	}
	if r.Attempts[0].Status != 101 || r.Attempts[0].Error != context.Canceled.Error() || r.Attempts[0].Payload.ResponseBody != "" {
		t.Fatalf("尝试状态或正文不符: %+v", r.Attempts[0])
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "test-secret") {
		t.Fatal("Realtime 事件正文或凭据泄漏")
	}
	var count int
	if err := catalog.DB().QueryRow(`SELECT SUM(requests) FROM request_totals`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("重复连接结算: count=%d err=%v", count, err)
	}
}

// TestRealtimeStreamCapturePrivacy 验证复用内部 SSE 形状采集时仍去重累计，完整音频事件只转发不落库。
func TestRealtimeStreamCapturePrivacy(t *testing.T) {
	s, _ := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.audio.delta\",\"delta\":\"PRIVATE_AUDIO\"}\n\n")
		for _, id := range []string{"r1", "r2", "r1"} {
			io.WriteString(w, "data: {\"type\":\"response.done\",\"response\":{\"id\":\""+id+"\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}\n\n")
		}
	}))
	defer upstream.Close()
	r, _ := exchange(t, s, upstream.URL, "openai:realtime", true)
	if r.Usage == nil || r.Usage.Input != 200 || r.Usage.Context != 200 || r.Usage.Output != 40 || r.Attempts[0].Payload.ResponseBody != "" {
		t.Fatalf("内部事件流未去重或泄漏正文: %+v", r)
	}
}

// TestRealtimeHandshakeFailureKeepsUsage 验证未升级握手保留未知值，同时不抹去后续成功连接的实际用量。
func TestRealtimeHandshakeFailureKeepsUsage(t *testing.T) {
	for _, status := range []int{0, http.StatusServiceUnavailable} {
		s, _ := fixture(t)
		ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "openai:realtime", Type: "realtime", Stream: true, Transport: "websocket"})
		if err != nil {
			t.Fatal(err)
		}
		for i, code := range []int{status, http.StatusSwitchingProtocols} {
			attemptCtx := WithTarget(ctx, Target{KeyID: []string{"failed-handshake", "connected"}[i]})
			request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, "https://example.test/v1/realtime", nil)
			if err != nil {
				t.Fatal(err)
			}
			observe, closeAttempt, err := PrepareRealtimeCapture(request)
			if err != nil {
				t.Fatal(err)
			}
			var cause error
			if i == 0 {
				cause = errors.New("上游握手失败")
			} else {
				observe([]byte(`{"type":"response.done","response":{"id":"r1","usage":{"input_tokens":100,"output_tokens":20,"input_token_details":{"cached_tokens":40}}}}`))
			}
			if err := closeAttempt(code, cause); err != nil {
				t.Fatal(err)
			}
		}
		if err := finish(nil); err != nil {
			t.Fatal(err)
		}
		run := ctx.Value(sessionKey{}).(*session)
		r, err := s.Get(context.Background(), run.record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.State != "success" || len(r.Attempts) != 2 || r.Attempts[0].Status != status || r.Attempts[0].Usage != nil || r.Attempts[0].Error == "" {
			t.Fatalf("握手失败被伪造为零用量或丢失: %+v", r)
		}
		if r.Usage == nil || r.Usage.Context != 100 || r.Usage.Input != 60 || r.Usage.Output != 20 || r.Usage.CacheRead != 40 {
			t.Fatalf("握手失败抹去实际连接用量: %+v", r.Usage)
		}
	}
}
