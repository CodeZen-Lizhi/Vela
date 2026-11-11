package scheduling

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SessionFromRequest 沿旧客户端适配器优先级识别会话，返回无歧义的family/account/session/agent组合。
// 不识别时返回空，不按模型或网关Key把所有无会话请求合并；返回值不用于日志或外部头部。
func SessionFromRequest(protocol string, headers http.Header, body []byte) string {
	var document map[string]any
	_ = json.Unmarshal(body, &document)
	families := sessionFamilies(headers)
	family := "unknown"
	if len(families) > 0 {
		family = families[0]
	} else {
		family = fingerprintFamily(headers)
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "openai:search" {
		if id := firstHeader(headers, "x-aether-session-id"); id != "" {
			return sessionIdentity("codex", "", id, firstHeader(headers, "x-aether-agent-id"))
		}
		if id := sessionValue(document, "id"); id != "" {
			return sessionIdentity("codex", firstHeader(headers, "chatgpt-account-id"), id, "")
		}
	}
	if id := firstHeader(headers, "x-aether-session-id"); id != "" {
		return sessionIdentity(family, "", id, firstHeader(headers, "x-aether-agent-id"))
	}
	if value := specificSession(family, headers, document); value != "" {
		return value
	}
	if id, agent := genericSession(headers, document); id != "" {
		return sessionIdentity(family, "", id, agent)
	}
	for _, other := range families {
		if other != family {
			if value := specificSession(other, headers, document); value != "" {
				return value
			}
		}
	}
	return ""
}

// sessionIdentity 使用JSON数组编码身份，避免session内容中的分隔符造成跨账号或agent碰撞。
func sessionIdentity(family, account, id, agent string) string {
	data, _ := json.Marshal([]string{family, account, id, agent})
	return string(data)
}

// firstHeader 按旧版给定顺序取首个非空文本，不对凭据头做任何读取。
func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// sessionValue 读取对象路径上的非空字符串，不把数字或复合对象转成虚构身份。
func sessionValue(document map[string]any, path ...string) string {
	var value any = document
	for _, name := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[name]
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// genericSession 保留通用header与body的先后顺序；只有body来源附带body中的agent。
func genericSession(headers http.Header, body map[string]any) (string, string) {
	if value := firstHeader(headers, "session-id", "thread-id", "session_id", "conversation_id"); value != "" {
		return value, ""
	}
	for _, path := range [][]string{{"prompt_cache_key"}, {"conversation_id"}, {"conversationId"}, {"session_id"}, {"sessionId"}, {"metadata", "session_id"}, {"metadata", "conversation_id"}, {"conversationState", "conversationId"}, {"conversationState", "sessionId"}} {
		if id := sessionValue(body, path...); id != "" {
			agent := ""
			for _, a := range [][]string{{"agent_id"}, {"agentId"}, {"metadata", "agent_id"}, {"metadata", "agentId"}, {"conversationState", "agentId"}} {
				if agent = sessionValue(body, a...); agent != "" {
					break
				}
			}
			return id, agent
		}
	}
	return "", ""
}

// specificSession 保留Claude专用body先于通用header等特殊规则，不简化成全局header优先。
func specificSession(family string, headers http.Header, body map[string]any) string {
	var id, agent, account string
	switch family {
	case "codex":
		account = firstHeader(headers, "chatgpt-account-id")
		id = firstHeader(headers, "session-id", "thread-id", "session_id", "conversation_id")
		if id == "" {
			id, agent = genericSession(headers, body)
		}
	case "claude_code":
		id = firstHeader(headers, "x-claude-code-session-id", "session_id", "conversation_id")
		if id == "" {
			user := sessionValue(body, "metadata", "user_id")
			if i := strings.LastIndex(user, "_session_"); i >= 0 {
				id = strings.TrimSpace(user[i+len("_session_"):])
			}
		}
		if id == "" {
			id, agent = genericSession(headers, body)
		}
	case "opencode":
		id = firstHeader(headers, "x-opencode-session-id", "session_id")
		agent = firstHeader(headers, "x-opencode-agent-id")
	case "qwen_code", "roo_code", "kilocode", "cherrystudio", "openui", "anthropic_js_sdk", "anthropic_python_sdk", "openai_js_sdk", "openai_python_sdk":
		id = firstHeader(headers, "session_id", "conversation_id")
		if id == "" {
			id, agent = genericSession(headers, body)
		}
	}
	if id == "" {
		return ""
	}
	return sessionIdentity(family, account, id, agent)
}

// sessionFamilies 依旧版12种专用适配器的固定顺序检测所有匹配项，以便主适配器失败时继续回退。
func sessionFamilies(headers http.Header) []string {
	ua := strings.ToLower(headers.Get("User-Agent"))
	origin := strings.ToLower(headers.Get("Originator"))
	lang := strings.ToLower(headers.Get("x-stainless-lang"))
	anthropic := firstHeader(headers, "anthropic-version") != ""
	hasPrefix := func(prefix string) bool {
		for name := range headers {
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				return true
			}
		}
		return false
	}
	// family与match分别是稳定客户端名称和旧版对应的检测条件，顺序不可随意交换。
	adapters := []struct {
		family string
		match  bool
	}{
		{"codex", strings.Contains(ua, "codex") || strings.Contains(origin, "codex") || firstHeader(headers, "chatgpt-account-id") != ""},
		{"claude_code", strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude code") || firstHeader(headers, "x-claude-code-session-id") != ""},
		{"opencode", strings.Contains(ua, "opencode") || firstHeader(headers, "x-opencode-session-id") != ""},
		{"qwen_code", strings.Contains(ua, "qwencode") || strings.Contains(ua, "qwen-code") || strings.Contains(strings.ToLower(headers.Get("x-dashscope-useragent")), "qwencode") || strings.Contains(strings.ToLower(headers.Get("x-dashscope-useragent")), "qwen-code")},
		{"roo_code", strings.Contains(ua, "roo-code") || strings.Contains(ua, "roocode") || strings.Contains(origin, "roo-code") || strings.Contains(origin, "roocode")},
		{"kilocode", strings.Contains(ua, "kilo-code") || strings.Contains(ua, "kilocode") || hasPrefix("x-kilocode-") || firstHeader(headers, "x-kilo-directory", "x-kilo-workspace") != ""},
		{"cherrystudio", strings.Contains(ua, "cherrystudio") || strings.Contains(ua, "cherry-studio") || strings.Contains(ua, "cherry studio")},
		{"openui", strings.Contains(ua, "openui")},
		{"anthropic_js_sdk", strings.Contains(ua, "anthropic/js") || (strings.Contains(ua, "/js ") && strings.Contains(lang, "js") && anthropic)},
		{"anthropic_python_sdk", strings.Contains(ua, "anthropic/python") || (strings.Contains(ua, "/python ") && strings.Contains(lang, "python") && anthropic)},
		{"openai_js_sdk", strings.Contains(ua, "openai/js") || (strings.Contains(ua, "/js ") && strings.Contains(lang, "js"))},
		{"openai_python_sdk", strings.Contains(ua, "openai/python") || (strings.Contains(ua, "/python ") && strings.Contains(lang, "python") && !anthropic)},
	}
	result := []string{}
	for _, adapter := range adapters {
		if adapter.match {
			result = append(result, adapter.family)
		}
	}
	return result
}

// fingerprintFamily 只有专用检测均未命中时使用已知客户端指纹，不凭空建立会话。
func fingerprintFamily(headers http.Header) string {
	ua := strings.ToLower(headers.Get("User-Agent"))
	// token与family分别是旧版识别文本与稳定分组名。
	fingerprints := [][2]string{{"geminicli", "gemini_cli"}, {"gemini-cli", "gemini_cli"}, {"cursor", "cursor"}, {"windsurf", "windsurf"}, {"continue", "continue"}, {"cline", "cline"}, {"aider", "aider"}, {"langchain", "langchain"}, {"llamaindex", "llamaindex"}, {"llama-index", "llamaindex"}}
	for _, pair := range fingerprints {
		if strings.Contains(ua, pair[0]) {
			return pair[1]
		}
	}
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "x-stainless-") {
			return "sdk"
		}
	}
	return "unknown"
}
