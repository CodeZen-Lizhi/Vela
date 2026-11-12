package modeltest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// transportResult 是单个固定 Key 与模型的诊断；快照脱敏且最多 32 KiB，Error 不包含凭据。
type transportResult struct {
	// Status 为最后收到的 HTTP 状态；FirstByteMillis 从首次发送前计时，无响应时为零。
	Status          int
	FirstByteMillis int64
	// RequestBody、RequestHeaders 保留初始提交；ResponseBody 保留最后一次响应。
	RequestBody    string
	RequestHeaders string
	ResponseBody   string
	// Error 为空才表示协议验证完成；取消保留 context 错误以供 Manager 分类。
	Error error
}

// runHTTP 执行独立非流式诊断；client 已配置出口与禁止重定向，ctx 管理整个提交及轮询期限。
func runHTTP(ctx context.Context, client *http.Client, request Request, baseURL, upstreamModel, secret string) (result transportResult) {
	redact := secretRedactor(secret)
	defer func() {
		result.RequestBody = diagnosticSnapshot(redact(result.RequestBody))
		result.RequestHeaders = diagnosticSnapshot(redact(result.RequestHeaders))
		result.ResponseBody = diagnosticSnapshot(redact(result.ResponseBody))
		if result.Error != nil && !errors.Is(result.Error, context.Canceled) && !errors.Is(result.Error, context.DeadlineExceeded) {
			result.Error = errors.New(redact(result.Error.Error()))
		}
	}()
	if client == nil {
		result.Error = errors.New("诊断 HTTP 客户端未初始化")
		return
	}
	endpoint, body, err := diagnosticRequest(request, baseURL, upstreamModel)
	if err != nil {
		result.Error = err
		return
	}
	headers, err := diagnosticHeaders(request.RequestHeaders)
	if err != nil {
		result.Error = err
		return
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	switch request.Protocol {
	case "anthropic":
		headers.Set("X-Api-Key", secret)
		if headers.Get("Anthropic-Version") == "" {
			headers.Set("Anthropic-Version", "2023-06-01")
		}
	case "gemini":
		headers.Set("X-Goog-Api-Key", secret)
	default:
		headers.Set("Authorization", "Bearer "+secret)
	}
	wire := body
	if request.Protocol == "video" {
		wire, err = videoForm(body, headers)
		if err != nil {
			result.Error = err
			return
		}
	}
	result.RequestBody = string(body)
	safeHeaders := headers.Clone()
	for _, key := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		if safeHeaders.Get(key) != "" {
			safeHeaders.Set(key, "[REDACTED]")
		}
	}
	encodedHeaders, err := json.Marshal(safeHeaders)
	if err != nil {
		result.Error = errors.New("无法生成请求头快照")
		return
	}
	result.RequestHeaders = string(encodedHeaders)
	start := time.Now()
	var firstByte atomic.Int64
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { firstByte.CompareAndSwap(0, max(1, time.Since(start).Nanoseconds())) }}
	ctx = httptrace.WithClientTrace(ctx, trace)
	method := http.MethodPost
	jobID := ""
	for {
		response, status, callErr := diagnosticExchange(ctx, client, method, endpoint, wire, headers, request.firstByteSeconds)
		result.Status = status
		result.FirstByteMillis = firstByte.Load() / int64(time.Millisecond)
		result.ResponseBody = string(response)
		if callErr != nil {
			result.Error = callErr
			return
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(response, &payload) != nil || payload == nil {
			result.Error = errors.New("上游未返回有效 JSON 对象")
			return
		}
		if value, exists := payload["error"]; exists && string(value) != "null" {
			result.Error = errors.New("上游返回错误信封，请查看脱敏响应")
			return
		}
		if request.Protocol != "video" {
			if !validDiagnosticTerminal(request.Protocol, payload) {
				result.Error = errors.New("上游缺少有效模型终态")
			}
			return
		}
		id := jsonString(payload["id"])
		state := jsonString(payload["status"])
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") || (jobID != "" && id != jobID) {
			result.Error = errors.New("视频响应缺少有效或一致的任务 ID")
			return
		}
		switch state {
		case "completed":
			return
		case "queued", "in_progress":
		default:
			result.Error = errors.New("视频任务失败或返回未知状态")
			return
		}
		if jobID == "" {
			jobID = id
			endpoint += "/" + url.PathEscape(id)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.Error = ctx.Err()
			return
		case <-timer.C:
		}
		method, wire = http.MethodGet, nil
	}
}

// validateRequest 在遍历候选前校验用户输入；不发请求，也不依赖具体供应商与 Key。
func validateRequest(request Request) error {
	_, body, err := diagnosticRequest(request, "https://diagnostic.invalid", "validation-model")
	if err != nil {
		return err
	}
	headers, err := diagnosticHeaders(request.RequestHeaders)
	if err != nil {
		return err
	}
	if request.Protocol == "video" {
		_, err = videoForm(body, headers)
	}
	return err
}

// diagnosticRequest 校验 JSON 对象并固定协议、实际模型和非流式语义，不让正文覆盖路由。
func diagnosticRequest(request Request, baseURL, model string) (string, []byte, error) {
	const requestLimit = 1 << 20 // 诊断请求正文最大 1 MiB。
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", nil, errors.New("供应商地址必须是无认证、查询参数和片段的 HTTP(S) 地址")
	}
	if strings.TrimSpace(model) == "" {
		return "", nil, errors.New("实际请求模型不能为空")
	}
	message := request.Message
	if strings.TrimSpace(message) == "" {
		message = "Hello"
	}
	var body map[string]any
	var path string
	switch request.Protocol {
	case "chat":
		path = "/v1/chat/completions"
		body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": message}}}
	case "responses":
		path = "/v1/responses"
		body = map[string]any{"input": message}
	case "anthropic":
		path = "/v1/messages"
		body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": message}}, "max_tokens": 64}
	case "gemini":
		path = "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent"
		body = map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": message}}}}}
	case "image":
		path = "/v1/images/generations"
		body = map[string]any{"prompt": message, "n": 1}
	case "embedding":
		path = "/v1/embeddings"
		body = map[string]any{"input": message}
	case "rerank":
		path = "/v1/rerank"
		body = map[string]any{"query": message, "documents": []string{message}}
	case "video":
		path = "/v1/videos"
		body = map[string]any{"prompt": message}
	default:
		return "", nil, errors.New("不支持的诊断协议")
	}
	if len(request.RequestBody) > requestLimit {
		return "", nil, errors.New("请求正文超过 1 MiB")
	}
	if strings.TrimSpace(request.RequestBody) != "" {
		body = nil
		decoder := json.NewDecoder(strings.NewReader(request.RequestBody))
		decoder.UseNumber()
		if decoder.Decode(&body) != nil || body == nil {
			return "", nil, errors.New("请求正文必须是 JSON 对象")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return "", nil, errors.New("请求正文包含多余 JSON 内容")
		}
	}
	delete(body, "stream")
	if request.Protocol == "gemini" {
		delete(body, "model")
	} else {
		body["model"] = model
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, errors.New("无法编码请求正文")
	}
	if len(data) > requestLimit {
		return "", nil, errors.New("请求正文超过 1 MiB")
	}
	// 用户地址可以包含网关前缀或协议版本，但版本段不重复拼接。
	prefix := strings.TrimRight(base.String(), "/")
	for _, version := range []string{"/v1", "/v1beta"} {
		if strings.HasSuffix(prefix, version) && strings.HasPrefix(path, version+"/") {
			prefix = strings.TrimSuffix(prefix, version)
			break
		}
	}
	return prefix + path, data, nil
}

// diagnosticHeaders 接收字符串字典，拒绝认证与传输控制头以及非法 HTTP 字符。
func diagnosticHeaders(raw string) (http.Header, error) {
	headers := make(http.Header)
	if strings.TrimSpace(raw) == "" {
		return headers, nil
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("自定义请求头超过 1 MiB")
	}
	var entries map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &entries) != nil || entries == nil {
		return nil, errors.New("请求头必须是 JSON 字符串字典")
	}
	for name, encoded := range entries {
		var value string
		if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) || json.Unmarshal(encoded, &value) != nil {
			return nil, errors.New("请求头值必须是字符串")
		}
		lower := strings.ToLower(name)
		switch lower {
		case "host", "cookie", "cookie2", "set-cookie", "connection", "content-length", "transfer-encoding", "trailer", "te", "upgrade", "expect", "accept-encoding", "content-type", "accept":
			return nil, errors.New("禁止覆盖认证或传输请求头")
		}
		if strings.Contains(lower, "auth") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") || strings.HasPrefix(lower, "proxy-") || strings.HasPrefix(lower, "sec-") {
			return nil, errors.New("禁止覆盖认证或传输请求头")
		}
		if name == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("请求头包含非法字符")
		}
		for _, c := range []byte(name) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
				return nil, errors.New("请求头名称无效")
			}
		}
		for _, c := range []byte(value) {
			if c < 32 && c != '\t' || c == 127 {
				return nil, errors.New("请求头值无效")
			}
		}
		if _, exists := headers[http.CanonicalHeaderKey(name)]; exists {
			return nil, errors.New("请求头名称重复")
		}
		headers.Set(name, value)
	}
	return headers, nil
}

// videoForm 按 Videos API 使用 multipart 提交 JSON 配置中的标量字段，不接受文件路径或嵌套对象。
func videoForm(data []byte, headers http.Header) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, errors.New("视频配置无效")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, raw := range body {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("视频配置不支持 null 字段")
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			var number json.Number
			if json.Unmarshal(raw, &number) != nil || string(raw) == "null" {
				return nil, errors.New("视频配置只支持字符串或数字字段")
			}
			value = number.String()
		}
		if err := writer.WriteField(key, value); err != nil {
			return nil, errors.New("无法编码视频请求")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, errors.New("无法完成视频请求")
	}
	if buffer.Len() > 1<<20 {
		return nil, errors.New("请求正文超过 1 MiB")
	}
	headers.Set("Content-Type", writer.FormDataContentType())
	return buffer.Bytes(), nil
}

// diagnosticExchange 对每次网络调用限制首次响应期限，收到首字节后仅受总体 ctx 约束；保留最多 4 MiB 的完整响应供验证。
func diagnosticExchange(ctx context.Context, client *http.Client, method, endpoint string, body []byte, headers http.Header, firstByteSeconds int) ([]byte, int, error) {
	if firstByteSeconds <= 0 {
		firstByteSeconds = 30
	}
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(time.Duration(firstByteSeconds)*time.Second, func() { cancel(errors.New("上游首次响应超时")) })
	defer timer.Stop()
	attemptCtx = httptrace.WithClientTrace(attemptCtx, &httptrace.ClientTrace{GotFirstResponseByte: func() { timer.Stop() }})
	call, err := http.NewRequestWithContext(attemptCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("无法创建诊断 HTTP 请求")
	}
	call.Header = headers.Clone()
	response, err := client.Do(call)
	if err != nil {
		if attemptCtx.Err() != nil {
			return nil, 0, context.Cause(attemptCtx)
		}
		return nil, 0, errors.New("诊断 HTTP 连接失败")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		if attemptCtx.Err() != nil {
			return data, response.StatusCode, context.Cause(attemptCtx)
		}
		return data, response.StatusCode, errors.New("读取诊断响应失败")
	}
	if len(data) > 4<<20 {
		return data, response.StatusCode, errors.New("诊断响应超过 4 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return data, response.StatusCode, fmt.Errorf("上游返回 HTTP %d", response.StatusCode)
	}
	return data, response.StatusCode, nil
}

// jsonString 读取协议中的字符串字段，缺失或非字符串时为空。
func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// validDiagnosticTerminal 验证完整响应中的实际输出与协议终态，HTTP 成功或空输出不代表测试成功。
func validDiagnosticTerminal(protocol string, body map[string]json.RawMessage) bool {
	switch protocol {
	case "chat":
		var choices []struct {
			// Message、FinishReason 为非流式输出和结束原因。
			Message      map[string]json.RawMessage `json:"message"`
			FinishReason string                     `json:"finish_reason"`
		}
		if json.Unmarshal(body["choices"], &choices) != nil || len(choices) == 0 {
			return false
		}
		for _, c := range choices {
			switch c.FinishReason {
			case "stop", "length", "tool_calls", "function_call":
			default:
				return false
			}
			if jsonString(c.Message["content"]) == "" && !nonemptyJSONArray(c.Message["tool_calls"]) && !nonemptyJSONObject(c.Message["function_call"]) {
				return false
			}
		}
		return true
	case "responses":
		return jsonString(body["status"]) == "completed" && diagnosticContent(body["output"], true)
	case "anthropic":
		switch jsonString(body["stop_reason"]) {
		case "end_turn", "max_tokens", "stop_sequence", "tool_use":
		default:
			return false
		}
		return jsonString(body["type"]) == "message" && diagnosticContent(body["content"], false)
	case "gemini":
		var candidates []struct {
			// FinishReason、Content 是 Gemini 终态与内容块。
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []map[string]json.RawMessage `json:"parts"` /* Parts 保存文本或工具调用。 */
			} `json:"content"`
		}
		if json.Unmarshal(body["candidates"], &candidates) != nil {
			return false
		}
		for _, c := range candidates {
			if c.FinishReason != "STOP" && c.FinishReason != "MAX_TOKENS" {
				continue
			}
			for _, part := range c.Content.Parts {
				if jsonString(part["text"]) != "" || nonemptyJSONObject(part["functionCall"]) {
					return true
				}
			}
		}
	case "image":
		var data []map[string]json.RawMessage
		if json.Unmarshal(body["data"], &data) != nil || len(data) == 0 {
			return false
		}
		for _, item := range data {
			if jsonString(item["url"]) == "" && jsonString(item["b64_json"]) == "" {
				return false
			}
		}
		return true
	case "embedding":
		var data []struct {
			Embedding []float64 `json:"embedding"` /* Embedding 是返回的数值向量。 */
		}
		if json.Unmarshal(body["data"], &data) != nil || len(data) == 0 {
			return false
		}
		for _, item := range data {
			if len(item.Embedding) == 0 {
				return false
			}
		}
		return true
	case "rerank":
		var results []struct {
			// Index、RelevanceScore 必须存在，分别表示输入文档位置和排序得分。
			Index          *int     `json:"index"`
			RelevanceScore *float64 `json:"relevance_score"`
		}
		if json.Unmarshal(body["results"], &results) != nil || len(results) == 0 {
			return false
		}
		for _, item := range results {
			if item.Index == nil || *item.Index < 0 || item.RelevanceScore == nil {
				return false
			}
		}
		return true
	}
	return false
}

// diagnosticContent 确认 Responses 或 Anthropic 返回可识别的文本、拒绝说明或工具调用。
func diagnosticContent(raw json.RawMessage, responses bool) bool {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		switch jsonString(block["type"]) {
		case "text", "output_text":
			if jsonString(block["text"]) != "" {
				return true
			}
		case "refusal":
			if jsonString(block["refusal"]) != "" {
				return true
			}
		case "message":
			if responses && diagnosticContent(block["content"], false) {
				return true
			}
		case "function_call", "tool_use":
			if jsonString(block["name"]) != "" {
				return true
			}
		}
	}
	return false
}

// nonemptyJSONArray 确认字段是至少包含一个非空对象的 JSON 数组。
func nonemptyJSONArray(raw json.RawMessage) bool {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !nonemptyJSONObject(value) {
			return false
		}
	}
	return true
}

// nonemptyJSONObject 拒绝 null、空对象以及不同 JSON 类型。
func nonemptyJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) > 0
}

// secretRedactor 在截断前覆盖明文、URL、JSON 与 Base64 编码的实际凭据，避免部分凭据落入快照。
func secretRedactor(secret string) func(string) string {
	if secret == "" {
		return func(value string) string { return value }
	}
	encoded, _ := json.Marshal(secret)
	quoted := string(encoded[1 : len(encoded)-1])
	var unicode strings.Builder
	for _, c := range []byte(secret) {
		fmt.Fprintf(&unicode, "\\u%04x", c)
	}
	variants := []string{secret, quoted, strings.ReplaceAll(quoted, "/", "\\/"), url.QueryEscape(secret), url.PathEscape(secret), unicode.String()}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		variants = append(variants, encoding.EncodeToString([]byte(secret)))
	}
	for _, variant := range append([]string(nil), variants...) {
		encoded, _ := json.Marshal(variant)
		variants = append(variants, string(encoded[1:len(encoded)-1]))
	}
	// 长形式先替换，避免明文替换后破坏其更长编码形式。
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	return func(value string) string {
		for _, variant := range variants {
			if variant != "" {
				value = strings.ReplaceAll(value, variant, "[REDACTED]")
			}
		}
		return value
	}
}

// diagnosticSnapshot 以 UTF-8 边界截断脱敏内容，整个快照不超过 32 KiB。
func diagnosticSnapshot(value string) string {
	const limit = 32 << 10   // 单项诊断快照上限为 32 KiB。
	const marker = "\n[已截断]" // 明确标记快照不是完整响应。
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= limit {
		return value
	}
	end := limit - len(marker)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + marker
}
