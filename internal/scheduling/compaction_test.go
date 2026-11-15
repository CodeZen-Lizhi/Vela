package scheduling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// TestCompactionIdentificationAndWait 验证只识别原生标记，默认 20 分钟且显式配置优先。
func TestCompactionIdentificationAndWait(t *testing.T) {
	for _, tc := range []struct {
		// protocol、body 是实际协议与正文，want 为是否应采用压缩执行策略。
		protocol, body string
		want           bool
	}{
		{"openai:responses:compact", `{}`, true},
		{"openai:responses", `{"input":[{"type":"compaction_trigger"}]}`, true},
		{"openai:responses", `{"input":[{"type":"message","content":"compaction_trigger"}]}`, false},
		{"openai:chat", `{"input":[{"type":"compaction_trigger"}]}`, false},
	} {
		if got := isCompactionRequest(ExecutionRequest{Request: Request{Protocol: tc.protocol}, Body: []byte(tc.body)}); got != tc.want {
			t.Fatalf("识别错误: protocol=%s got=%v", tc.protocol, got)
		}
	}
	c := runtimeCandidate("a", 0, 0, nil)
	if got := compactionWaitSeconds(c); got != 1200 {
		t.Fatal("压缩默认不是20分钟", got)
	}
	c.Provider.Request.RequestSeconds = 7
	if got := compactionWaitSeconds(c); got != 7 {
		t.Fatal("未保留显式超时", got)
	}
}

// TestCompactExplicitTimeoutAndCancel 验证非流式压缩尊重显式期限和上游调用方取消，取消不会归为供应商故障。
func TestCompactExplicitTimeoutAndCancel(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					return
				}
				if canceled {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			runner := NewHTTPRunner()
			defer runner.Close()
			c := Candidate{Provider: providers.Provider{URL: upstream.URL, Request: providers.RequestPolicy{RequestSeconds: 1}}, Model: "m"}
			var out bytes.Buffer
			start := time.Now()
			result, err := runner.Run(ctx, c, ExecutionRequest{Request: Request{Protocol: "openai:responses:compact"}, Path: "/v1/responses/compact", Body: []byte(`{"input":[]}`)}, "test-key", &out)
			if err == nil || result.Committed || out.Len() != 0 {
				t.Fatalf("超时/取消仍提交压缩: result=%+v err=%v", result, err)
			}
			if canceled {
				if !errors.Is(err, context.Canceled) || result.Outcome.Penalty != Neutral {
					t.Fatalf("取消错误归类: result=%+v err=%v", result, err)
				}
			} else if result.Status != 504 || result.Outcome.Penalty != Light || time.Since(start) > 2*time.Second {
				t.Fatalf("显式压缩期限未生效: result=%+v err=%v elapsed=%s", result, err, time.Since(start))
			}
		})
	}
}

// TestNonStreamCompactionTriggerRequiresComplete 验证非流式 Responses 压缩触发请求只接受完整检查点。
func TestNonStreamCompactionTriggerRequiresComplete(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		for _, status := range []string{"completed", "incomplete"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if mode == "sse" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response."+status+"\",\"response\":{\"id\":\"resp-sealed\",\"status\":\""+status+"\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"sealed\"}]}}\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"resp-sealed","status":"`+status+`","output":[{"type":"compaction","encrypted_content":"sealed"}]}`)
				}))
				defer upstream.Close()
				runner := NewHTTPRunner()
				defer runner.Close()
				var out bytes.Buffer
				result, err := runner.Run(context.Background(), Candidate{Provider: providers.Provider{URL: upstream.URL}, Model: "m"}, ExecutionRequest{Request: Request{Protocol: "openai:responses"}, Path: "/v1/responses", Body: []byte(`{"input":[{"type":"compaction_trigger"}]}`)}, "test-key", &out)
				if (err == nil) != (status == "completed") || result.Outcome.Success != (status == "completed") || (out.Len() > 0) != (status == "completed") {
					t.Fatalf("非流式压缩完整性错误: result=%+v err=%v", result, err)
				}
			})
		}
	}
}

// TestCompactionBindingIdentity 验证已知摘要的主体、凭据与地址约束；外部或过期摘要不假定所属目标。
func TestCompactionBindingIdentity(t *testing.T) {
	c := runtimeCandidate("a", 0, 0, nil)
	c.Provider.URL = "https://upstream.invalid"
	ref := compactionReference(map[string]any{"type": "compaction", "encrypted_content": "sealed"})
	for _, change := range []string{"none", "key", "credential", "url", "principal", "unknown", "expired"} {
		t.Run(change, func(t *testing.T) {
			store := NewContinuations()
			req := ExecutionRequest{Request: Request{Protocol: "openai:responses", Model: "m"}, Principal: "p", Body: []byte(`{"input":[{"type":"compaction","encrypted_content":"sealed"}]}`)}
			store.remember(req, c, ref)
			other := c
			switch change {
			case "key":
				other.Key.ID = "other"
			case "credential":
				other.CredentialVersion = "new"
			case "url":
				other.Provider.URL = "https://other.invalid"
			case "principal":
				req.Principal = "other"
			case "unknown":
				req.Body = []byte(`{"input":[{"type":"compaction","encrypted_content":"foreign"}]}`)
			case "expired":
				key := sha256.Sum256([]byte(req.Principal + "\x00" + ref))
				binding := store.entries[key]
				binding.expires = time.Now().Add(-time.Second)
				store.entries[key] = binding
			}
			candidates, bound := store.resolveCompactions(req, []Candidate{other})
			wantBound := change != "principal" && change != "unknown" && change != "expired"
			wantCandidate := !wantBound || change == "none"
			if bound != wantBound || (len(candidates) == 1) != wantCandidate {
				t.Fatalf("归属结果错误: bound=%v candidates=%d", bound, len(candidates))
			}
		})
	}
}

// TestCompactionStreamRejectsPartial 验证有密文但无完整终态、incomplete 或只有普通文本均不能输出压缩成功。
func TestCompactionStreamRejectsPartial(t *testing.T) {
	for _, ending := range []string{"", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[]}}\n\n"} {
		body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"partial\"}}\n\n" + ending
		var out strings.Builder
		result, err := forwardEventStream(context.Background(), strings.NewReader(body), &out, "openai:responses", AttemptResult{}, streamTerminal{compaction: true})
		if err == nil || result.Committed || !result.Retryable || out.Len() != 0 || len(result.compactions) != 0 {
			t.Fatalf("残缺检查点被提交: result=%+v err=%v bytes=%d", result, err, out.Len())
		}
	}
}
