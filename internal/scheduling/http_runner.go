package scheduling

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/outbound"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// HTTPRunner 是同格式 HTTP/SSE 执行适配器；不会自行重试或转换 API 格式。
type HTTPRunner struct {
	// Client 是不跟随重定向的复用客户端；ClientFor 可按候选出口覆盖它。
	Client *http.Client
	// ClientFor 解析供应商、Key 与全局代理优先级，返回可复用客户端。
	ClientFor func(context.Context, Candidate) (*http.Client, error)
}

// NewHTTPRunner 创建默认直连、禁止携带凭据跟随重定向的客户端。
func NewHTTPRunner() *HTTPRunner {
	return &HTTPRunner{Client: &http.Client{Transport: outbound.Transport(nil), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Close 释放默认客户端的空闲连接，不取消已经在执行的请求。
func (r *HTTPRunner) Close() {
	if r.Client != nil {
		r.Client.CloseIdleConnections()
	}
}

// Run 完成同格式请求兼容、模型映射与上游认证；SSE 逐事件验证，提交后失败不透明重放。
func (r *HTTPRunner) Run(ctx context.Context, c Candidate, req ExecutionRequest, secret string, w io.Writer) (AttemptResult, error) {
	var result AttemptResult
	switch req.Request.Protocol {
	case "openai:chat", "chat", "openai:responses", "responses", "claude:messages", "anthropic", "gemini:generate_content", "gemini":
	default:
		return result, errors.New("此HTTP适配器只接收已确认的四种聊天协议")
	}
	parsed, err := url.Parse(req.Path)
	if err != nil || !strings.HasPrefix(req.Path, "/") || parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(req.Path, "//") || parsed.Fragment != "" {
		return result, errors.New("上游请求路径无效")
	}
	var body map[string]json.RawMessage
	if err = json.Unmarshal(req.Body, &body); err != nil || body == nil {
		return result, errors.New("调用正文必须是 JSON 对象")
	}
	if err = prepareRequest(body, req.Request.Protocol, req.Stream); err != nil {
		return result, err
	}
	body["model"], err = json.Marshal(c.Model)
	if err != nil {
		return result, err
	}
	if req.Request.Protocol == "gemini:generate_content" || req.Request.Protocol == "gemini" {
		delete(body, "model")
	}
	if req.WebSocket != nil {
		body["type"] = json.RawMessage(`"response.create"`)
		delete(body, "stream")
		delete(body, "background")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !req.Stream {
		seconds := c.Provider.Request.RequestSeconds
		if seconds == 0 {
			seconds = 300
		}
		bounded, boundedCancel := context.WithTimeout(attemptCtx, time.Duration(seconds)*time.Second)
		defer boundedCancel()
		attemptCtx = bounded
	}
	call, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, strings.TrimRight(c.Provider.URL, "/")+upstreamPath(req, c.Model), bytes.NewReader(data))
	if err != nil {
		return result, errors.New("供应商地址无效")
	}
	call.Header.Set("Content-Type", "application/json")
	for _, name := range []string{"Accept", "Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if value := req.Headers.Get(name); value != "" {
			call.Header.Set(name, value)
		}
	}
	switch req.Request.Protocol {
	case "anthropic", "claude:messages":
		call.Header.Set("x-api-key", secret)
		if call.Header.Get("Anthropic-Version") == "" {
			call.Header.Set("Anthropic-Version", "2023-06-01")
		}
	case "gemini", "gemini:generate_content":
		call.Header.Set("x-goog-api-key", secret)
	default:
		call.Header.Set("Authorization", "Bearer "+secret)
	}
	client := r.Client
	if r.ClientFor != nil {
		client, err = r.ClientFor(ctx, c)
		if err != nil {
			return result, err
		}
	}
	if client == nil {
		return result, errors.New("上游 HTTP 客户端尚未初始化")
	}
	if req.WebSocket != nil {
		return r.runWebSocket(ctx, c, req, call, client, w)
	}
	first := c.Provider.Request.FirstByteSeconds
	if first == 0 {
		first = 30
	}
	// 首次响应以最终上游头为界，正文读取继续受逻辑请求期限约束。
	var timer *time.Timer
	if req.Stream {
		timer = time.AfterFunc(time.Duration(first)*time.Second, cancel)
	}
	response, err := client.Do(call)
	stopped := true
	if timer != nil {
		stopped = timer.Stop()
	}
	if err != nil {
		var local *records.LocalError
		if errors.As(err, &local) {
			result.Status = 500
			return result, errors.New("本地请求记录准备失败，未发送上游")
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Retryable = true
		result.Outcome.Penalty = Heavy
		if !stopped || errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			result.Outcome.Penalty = Light
			result.Status = 504
		}
		return result, errors.New("上游连接或首次响应失败")
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if readErr != nil {
			return result, errors.New("读取上游错误响应失败")
		}
		result.Outcome = classifyHTTP(response.StatusCode, data)
		result.Outcome.RetryAfter, result.Outcome.RetryAfterSet = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		result.Retryable = result.Outcome.Penalty == Light || result.Outcome.Penalty == Heavy
		return result, fmt.Errorf("上游返回状态 %d", response.StatusCode)
	}
	if req.Stream {
		if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			result.Outcome.Penalty = Heavy
			result.Retryable = true
			return result, errors.New("上游未返回有效 SSE 响应")
		}
		return forwardSSE(ctx, response.Body, w, req.Request.Protocol, result)
	}
	// 非流式 JSON 必须完整解析后才提交，限制只作用于非流式响应，不截断长流输出。
	data, err = io.ReadAll(io.LimitReader(response.Body, 32*1024*1024+1))
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Outcome.Penalty = Heavy
		if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			result.Outcome.Penalty = Light
			result.Status = 504
		}
		result.Retryable = true
		return result, errors.New("读取上游响应失败")
	}
	if len(data) > 32*1024*1024 {
		return result, errors.New("非流式响应超过 32 MiB")
	}
	if !validJSONTerminal(req.Request.Protocol, data) {
		result.Outcome = classifyHTTP(200, data)
		if result.Outcome.Penalty == Neutral {
			result.Outcome.Penalty = Heavy
		}
		result.Retryable = true
		return result, errors.New("上游缺少有效模型终态")
	}
	var responseObject struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &responseObject)
	result.ResponseID = responseObject.ID
	result.Committed = true
	if _, err = w.Write(data); err != nil {
		return result, err
	}
	result.Outcome.Success = true
	return result, nil
}

// classifyHTTP 只读取顶层错误信封，不把生成文字中的错误码当作凭据失效证明。
func classifyHTTP(status int, data []byte) Outcome {
	var body struct {
		Error *struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	if body.Error != nil {
		code := body.Error.Code
		if code == "" {
			code = body.Error.Type
		}
		switch code {
		case "rate_limit_exceeded", "rate_limit_error", "concurrency_limit_exceeded", "too_many_concurrent_requests", "overloaded_error", "server_overloaded":
			return Outcome{Penalty: Light, RateLimited: true}
		}
	}
	switch {
	case status == 429:
		return Outcome{Penalty: Light, RateLimited: true}
	case status == 408 || status == 503 || status == 504:
		return Outcome{Penalty: Light}
	case status >= 500 || status >= 401 && status <= 403 || status >= 200 && status < 300 && body.Error != nil:
		return Outcome{Penalty: Heavy}
	}
	return Outcome{}
}

// retryAfter 支持秒数与 HTTP 日期；无效、不为正的值不建立冷却。
func retryAfter(value string, now time.Time) time.Duration {
	duration, _ := parseRetryAfter(value, now)
	return duration
}

// parseRetryAfter 区分有效零秒与缺失/无效值，不对显式零秒另加本地退避。
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 600)) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, min(date.Sub(now), 600*time.Second)), true
	}
	return 0, false
}

// validJSONTerminal 确认完整同格式终态；HTTP 200、空对象、错误体与只有响应头都不回健康分。
func validJSONTerminal(protocol string, data []byte) bool {
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return false
	}
	if value, ok := body["error"]; ok && string(value) != "null" {
		return false
	}
	switch protocol {
	case "openai:responses", "responses":
		terminal, useful := responsesEnvelope(data)
		return terminal && useful
	case "anthropic", "claude:messages":
		var stop *string
		_ = json.Unmarshal(body["stop_reason"], &stop)
		var kind string
		_ = json.Unmarshal(body["type"], &kind)
		var content []any
		_ = json.Unmarshal(body["content"], &content)
		useful := outputParts(content)
		for _, raw := range content {
			item, _ := raw.(map[string]any)
			useful = useful || item["type"] == "tool_use" && nonemptyText(item["name"])
		}
		return kind == "message" && stop != nil && *stop != "" && useful
	case "gemini", "gemini:generate_content":
		var candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      *struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		}
		if json.Unmarshal(body["candidates"], &candidates) != nil {
			return false
		}
		for _, c := range candidates {
			if c.Content != nil {
				for _, part := range c.Content.Parts {
					if text, ok := part["text"].(string); ok && text != "" {
						return true
					}
					if part["functionCall"] != nil {
						return true
					}
				}
			}
		}
		return false
	default:
		var choices []struct {
			FinishReason *string         `json:"finish_reason"`
			Message      json.RawMessage `json:"message"`
		}
		if json.Unmarshal(body["choices"], &choices) != nil || len(choices) == 0 {
			return false
		}
		useful := false
		for _, c := range choices {
			var message map[string]any
			_ = json.Unmarshal(c.Message, &message)
			useful = useful || chatOutput(message)
			if c.FinishReason == nil || *c.FinishReason == "" || !bytes.HasPrefix(bytes.TrimSpace(c.Message), []byte("{")) {
				return false
			}
		}
		return useful
	}
}

// streamTerminal 记录流式协议终态与有效输出；首个非空模型或工具增量之后不得切目标。
type streamTerminal struct {
	// finished、done、failed 分别记录协议结束、Chat DONE 标志与错误事件。
	finished bool
	done     bool
	failed   bool
	// useful 与协议终态、提交边界独立；仅 DONE 或空 completed 不算完整模型成功。
	useful bool
	// responseID 是上游 Responses 返回的状态标识，只在完整成功后交给所有权注册表。
	responseID string
	// commitSetup 使 WebSocket response.created 的公开状态立即成为不可重放边界；allowEmpty 仅用于明确 generate:false 的预热。
	commitSetup bool
	allowEmpty  bool
}

// observe 按协议识别终态，返回此事件是否已包含不可替换的模型/工具状态。
func (s *streamTerminal) observe(protocol, event, data string) bool {
	if data == "[DONE]" {
		if protocol == "openai:chat" || protocol == "chat" {
			s.finished = true
		}
		s.done = true
		return false
	}
	var body map[string]any
	if json.Unmarshal([]byte(data), &body) != nil {
		s.failed = true
		return false
	}
	if body["error"] != nil || event == "error" {
		s.failed = true
		return false
	}
	switch protocol {
	case "openai:responses", "responses":
		kind, _ := body["type"].(string)
		if kind == "" {
			kind = event
		}
		if response, ok := body["response"].(map[string]any); ok {
			if id, ok := response["id"].(string); ok && id != "" {
				s.responseID = id
			}
		}
		if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" {
			response, ok := body["response"]
			if ok {
				raw, _ := json.Marshal(response)
				terminal, useful := responsesEnvelope(raw)
				s.useful = s.useful || useful
				s.finished = terminal
				s.done = s.finished
			}
			if !s.finished {
				s.failed = true
			}
		}
		if kind == "response.failed" {
			s.failed = true
		}
		if s.commitSetup && kind == "response.cancelled" {
			s.failed = true
			s.done = true
		}
		useful := responsesEventOutput(kind, body)
		s.useful = s.useful || useful
		return useful || s.commitSetup && s.responseID != "" && (kind == "response.created" || kind == "response.in_progress") || kind == "response.output_item.added" || kind == "response.output_item.done"
	case "anthropic", "claude:messages":
		kind, _ := body["type"].(string)
		if kind == "" {
			kind = event
		}
		if kind == "message_stop" {
			s.finished = event == "" || event == "message_stop"
			s.done = s.finished
			if !s.finished {
				s.failed = true
			}
		}
		block, _ := body["content_block"].(map[string]any)
		delta, _ := body["delta"].(map[string]any)
		s.useful = s.useful || outputParts([]any{block, delta}) || block["type"] == "tool_use" && nonemptyText(block["name"]) || nonemptyText(delta["partial_json"])
		return kind == "content_block_start" || kind == "content_block_delta"
	case "gemini", "gemini:generate_content":
		candidates, _ := body["candidates"].([]any)
		for _, item := range candidates {
			c, _ := item.(map[string]any)
			if value, ok := c["finishReason"].(string); ok && value != "" {
				s.finished = true
				s.done = true
			}
			if content, ok := c["content"].(map[string]any); ok {
				parts, _ := content["parts"].([]any)
				for _, item := range parts {
					part, _ := item.(map[string]any)
					text, _ := part["text"].(string)
					if text != "" || part["functionCall"] != nil {
						s.finished = true
						s.useful = true
						return true
					}
				}
			}
		}
		return false
	default:
		choices, _ := body["choices"].([]any)
		meaningful := false
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if value, ok := choice["finish_reason"].(string); ok && value != "" {
				s.finished = true
			}
			delta, _ := choice["delta"].(map[string]any)
			meaningful = meaningful || chatOutput(delta)
		}
		s.useful = s.useful || meaningful
		return meaningful
	}
}

// forwardSSE 有界预读 SSE 后逐事件转发；Responses 达到分类预算即提交，之后失败不重放，只有有效终态才算成功。
func forwardSSE(ctx context.Context, body io.Reader, w io.Writer, protocol string, result AttemptResult) (AttemptResult, error) {
	return forwardEventStream(ctx, body, w, protocol, result, streamTerminal{})
}

// forwardEventStream 共享事件校验和输出提交，WebSocket 允许明确的无生成预热并沿用其 16 MiB 消息上限。
func forwardEventStream(ctx context.Context, body io.Reader, w io.Writer, protocol string, result AttemptResult, state streamTerminal) (AttemptResult, error) {
	// maxEventBytes 限制单行和单事件；预读预算只决定提交时机，不用于判定 Responses 上游失败。
	maxEventBytes := 4 * 1024 * 1024
	if state.commitSetup {
		maxEventBytes = 16*1024*1024 + 16
	}
	// responsesPrefetchBytes、responsesPrefetchEvents 分别是 Responses 分类预读的字节与完整事件预算。
	const responsesPrefetchBytes = 256 * 1024
	const responsesPrefetchEvents = 5
	responses := protocol == "openai:responses" || protocol == "responses"
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	var pending bytes.Buffer
	var event string
	var data []string
	eventBytes, prefetchEvents := 0, 0
	// 提交前保留原始协议字节；提交后任何失败都不得切换目标重放。
	writePending := func() error {
		if !result.Committed || pending.Len() == 0 {
			return nil
		}
		result.Retryable = false
		if _, err := w.Write(pending.Bytes()); err != nil {
			return err
		}
		pending.Reset()
		if flusher, ok := w.(interface{ Flush() }); ok {
			flusher.Flush()
		}
		return nil
	}
	flush := func() error {
		if len(data) > 0 {
			meaningful := state.observe(protocol, event, strings.Join(data, "\n"))
			if meaningful || state.finished && (state.useful || state.allowEmpty) {
				result.Committed = true
			}
		}
		prefetchEvents++
		// Aether 的传输帧在此读取器中不可见，以完整 SSE 事件限制小前奏的等待次数。
		if responses && !state.failed && prefetchEvents >= responsesPrefetchEvents {
			result.Committed = true
		}
		eventBytes = 0
		event = ""
		data = nil
		return writePending()
	}
	for scanner.Scan() {
		line := scanner.Text()
		eventBytes += len(line) + 1
		// 单事件始终有界；其他协议保留原有 64 KiB 前奏边界。
		if eventBytes > maxEventBytes || (!responses && !result.Committed && pending.Len()+len(line)+1 > 64*1024) {
			result.Outcome.Penalty = Heavy
			result.Retryable = !result.Committed
			return result, errors.New("上游SSE单事件或前奏过长")
		}
		pending.WriteString(line)
		pending.WriteByte('\n')
		if line == "" {
			if err := flush(); err != nil {
				return result, err
			}
			if state.failed || state.done {
				break
			}
		} else {
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		// 达到分类预算只结束预读，不能把仍在传输的正常前奏误判为失败；允许在事件中途提交。
		if responses && !result.Committed && pending.Len() >= responsesPrefetchBytes {
			result.Committed = true
		}
		if responses {
			if err := writePending(); err != nil {
				return result, err
			}
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if eventBytes > 0 {
		if err := flush(); err != nil {
			return result, err
		}
	}
	if scanner.Err() != nil || state.failed || !state.finished || !state.useful && !state.allowEmpty {
		result.Outcome.Penalty = Heavy
		result.Retryable = !result.Committed
		return result, errors.New("上游流式响应错误或缺少有效终态")
	}
	result.Outcome.Success = true
	result.Warmup = state.allowEmpty
	if result.Warmup {
		result.Outcome.Success = false
	}
	result.ResponseID = state.responseID
	return result, nil
}

// upstreamPath 仅替换Gemini模型路径中的模型段，保留原操作与查询参数。
func upstreamPath(req ExecutionRequest, model string) string {
	parsed, _ := url.Parse(req.Path)
	query := parsed.Query()
	for _, key := range []string{"key", "api_key", "access_token"} {
		query.Del(key)
	}
	parsed.RawQuery = query.Encode()
	if req.Request.Protocol == "gemini" || req.Request.Protocol == "gemini:generate_content" {
		marker := "/models/"
		start := strings.Index(parsed.Path, marker)
		if start >= 0 {
			end := strings.Index(parsed.Path[start+len(marker):], ":")
			if end >= 0 {
				parsed.Path = parsed.Path[:start+len(marker)] + strings.TrimPrefix(model, "models/") + parsed.Path[start+len(marker)+end:]
			}
		}
	}
	return parsed.String()
}
