package scheduling

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// prepareRequest 仅整理同格式必要兼容：Responses 独立上下文与 Chat 流式用量；不转换协议或伪造工具标识。
func prepareRequest(body map[string]json.RawMessage, protocol string, stream bool) error {
	if protocol == "openai:responses" || protocol == "responses" || protocol == "openai:responses:compact" {
		return normalizeStandaloneOutputs(body)
	}
	if stream && (protocol == "openai:chat" || protocol == "chat") {
		var options map[string]json.RawMessage
		_ = json.Unmarshal(body["stream_options"], &options)
		if options == nil {
			options = map[string]json.RawMessage{}
		}
		options["include_usage"] = json.RawMessage("true")
		raw, err := json.Marshal(options)
		if err != nil {
			return err
		}
		body["stream_options"] = raw
	}
	return nil
}

// normalizeStandaloneOutputs 迁移 Aether 的独立通知归一化及配对顺序；未知形状和非法 call_id 原样交给上游校验。
func normalizeStandaloneOutputs(body map[string]json.RawMessage) error {
	var input []json.RawMessage
	if json.Unmarshal(body["input"], &input) != nil || input == nil {
		return nil
	}
	output := make([]json.RawMessage, 0, len(input))
	var pending []json.RawMessage
	inCalls, hadResults := false, false
	for _, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			output = append(output, pending...)
			pending = nil
			output = append(output, raw)
			inCalls = false
			continue
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		id, present := item["call_id"]
		var callID *string
		idErr := json.Unmarshal(id, &callID)
		emptyID := !present || idErr == nil && callID != nil && *callID == ""
		if (kind == "function_call_output" || kind == "custom_tool_call_output") && emptyID {
			var text string
			var parts []json.RawMessage
			if json.Unmarshal(item["output"], &text) == nil && string(item["output"]) != "null" {
				part, _ := json.Marshal(map[string]string{"type": "input_text", "text": text})
				parts = []json.RawMessage{part}
			} else if json.Unmarshal(item["output"], &parts) == nil && parts != nil {
				for i, part := range parts {
					var content map[string]json.RawMessage
					var contentKind string
					if json.Unmarshal(part, &content) == nil {
						_ = json.Unmarshal(content["type"], &contentKind)
					}
					if contentKind == "text" {
						content["type"] = json.RawMessage(`"input_text"`)
						parts[i], _ = json.Marshal(content)
					}
				}
			} else {
				output = append(output, raw)
				continue
			}
			message, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": parts})
			if err != nil {
				return err
			}
			if inCalls {
				pending = append(pending, message)
			} else {
				output = append(output, message)
			}
			continue
		}
		switch kind {
		case "function_call", "custom_tool_call", "tool_search_call":
			if hadResults {
				output = append(output, pending...)
				pending = nil
			}
			inCalls, hadResults = true, false
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			hadResults = true
		default:
			output = append(output, pending...)
			pending = nil
			inCalls = false
		}
		output = append(output, raw)
	}
	output = append(output, pending...)
	raw, err := json.Marshal(output)
	if err == nil {
		body["input"] = raw
	}
	return err
}

// prepareProviderRequest 仅在标准协议出站处恢复旧版 Gemini 与 Responses 历史兼容，不推断私有供应商 family。
func prepareProviderRequest(body map[string]json.RawMessage, req ExecutionRequest, c Candidate) error {
	switch req.Request.Protocol {
	case "gemini", "gemini:generate_content", "gemini:count_tokens":
		var value map[string]any
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		stripGeminiResponseIDs(value)
		tools, _ := value["tools"].([]any)
		functions, builtin := false, false
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			for key, raw := range tool {
				if key == "functionDeclarations" || key == "function_declarations" {
					items, _ := raw.([]any)
					functions = functions || len(items) > 0
				} else {
					builtin = true
				}
			}
		}
		if functions && builtin {
			config, ok := value["toolConfig"].(map[string]any)
			if !ok && value["toolConfig"] != nil {
				return errors.New("Gemini toolConfig 必须为对象")
			}
			if config == nil {
				config = map[string]any{}
			}
			delete(config, "include_server_side_tool_invocations")
			config["includeServerSideToolInvocations"] = true
			value["toolConfig"] = config
		}
		for key, value := range value {
			body[key], err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
	case "responses", "openai:responses", "openai:responses:compact":
		var input []json.RawMessage
		if json.Unmarshal(body["input"], &input) != nil || input == nil {
			return nil
		}
		providerURL, _ := url.Parse(c.Provider.URL)
		deepseek := providerURL != nil && strings.EqualFold(providerURL.Hostname(), "api.deepseek.com") && req.Request.Protocol != "openai:responses:compact" && !strings.HasSuffix(strings.Split(req.Path, "?")[0], "/compact")
		for _, raw := range input {
			var item map[string]any
			_ = json.Unmarshal(raw, &item)
			if item["type"] == "compaction_trigger" {
				deepseek = false
			}
		}
		output := make([]json.RawMessage, 0, len(input))
		for _, raw := range input {
			var item map[string]any
			_ = json.Unmarshal(raw, &item)
			if item["type"] != "reasoning" || replayableReasoning(item, deepseek) {
				output = append(output, raw)
			}
		}
		var err error
		body["input"], err = json.Marshal(output)
		return err
	}
	return nil
}

// stripGeminiResponseIDs 清理旧版上游拒绝的函数结果 ID，其余函数结果内容保持原样。
func stripGeminiResponseIDs(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "functionResponse" || key == "function_response" {
				if response, ok := child.(map[string]any); ok {
					delete(response, "id")
				}
			}
			stripGeminiResponseIDs(child)
		}
	case []any:
		for _, child := range value {
			stripGeminiResponseIDs(child)
		}
	}
}

// replayableReasoning 保留 OpenAI rs 引用与带密文的合成项；DeepSeek 普通续接额外允许无 ID 的不透明 reasoning_text。
func replayableReasoning(item map[string]any, deepseek bool) bool {
	id, isString := item["id"].(string)
	encrypted, _ := item["encrypted_content"].(string)
	if deepseek && (item["id"] == nil || isString && strings.TrimSpace(id) == "") && strings.TrimSpace(encrypted) != "" {
		parts, _ := item["content"].([]any)
		for _, raw := range parts {
			part, _ := raw.(map[string]any)
			_, text := part["text"].(string)
			if part["type"] == "reasoning_text" && text {
				return true
			}
		}
	}
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, "rs") && (!strings.HasPrefix(id, "rs_aether_") || strings.TrimSpace(encrypted) != "")
}
