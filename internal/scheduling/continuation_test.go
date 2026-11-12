package scheduling

import (
	"crypto/sha256"
	"strconv"
	"testing"
	"time"
)

// TestContinuationIdentityAndExpiry 验证所有权必须同时匹配请求主体、协议、模型、地址及凭据，过期与冲突均拒绝续接。
func TestContinuationIdentityAndExpiry(t *testing.T) {
	req := ExecutionRequest{Request: Request{Model: "logical", Protocol: "openai:responses"}, Principal: "principal", PreviousResponseID: "resp-1"}
	candidate := runtimeCandidate("a", 0, 0, nil)
	candidate.Provider.URL = "https://upstream.invalid"
	for _, change := range []string{"principal", "protocol", "logical", "url", "credential", "mapping", "expired", "collision"} {
		t.Run(change, func(t *testing.T) {
			store := NewContinuations()
			store.remember(req, candidate, "resp-1")
			if len(store.resolve(req, []Candidate{candidate})) != 1 {
				t.Fatal("成功原绑定无法续接")
			}
			changedReq, changedCandidate := req, candidate
			switch change {
			case "principal":
				changedReq.Principal = "other"
			case "protocol":
				changedReq.Request.Protocol = "openai:chat"
			case "logical":
				changedReq.Request.Model = "other"
			case "url":
				changedCandidate.Provider.URL = "https://other.invalid"
			case "credential":
				changedCandidate.CredentialVersion = "new-version"
			case "mapping":
				changedCandidate.Model = "other"
			case "expired":
				key := sha256.Sum256([]byte(req.Principal + "\x00resp-1"))
				binding := store.entries[key]
				binding.expires = time.Now().Add(-time.Second)
				store.entries[key] = binding
			case "collision":
				other := runtimeCandidate("b", 1, 0, nil)
				store.remember(req, other, "resp-1")
			}
			if len(store.resolve(changedReq, []Candidate{changedCandidate})) != 0 {
				t.Fatal("变化后错误续接原绑定")
			}
		})
	}
}

// TestContinuationCapacity 验证长时间运行的所有权表仍有界，满表更新已有记录不额外淘汰其他绑定。
func TestContinuationCapacity(t *testing.T) {
	store := NewContinuations()
	req := ExecutionRequest{Request: Request{Model: "m", Protocol: "openai:responses"}}
	candidate := runtimeCandidate("a", 0, 0, nil)
	for i := 0; i < 1024; i++ {
		store.remember(req, candidate, "resp-"+strconv.Itoa(i))
	}
	store.remember(req, candidate, "resp-1023")
	if len(store.entries) != 1024 {
		t.Fatal("更新已有绑定不应淘汰其他记录")
	}
	store.remember(req, candidate, "resp-new")
	req.PreviousResponseID = "resp-0"
	if len(store.entries) != 1024 || len(store.resolve(req, []Candidate{candidate})) != 0 {
		t.Fatal("容量边界未淘汰最旧绑定")
	}
}
