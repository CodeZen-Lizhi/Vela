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

// Run 只改写 JSON 顶层模型名和上游认证；SSE 逐事件验证，输出后失败不透明重放。
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
	body["model"], err = json.Marshal(c.Model)
	if err != nil {
		return result, err
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
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Retryable = true
		result.TransportFailure = true
		result.Outcome.Penalty = Heavy
		if !stopped || errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			result.TransportFailure = false
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
		var status string
		_ = json.Unmarshal(body["status"], &status)
		return (status == "completed" || status == "incomplete") && bytes.HasPrefix(bytes.TrimSpace(body["output"]), []byte("["))
	case "anthropic", "claude:messages":
		var stop *string
		_ = json.Unmarshal(body["stop_reason"], &stop)
		var kind string
		_ = json.Unmarshal(body["type"], &kind)
		return kind == "message" && stop != nil && *stop != "" && len(body["content"]) > 0 && bytes.HasPrefix(bytes.TrimSpace(body["content"]), []byte("["))
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
		for _, c := range choices {
			if c.FinishReason == nil || *c.FinishReason == "" || !bytes.HasPrefix(bytes.TrimSpace(c.Message), []byte("{")) {
				return false
			}
		}
		return true
	}
}

// streamTerminal 记录流式协议终态与有效输出；首个非空模型或工具增量之后不得切目标。
type streamTerminal struct {
	// finished、done、failed 分别记录协议结束、Chat DONE 标志与错误事件。
	finished bool
	done     bool
	failed   bool
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
		if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" {
			response, ok := body["response"]
			if ok {
				raw, _ := json.Marshal(response)
				s.finished = validJSONTerminal(protocol, raw)
				s.done = s.finished
			}
			if !s.finished {
				s.failed = true
			}
		}
		if kind == "response.failed" {
			s.failed = true
		}
		delta, _ := body["delta"].(string)
		return delta != "" || kind == "response.output_item.added" || kind == "response.output_item.done"
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
			for _, key := range []string{"content", "reasoning_content", "tool_calls", "function_call"} {
				v := delta[key]
				if v != nil && v != "" {
					meaningful = true
				}
			}
		}
		return meaningful
	}
}

// forwardSSE 缓冲提交前的协议前奏，之后逐事件转发；缺失终态或错误信封不回分，也不重放已输出内容。
func forwardSSE(ctx context.Context, body io.Reader, w io.Writer, protocol string, result AttemptResult) (AttemptResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	var pending, eventBlock bytes.Buffer
	var event string
	var data []string
	state := streamTerminal{}
	flush := func() error {
		if len(data) > 0 {
			meaningful := state.observe(protocol, event, strings.Join(data, "\n"))
			if meaningful || state.finished {
				result.Committed = true
			}
		}
		pending.Write(eventBlock.Bytes())
		eventBlock.Reset()
		event = ""
		data = nil
		if !result.Committed && pending.Len() > 64*1024 {
			return errors.New("上游在有效输出前返回过长的 SSE 前奏")
		}
		if result.Committed && pending.Len() > 0 {
			if _, err := w.Write(pending.Bytes()); err != nil {
				return err
			}
			pending.Reset()
			if flusher, ok := w.(interface{ Flush() }); ok {
				flusher.Flush()
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		// Scanner只限制单行，事件总量也必须有界，避免无空行的keepalive无限累积。
		if eventBlock.Len()+len(line)+1 > 4*1024*1024 || (!result.Committed && pending.Len()+eventBlock.Len()+len(line)+1 > 64*1024) {
			result.Outcome.Penalty = Heavy
			result.Retryable = !result.Committed
			return result, errors.New("上游SSE单事件或前奏过长")
		}
		eventBlock.WriteString(line)
		eventBlock.WriteByte('\n')
		if line == "" {
			if err := flush(); err != nil {
				return result, err
			}
			if state.failed || state.done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if eventBlock.Len() > 0 {
		if err := flush(); err != nil {
			return result, err
		}
	}
	if scanner.Err() != nil || state.failed || !state.finished {
		result.Outcome.Penalty = Heavy
		result.Retryable = !result.Committed
		return result, errors.New("上游流式响应错误或缺少有效终态")
	}
	result.Outcome.Success = true
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
