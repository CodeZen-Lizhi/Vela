package scheduling

import "encoding/json"

// prepareRequest 仅整理同格式必要兼容：Responses 独立上下文与 Chat 流式用量；不转换协议或伪造工具标识。
func prepareRequest(body map[string]json.RawMessage, protocol string, stream bool) error {
	if protocol == "openai:responses" || protocol == "responses" {
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
