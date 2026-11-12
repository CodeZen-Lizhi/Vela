package scheduling

import (
	"encoding/json"
)

// nonemptyText 判断协议中的实际文字或标识，空串不算有效模型输出，保留模型实际返回的空白文字。
func nonemptyText(value any) bool { text, _ := value.(string); return text != "" }

// outputParts 按协议识别文字、拒绝、推理及音频内容，不把空容器当成模型输出。
func outputParts(value any) bool {
	if nonemptyText(value) {
		return true
	}
	parts, _ := value.([]any)
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		for _, key := range []string{"text", "refusal", "thinking", "data"} {
			if nonemptyText(part[key]) {
				return true
			}
		}
	}
	return false
}

// chatOutput 识别 Chat 同步消息或增量中的内容和工具状态；空 tool_calls 不构成输出。
func chatOutput(message map[string]any) bool {
	for _, key := range []string{"content", "refusal", "reasoning_content", "reasoning"} {
		if outputParts(message[key]) {
			return true
		}
	}
	audio, _ := message["audio"].(map[string]any)
	if nonemptyText(audio["data"]) || nonemptyText(audio["id"]) {
		return true
	}
	call, _ := message["function_call"].(map[string]any)
	if nonemptyText(call["name"]) || nonemptyText(call["arguments"]) {
		return true
	}
	calls, _ := message["tool_calls"].([]any)
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		function, _ := call["function"].(map[string]any)
		custom, _ := call["custom"].(map[string]any)
		if nonemptyText(function["name"]) || nonemptyText(function["arguments"]) || nonemptyText(custom["name"]) || nonemptyText(custom["input"]) {
			return true
		}
	}
	return false
}

// responsesItemOutput 沿用 Aether 的有效输出识别，涵盖文本、拒绝、推理、压缩和工具调用结果。
func responsesItemOutput(item map[string]any) bool {
	switch item["type"] {
	case "message":
		return outputParts(item["content"])
	case "reasoning", "compaction":
		return outputParts(item["summary"]) || outputParts(item["content"]) || nonemptyText(item["encrypted_content"])
	case "function_call":
		return nonemptyText(item["name"]) && nonemptyText(item["arguments"])
	case "custom_tool_call":
		_, hasInput := item["input"].(string)
		return nonemptyText(item["name"]) && hasInput
	case "image_generation_call":
		return nonemptyText(item["result"])
	case "computer_call":
		action, _ := item["action"].(map[string]any)
		return nonemptyText(item["call_id"]) && nonemptyText(action["type"])
	case "web_search_call", "file_search_call", "code_interpreter_call", "local_shell_call", "shell_call", "apply_patch_call", "mcp_call", "mcp_list_tools", "mcp_approval_request", "tool_search_call":
		return nonemptyText(item["id"]) && (nonemptyText(item["status"]) || item["action"] != nil || item["queries"] != nil || item["code"] != nil || item["tools"] != nil || nonemptyText(item["name"]))
	}
	return false
}

// responsesEnvelope 分开判断 Responses 合法终态与有效内容，允许流式已输出而终态 output 为空。
func responsesEnvelope(raw []byte) (terminal, useful bool) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["error"] != nil {
		return false, false
	}
	output, ok := body["output"].([]any)
	terminal = ok && (body["status"] == "completed" || body["status"] == "incomplete")
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		useful = useful || responsesItemOutput(item)
	}
	return
}

// responsesEventOutput 按 Aether 的已知输出事件识别内容，准备事件或未知事件中的同名字段不冒充有效生成。
func responsesEventOutput(kind string, body map[string]any) bool {
	switch kind {
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.refusal.delta":
		return nonemptyText(body["delta"])
	case "response.output_text.done", "response.reasoning_text.done", "response.reasoning_summary_text.done", "response.refusal.done", "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part, _ := body["part"].(map[string]any)
		return nonemptyText(body["text"]) || nonemptyText(body["refusal"]) || outputParts([]any{part})
	case "response.function_call_arguments.done":
		return nonemptyText(body["arguments"])
	case "response.custom_tool_call_input.done":
		return nonemptyText(body["input"])
	case "response.output_item.added", "response.output_item.done":
		item, _ := body["item"].(map[string]any)
		return responsesItemOutput(item)
	}
	return false
}

// anthropicToolOutput 识别客户端工具和服务端工具调用、工具结果及不透明推理块，空容器不算内容。
func anthropicToolOutput(item map[string]any) bool {
	kind, _ := item["type"].(string)
	switch kind {
	case "tool_use", "server_tool_use", "mcp_tool_use":
		return nonemptyText(item["name"])
	case "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result", "mcp_tool_result", "tool_search_tool_result":
		return nonemptyText(item["tool_use_id"]) && item["content"] != nil
	case "redacted_thinking":
		return nonemptyText(item["data"])
	}
	return false
}

// geminiPartOutput 识别 Gemini 原生文本、工具与多模态输出，空对象不作为有效生成。
func geminiPartOutput(part map[string]any) bool {
	if nonemptyText(part["text"]) {
		return true
	}
	for _, key := range []string{"functionCall", "functionResponse", "inlineData", "fileData", "executableCode", "codeExecutionResult"} {
		value, _ := part[key].(map[string]any)
		if len(value) > 0 {
			return true
		}
	}
	return false
}
