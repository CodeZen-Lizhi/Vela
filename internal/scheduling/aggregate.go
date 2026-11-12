package scheduling

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

// boundedAggregate 保存同步聚合所需的协议字节；超过载荷上限时不向客户端提交不完整 JSON。
type boundedAggregate struct {
	// Buffer 保存已通过流式提交校验的原始事件，总量最多 128 MiB。
	bytes.Buffer
}

// Write 在真正分配内存前检查同步聚合载荷边界。
func (b *boundedAggregate) Write(p []byte) (int, error) {
	if len(p) > maxHTTPPayloadBytes-b.Len() {
		return 0, errors.New("同步聚合响应超过 128 MiB")
	}
	return b.Buffer.Write(p)
}

// aggregateSSE 复用真实流式终态校验，再按原协议聚合输出和用量；聚合期间没有客户端提交，允许安全回退。
func aggregateSSE(ctx context.Context, body io.Reader, w io.Writer, protocol string, result AttemptResult) (outcome AttemptResult, aggregateErr error) {
	defer func() {
		if aggregateErr != nil {
			outcome.Outcome.Success = false
			if !outcome.Committed {
				outcome.Retryable = true
				if outcome.Outcome.Penalty == Neutral {
					outcome.Outcome.Penalty = Heavy
				}
			}
		}
	}()
	var raw boundedAggregate
	result, err := forwardSSE(ctx, body, &raw, protocol, result)
	result.Committed = false
	if err != nil {
		result.Retryable = true
		return result, err
	}
	aggregate := streamAggregate{protocol: protocol, root: map[string]any{}, choices: map[int]map[string]any{}, blocks: map[int]map[string]any{}, candidates: map[int]map[string]any{}}
	scanner := bufio.NewScanner(bytes.NewReader(raw.Bytes()))
	scanner.Buffer(make([]byte, 4096), maxHTTPPayloadBytes)
	var data []string
	var eventName string
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		joined := strings.Join(data, "\n")
		data = nil
		if joined == "[DONE]" {
			return nil
		}
		var event map[string]any
		decoder := json.NewDecoder(strings.NewReader(joined))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			return err
		}
		if event["type"] == nil && eventName != "" {
			event["type"] = eventName
		}
		eventName = ""
		aggregate.observe(event)
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return result, err
			}
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if err := consume(); err != nil {
		return result, err
	}
	response, err := aggregate.finish()
	if err != nil {
		return result, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return result, err
	}
	if len(encoded) > maxHTTPPayloadBytes {
		result.Retryable = true
		return result, errors.New("同步聚合响应超过 128 MiB")
	}
	result.Committed = true
	result.Retryable = false
	_, err = w.Write(encoded)
	return result, err
}

// streamAggregate 保留四种协议自己的响应信封和索引，不做跨格式转换。
type streamAggregate struct {
	// protocol、root 是原格式与响应信封；其余映射分别按 wire 索引保存 choice、内容块和 candidate。
	protocol   string
	root       map[string]any
	choices    map[int]map[string]any
	blocks     map[int]map[string]any
	candidates map[int]map[string]any
	// audio 按 Chat choice 索引累计解码后的音频，收尾统一编码，避免各段 padding 截断后续字节。
	audio map[int][]byte
	// failure 保存上游内容结构错误，禁止交付被静默删改的聚合结果。
	failure error
}

// objectIndex 读取协议中的非负序号；缺省沿协议第一项处理。
func objectIndex(value any) int {
	if number, ok := value.(json.Number); ok {
		index, _ := number.Int64()
		return max(0, int(index))
	}
	index, _ := value.(float64)
	return max(0, int(index))
}

// copyFields 保留上游扩展字段，跳过由聚合器独立维护的数组和增量字段。
func copyFields(dst, src map[string]any, exclude ...string) {
	for key, value := range src {
		skip := false
		for _, name := range exclude {
			if key == name {
				skip = true
				break
			}
		}
		if !skip {
			dst[key] = value
		}
	}
}

// appendDelta 合并文本/工具参数的片段及按 index 编址的工具调用，保留未知扩展字段。
func appendDelta(dst, src map[string]any) {
	for key, value := range src {
		switch value := value.(type) {
		case string:
			if key == "content" || key == "refusal" || key == "reasoning_content" || key == "reasoning" || key == "arguments" || key == "input" || key == "text" || key == "thinking" || key == "signature" || key == "name" {
				previous, _ := dst[key].(string)
				dst[key] = previous + value
			} else {
				dst[key] = value
			}
		case map[string]any:
			target, _ := dst[key].(map[string]any)
			if target == nil {
				target = map[string]any{}
				dst[key] = target
			}
			appendDelta(target, value)
		case []any:
			if key != "tool_calls" {
				existing, _ := dst[key].([]any)
				dst[key] = append(existing, value...)
				continue
			}
			existing, _ := dst[key].([]any)
			for _, raw := range value {
				call, _ := raw.(map[string]any)
				index := objectIndex(call["index"])
				var target map[string]any
				for _, raw := range existing {
					item, _ := raw.(map[string]any)
					if objectIndex(item["index"]) == index {
						target = item
						break
					}
				}
				if target == nil {
					target = map[string]any{}
					existing = append(existing, target)
				}
				appendDelta(target, call)
			}
			dst[key] = existing
		default:
			dst[key] = value
		}
	}
}

// orderedObjects 恢复协议索引顺序，避免 map 遍历改变 choice、工具或内容顺序。
func orderedObjects(items map[int]map[string]any) []any {
	keys := make([]int, 0, len(items))
	for index := range items {
		keys = append(keys, index)
	}
	sort.Ints(keys)
	result := make([]any, 0, len(keys))
	for _, index := range keys {
		result = append(result, items[index])
	}
	return result
}

// observe 根据原协议累计消息、工具片段和最终用量，保留终态的完整快照。
func (a *streamAggregate) observe(event map[string]any) {
	switch a.protocol {
	case "responses", "openai:responses":
		a.observeResponses(event)
	case "anthropic", "claude:messages":
		a.observeAnthropic(event)
	case "gemini", "gemini:generate_content":
		copyFields(a.root, event, "candidates", "usageMetadata")
		if usage, ok := event["usageMetadata"].(map[string]any); ok {
			current, _ := a.root["usageMetadata"].(map[string]any)
			if current == nil {
				current = map[string]any{}
				a.root["usageMetadata"] = current
			}
			copyFields(current, usage)
		}
		candidates, _ := event["candidates"].([]any)
		for _, raw := range candidates {
			candidate, _ := raw.(map[string]any)
			index := objectIndex(candidate["index"])
			target := a.candidates[index]
			if target == nil {
				target = map[string]any{}
				a.candidates[index] = target
			}
			copyFields(target, candidate, "content")
			content, _ := candidate["content"].(map[string]any)
			if content != nil {
				current, _ := target["content"].(map[string]any)
				if current == nil {
					current = map[string]any{}
					target["content"] = current
				}
				copyFields(current, content, "parts")
				parts, _ := current["parts"].([]any)
				incoming, _ := content["parts"].([]any)
				current["parts"] = append(parts, incoming...)
			}
		}
	default:
		copyFields(a.root, event, "choices")
		choices, _ := event["choices"].([]any)
		for _, raw := range choices {
			choice, _ := raw.(map[string]any)
			index := objectIndex(choice["index"])
			target := a.choices[index]
			if target == nil {
				target = map[string]any{"index": index, "message": map[string]any{}}
				a.choices[index] = target
			}
			copyFields(target, choice, "delta", "message")
			message, _ := target["message"].(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if audio, ok := delta["audio"].(map[string]any); ok {
				a.observeChatAudio(index, message, audio)
				delete(delta, "audio")
			}
			appendDelta(message, delta)
			if snapshot, ok := choice["message"].(map[string]any); ok {
				target["message"] = snapshot
				delete(a.audio, index)
			}
		}
	}
}

// observeChatAudio 逐段解码并累计 Chat 音频，拼接转写且保留音频身份及扩展字段；非法编码使同步聚合失败。
func (a *streamAggregate) observeChatAudio(index int, message, delta map[string]any) {
	audio, _ := message["audio"].(map[string]any)
	if audio == nil {
		audio = map[string]any{}
		message["audio"] = audio
	}
	copyFields(audio, delta, "data", "transcript")
	if transcript, ok := delta["transcript"].(string); ok {
		previous, _ := audio["transcript"].(string)
		audio["transcript"] = previous + transcript
	}
	if raw, present := delta["data"]; present {
		encoded, ok := raw.(string)
		if !ok {
			a.failure = errors.New("上游音频段必须是 base64 字符串")
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			a.failure = errors.New("上游音频段 base64 编码无效")
			return
		}
		if a.audio == nil {
			a.audio = make(map[int][]byte)
		}
		a.audio[index] = append(a.audio[index], decoded...)
	}
}

// observeAnthropic 拼接 Messages 内容块，并把输入/输出用量及停止原因合回原 message。
func (a *streamAggregate) observeAnthropic(event map[string]any) {
	kind, _ := event["type"].(string)
	index := objectIndex(event["index"])
	switch kind {
	case "message_start":
		message, _ := event["message"].(map[string]any)
		copyFields(a.root, message, "content")
		content, _ := message["content"].([]any)
		for index, raw := range content {
			block, _ := raw.(map[string]any)
			a.blocks[index] = block
		}
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		a.blocks[index] = block
	case "content_block_delta":
		block := a.blocks[index]
		if block == nil {
			block = map[string]any{}
			a.blocks[index] = block
		}
		delta, _ := event["delta"].(map[string]any)
		if partial, ok := delta["partial_json"].(string); ok {
			old, _ := block["_partial_json"].(string)
			block["_partial_json"] = old + partial
		}
		for key, value := range delta {
			if key != "type" && key != "partial_json" {
				appendDelta(block, map[string]any{key: value})
			}
		}
	case "message_delta":
		delta, _ := event["delta"].(map[string]any)
		copyFields(a.root, delta)
		usage, _ := event["usage"].(map[string]any)
		current, _ := a.root["usage"].(map[string]any)
		if current == nil {
			current = map[string]any{}
			a.root["usage"] = current
		}
		copyFields(current, usage)
	}
}

// observeResponses 优先保留完整终态，同时收集无快照终态所需的输出项和文本/工具片段。
func (a *streamAggregate) observeResponses(event map[string]any) {
	kind, _ := event["type"].(string)
	if response, ok := event["response"].(map[string]any); ok {
		copyFields(a.root, response)
	}
	index := objectIndex(event["output_index"])
	switch kind {
	case "response.output_item.added", "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		a.blocks[index] = item
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.output_text.done", "response.refusal.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
		item := a.blocks[index]
		if item == nil {
			item = map[string]any{"type": "message", "role": "assistant"}
			a.blocks[index] = item
		}
		key, partType, field := "content", "output_text", "text"
		if strings.Contains(kind, "refusal") {
			partType = "refusal"
			field = "refusal"
		}
		if strings.Contains(kind, "reasoning") {
			item["type"] = "reasoning"
			delete(item, "role")
			partType = "reasoning_text"
		}
		if strings.Contains(kind, "summary") {
			key = "summary"
			partType = "summary_text"
		}
		partIndex := objectIndex(event["content_index"])
		if key == "summary" {
			partIndex = objectIndex(event["summary_index"])
		}
		parts, _ := item[key].([]any)
		// 内容块由协议按顺序创建；稀疏索引不预分配任意大的容器，非法结构留到收尾拒绝。
		if partIndex > len(parts) {
			a.failure = errors.New("上游内容块索引不连续")
			return
		}
		if partIndex == len(parts) {
			parts = append(parts, map[string]any{"type": partType})
		}
		part, _ := parts[partIndex].(map[string]any)
		if snapshot, ok := event["part"].(map[string]any); ok {
			parts[partIndex] = snapshot
		} else if strings.HasSuffix(kind, ".done") {
			part[field] = event[field]
		} else {
			appendDelta(part, map[string]any{field: event["delta"]})
		}
		item[key] = parts
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		item := a.blocks[index]
		if item == nil {
			item = map[string]any{}
			a.blocks[index] = item
		}
		field := "arguments"
		if strings.Contains(kind, "custom_tool_call_input") {
			field = "input"
		}
		if strings.HasSuffix(kind, ".done") {
			item[field] = event[field]
		} else {
			appendDelta(item, map[string]any{field: event["delta"]})
		}
	}
}

// finish 生成原协议同步信封并统一编码累积音频；工具块、完整转写和用量保持各自语义。
func (a *streamAggregate) finish() (map[string]any, error) {
	if a.failure != nil {
		return nil, a.failure
	}
	switch a.protocol {
	case "responses", "openai:responses":
		output, _ := a.root["output"].([]any)
		if len(output) == 0 {
			a.root["output"] = orderedObjects(a.blocks)
		}
	case "anthropic", "claude:messages":
		for _, block := range a.blocks {
			if partial, ok := block["_partial_json"].(string); ok {
				var input any
				decoder := json.NewDecoder(strings.NewReader(partial))
				decoder.UseNumber()
				if err := decoder.Decode(&input); err != nil {
					return nil, errors.New("上游工具参数 JSON 不完整")
				}
				block["input"] = input
				delete(block, "_partial_json")
			}
		}
		a.root["type"] = "message"
		a.root["content"] = orderedObjects(a.blocks)
	case "gemini", "gemini:generate_content":
		a.root["candidates"] = orderedObjects(a.candidates)
	default:
		for index, choice := range a.choices {
			message, _ := choice["message"].(map[string]any)
			if data, ok := a.audio[index]; ok {
				audio, _ := message["audio"].(map[string]any)
				audio["data"] = base64.StdEncoding.EncodeToString(data)
			}
			calls, _ := message["tool_calls"].([]any)
			sort.SliceStable(calls, func(i, j int) bool {
				left, _ := calls[i].(map[string]any)
				right, _ := calls[j].(map[string]any)
				return objectIndex(left["index"]) < objectIndex(right["index"])
			})
			for _, raw := range calls {
				call, _ := raw.(map[string]any)
				delete(call, "index")
			}
		}
		a.root["object"] = "chat.completion"
		a.root["choices"] = orderedObjects(a.choices)
	}
	return a.root, nil
}
