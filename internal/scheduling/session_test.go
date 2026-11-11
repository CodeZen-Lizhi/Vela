package scheduling

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// TestSessionMigration 验证实际会话提取优先级及账号/agent隔离，不在测试里复制提取逻辑。
func TestSessionMigration(t *testing.T) {
	cases := []struct {
		// name、headers、body是原始请求信号，want是旧版身份含义的明确预期。
		name    string
		headers map[string]string
		body    string
		want    []string
	}{
		{"explicit", map[string]string{"User-Agent": "codex", "x-aether-session-id": " explicit ", "x-aether-agent-id": "agent", "session-id": "ignored"}, `{}`, []string{"codex", "", "explicit", "agent"}},
		{"codex-account", map[string]string{"chatgpt-account-id": "account-a", "thread-id": "thread"}, `{}`, []string{"codex", "account-a", "thread", ""}},
		{"claude-body-before-generic-header", map[string]string{"User-Agent": "claude-code", "session-id": "generic"}, `{"metadata":{"user_id":"user_a_session_claude"}}`, []string{"claude_code", "", "claude", ""}},
		{"opencode-agent", map[string]string{"x-opencode-session-id": "session", "x-opencode-agent-id": "agent-a"}, `{}`, []string{"opencode", "", "session", "agent-a"}},
		{"sdk", map[string]string{"User-Agent": "OpenAI/JS 1.0", "session_id": "sdk"}, `{}`, []string{"openai_js_sdk", "", "sdk", ""}},
		{"generic-body", nil, `{"metadata":{"session_id":"body","agentId":"worker"}}`, []string{"unknown", "", "body", "worker"}},
		{"no-signal", nil, `{"model":"same-model"}`, nil},
		{"empty-signal", map[string]string{"session-id": "  "}, `{"session_id":123}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			for k, v := range tc.headers {
				headers.Set(k, v)
			}
			got := SessionFromRequest("openai:chat", headers, []byte(tc.body))
			if tc.want == nil {
				if got != "" {
					t.Fatal(got)
				}
				return
			}
			var identity []string
			if err := json.Unmarshal([]byte(got), &identity); err != nil || !reflect.DeepEqual(identity, tc.want) {
				t.Fatalf("identity=%v %v expected=%v", identity, err, tc.want)
			}
		})
	}
	a := SessionFromRequest("openai:chat", http.Header{}, []byte(`{"session_id":"x;agent=y"}`))
	b := SessionFromRequest("openai:chat", http.Header{}, []byte(`{"session_id":"x","agent_id":"y"}`))
	if a == b {
		t.Fatal("组合身份发生分隔符碰撞")
	}
}

// TestSessionSearchAndWhitespace 保留Search专用family和trim后空Anthropic头的旧版语义。
func TestSessionSearchAndWhitespace(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-aether-session-id", "search-session")
	value := SessionFromRequest(" OpenAI:Search ", headers, []byte(`{}`))
	var got []string
	if err := json.Unmarshal([]byte(value), &got); err != nil || !reflect.DeepEqual(got, []string{"codex", "", "search-session", ""}) {
		t.Fatalf("Search identity: %v %v", got, err)
	}
	headers = http.Header{}
	headers.Set("User-Agent", "client/python 1.0")
	headers.Set("x-stainless-lang", "python")
	headers.Set("anthropic-version", " ")
	headers.Set("session_id", "sdk")
	value = SessionFromRequest("openai:chat", headers, []byte(`{}`))
	if err := json.Unmarshal([]byte(value), &got); err != nil || got[0] != "openai_python_sdk" {
		t.Fatalf("SDK空白头: %v %v", got, err)
	}
}
