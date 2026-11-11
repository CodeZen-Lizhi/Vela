package fingerprint

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBundledModels 验证真实目录身份、数据匹配及返回切片的隔离。
func TestBundledModels(t *testing.T) {
	d, err := LoadBundled()
	if err != nil {
		t.Fatal(err)
	}
	models := d.Models()
	if len(models) != len(d.detector.modelIDs) || len(models) == 0 {
		t.Fatal("missing reference models")
	}
	for i, m := range models {
		if m.ID != d.detector.modelIDs[i] || m.DisplayName == "" || m.Family == "" {
			t.Fatalf("invalid reference model: %+v", m)
		}
	}
	first := models[0].ID
	models[0].ID = "changed"
	if d.ReferenceModels()[0].ID != first {
		t.Fatal("Models exposes shared state")
	}
}

// TestRequestSampleInjectedClientAndCancellation 通过真实 HTTP 验证客户端注入和上游取消传递。
func TestRequestSampleInjectedClientAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan Sample, 1)
	go func() {
		result <- RequestSample(ctx, DetectConfig{BaseURL: server.URL, APIKey: "test", Model: "test", HTTPClient: server.Client(), Timeout: time.Second}, Challenge{ExpectedCount: 300, Prompt: "numbers"})
	}()
	select {
	case <-entered:
	case sample := <-result:
		t.Fatalf("injected TLS client not used: %+v", sample)
	case <-time.After(3 * time.Second):
		t.Fatal("request not dispatched")
	}
	cancel()
	select {
	case sample := <-result:
		if sample.State != "cancelled" {
			t.Fatalf("state=%s", sample.State)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sample ignored cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("server request ignored cancellation")
	}
}

// TestGenerateChallenges 验证沿用上游题目范围、不同计数及输入约束。
func TestGenerateChallenges(t *testing.T) {
	challenges, err := GenerateChallenges(3)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int]bool{}
	for _, c := range challenges {
		if c.ID == "" || c.Prompt == "" || c.ExpectedCount < 292 || c.ExpectedCount > 332 || counts[c.ExpectedCount] {
			t.Fatalf("invalid challenge: %+v", c)
		}
		counts[c.ExpectedCount] = true
	}
	if _, err := GenerateChallenges(0); err == nil {
		t.Fatal("invalid count accepted")
	}
}
