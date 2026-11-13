package scheduling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
)

// WebSocketSession 持有一个客户端连接对应的上游物理连接和原目标；同一时刻只执行一轮，Close 可中断正在执行的 IO。
type WebSocketSession struct {
	// mu 只保护连接指针和绑定，不在网络读写期间持锁。
	mu      sync.Mutex
	conn    *websocket.Conn
	binding *continuationBinding
	// closed 防止网关停止后迟到的握手重新建立连接。
	closed bool
	// client 是创建连接时的出口快照；代理变化后旧连接不能继续绕过新出口策略。
	client *http.Client
	// messages 由连接级读循环提供，空闲期间也处理 Ping/Pong；readCancel 在断开与清理时终止读循环。
	messages   <-chan upstreamSocketMessage
	readCancel context.CancelFunc
}

// NewWebSocketSession 创建尚未选路的 Responses 标准 WebSocket 会话。
func NewWebSocketSession() *WebSocketSession { return &WebSocketSession{} }

// Close 立即关闭持有的上游连接，释放握手、生成和客户端断开时的资源。
func (s *WebSocketSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.readCancel != nil {
		s.readCancel()
	}
	if s.conn != nil {
		_ = s.conn.CloseNow()
		s.conn = nil
	}
}

// filter 只允许启用 WebSocket 的供应商；已绑定会话失效时返回续接冲突，要求客户端重连而非透明换目标。
func (s *WebSocketSession) filter(req ExecutionRequest, candidates []Candidate) ([]Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, context.Canceled
	}
	result := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Provider.Request.ResponsesWebSocket && (s.binding == nil || s.binding.matches(req, c)) {
			result = append(result, c)
		}
	}
	if len(result) == 0 && s.binding != nil {
		s.closed = true
		if s.readCancel != nil {
			s.readCancel()
			s.readCancel = nil
		}
		if s.conn != nil {
			_ = s.conn.CloseNow()
			s.conn = nil
		}
		return nil, ErrContinuation
	}
	return result, nil
}

// discard 清理失败的连接；只有未向客户端提交任何状态时才允许下一候选重新选路。
func (s *WebSocketSession) discard(uncommitted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readCancel != nil {
		s.readCancel()
		s.readCancel = nil
	}
	if s.conn != nil {
		_ = s.conn.CloseNow()
		s.conn = nil
	}
	if uncommitted {
		s.binding = nil
	}
}

// runWebSocket 复用标准 Responses 事件校验、记录和代理客户端；HTTP/SSE 客户端不会进入此路径。
func (r *HTTPRunner) runWebSocket(ctx context.Context, c Candidate, req ExecutionRequest, call *http.Request, client *http.Client, w io.Writer) (AttemptResult, error) {
	var result AttemptResult
	session := req.WebSocket
	finish, err := records.PrepareStreamCapture(call, true)
	if err != nil {
		result.Status = 500
		return result, errors.New("本地请求记录准备失败，未发送上游")
	}
	session.mu.Lock()
	conn := session.conn
	messages := session.messages
	closed := session.closed
	changedExit := conn != nil && session.client != client
	session.mu.Unlock()
	if closed {
		return result, context.Canceled
	}
	if changedExit {
		session.discard(false)
		result.Status = 409
		_, _ = finish(nil, errors.New("WebSocket 出口配置已改变"))
		return result, ErrContinuation
	}
	status := http.StatusSwitchingProtocols
	headers := http.Header{"Upgrade": []string{"websocket"}}
	if conn == nil {
		// 常规 HTTP 采集包装器的响应体不可写，握手复用其原始 transport，实际轮次另由同一个采集器观察。
		dialClient := *client
		if transport, ok := client.Transport.(*records.Transport); ok {
			dialClient.Transport = transport.Base
		}
		address := *call.URL
		if address.Scheme == "https" {
			address.Scheme = "wss"
		} else {
			address.Scheme = "ws"
		}
		first := c.Provider.Request.FirstByteSeconds
		if first == 0 {
			first = 30
		}
		dialCtx, cancel := context.WithTimeout(ctx, time.Duration(first)*time.Second)
		var response *http.Response
		conn, response, err = websocket.Dial(dialCtx, address.String(), &websocket.DialOptions{HTTPClient: &dialClient, HTTPHeader: call.Header})
		expired := errors.Is(dialCtx.Err(), context.DeadlineExceeded)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				_, _ = finish(nil, err)
				return result, ctx.Err()
			}
			result.Retryable = true
			result.Outcome.Penalty = Heavy
			if response != nil {
				captured, _ := finish(response, nil)
				data, _ := io.ReadAll(captured.Body)
				_ = captured.Body.Close()
				result.Status = response.StatusCode
				result.Outcome = classifyHTTP(result.Status, data)
				result.Outcome.RetryAfter, result.Outcome.RetryAfterSet = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
				if result.Outcome.Penalty == Neutral {
					result.Outcome.Penalty = Heavy
				}
				result.Retryable = result.Outcome.Penalty == Heavy || result.Outcome.Penalty == Light
			} else {
				_, _ = finish(nil, err)
				if expired {
					result.Status = 504
					result.Outcome.Penalty = Light
				}
			}
			return result, errors.New("上游 WebSocket 握手失败")
		}
		headers = response.Header
		conn.SetReadLimit(16 << 20)
		session.mu.Lock()
		if session.closed {
			session.mu.Unlock()
			_ = conn.CloseNow()
			return result, context.Canceled
		}
		readCtx, readCancel := context.WithCancel(context.Background())
		received := make(chan upstreamSocketMessage, 1)
		session.conn = conn
		session.client = client
		session.messages = received
		session.readCancel = readCancel
		messages = received
		session.binding = &continuationBinding{provider: c.Provider.ID, key: c.Key.ID, version: c.CredentialVersion, url: c.Provider.URL, upstream: c.Model, model: req.Request.Model, protocol: req.Request.Protocol, websocket: true}
		session.mu.Unlock()
		go readUpstreamSocket(readCtx, conn, received)
	}
	result.Status = status
	raw, err := call.GetBody()
	if err != nil {
		session.discard(true)
		return result, err
	}
	data, err := io.ReadAll(raw)
	_ = raw.Close()
	if err != nil {
		session.discard(true)
		return result, err
	}
	if err = conn.Write(ctx, websocket.MessageText, data); err != nil {
		_, _ = finish(nil, err)
		session.discard(req.PreviousResponseID == "")
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Outcome.Penalty = Heavy
		result.Retryable = req.PreviousResponseID == ""
		return result, errors.New("发送上游 WebSocket 请求失败")
	}
	events := &webSocketEvents{ctx: ctx, messages: messages}
	response, _ := finish(&http.Response{StatusCode: status, Header: headers, Body: events}, nil)
	defer response.Body.Close()
	var options struct {
		// Generate 是客户端显式预热控制，缺失或 true 时必须有有效模型输出。
		Generate *bool `json:"generate"`
	}
	_ = json.Unmarshal(data, &options)
	warmup := options.Generate != nil && !*options.Generate
	result, err = forwardEventStream(ctx, response.Body, w, "openai:responses", result, streamTerminal{commitSetup: true, allowEmpty: warmup})
	if err != nil {
		if ctx.Err() == nil && events.failureStatus > 0 {
			result.Status = events.failureStatus
			result.Outcome = classifyHTTP(events.failureStatus, events.failureBody)
			result.Retryable = !result.Committed && (result.Outcome.Penalty == Light || result.Outcome.Penalty == Heavy)
		}
		session.discard(!result.Committed)
	}
	return result, err
}

// webSocketEvents 将上游完整 JSON 事件提供给已有 SSE 观察器；仅内部采集采用 SSE 形状，不改变外部 WebSocket 帧。
type webSocketEvents struct {
	// ctx 是本轮期限；messages 来自连接级读循环，pending 是单个已读取事件的剩余字节。
	ctx      context.Context
	messages <-chan upstreamSocketMessage
	pending  *bytes.Reader
	// failureStatus、failureBody 保留结构化错误归类事实，不根据生成文字猜测错误身份。
	failureStatus int
	failureBody   []byte
}

// Read 逐帧读取文本并保留事件边界，二进制帧和非法 JSON 直接判定协议失败。
func (r *webSocketEvents) Read(p []byte) (int, error) {
	if r.pending == nil || r.pending.Len() == 0 {
		var message upstreamSocketMessage
		select {
		case message = <-r.messages:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
		if message.err != nil {
			return 0, message.err
		}
		kind, data := message.kind, message.data
		var err error
		if kind != websocket.MessageText || !json.Valid(data) {
			return 0, errors.New("上游 WebSocket 事件必须是 JSON 文本")
		}
		// JSON 可包含格式化换行；压紧后才进入按行解析的内部 SSE 通道，数值及事件内容不变。
		var compact bytes.Buffer
		if err = json.Compact(&compact, data); err != nil {
			return 0, err
		}
		r.failureStatus, r.failureBody = webSocketFailure(compact.Bytes())
		r.pending = bytes.NewReader([]byte("data: " + compact.String() + "\n\n"))
	}
	return r.pending.Read(p)
}

// Close 只结束本轮采集；长连接由会话或失败清理路径关闭，以便下一轮复用。
func (r *webSocketEvents) Close() error { return nil }

// WebSocketEventWriter 将内部 SSE 输出还原为逐个 JSON 文本帧，提供给客户端连接。
type WebSocketEventWriter struct {
	// Context、Conn 关联当前逻辑轮次与客户端；pending 最多积累一个受协议上限约束的事件。
	Context context.Context
	Conn    *websocket.Conn
	pending []byte
	// Terminal 标记已向客户端交付终态，OnTerminal 允许入口在终态送达后接收下一轮。
	Terminal   bool
	OnTerminal func()
}

// Write 将完整事件转发给客户端，写入等待上限沿用 Aether 的 30 秒，并受本轮总期限约束。
func (w *WebSocketEventWriter) Write(data []byte) (int, error) {
	w.pending = append(w.pending, data...)
	for {
		block, rest, ok := bytes.Cut(w.pending, []byte("\n\n"))
		if !ok {
			break
		}
		var parts []string
		for _, line := range strings.Split(string(block), "\n") {
			if text, ok := strings.CutPrefix(line, "data:"); ok {
				parts = append(parts, strings.TrimPrefix(text, " "))
			}
		}
		if len(parts) > 0 {
			text := []byte(strings.Join(parts, "\n"))
			var event struct {
				// Type 是终态事件标识，客户端收到该事件后可立即开始下一轮。
				Type string `json:"type"`
			}
			_ = json.Unmarshal(text, &event)
			terminal := false
			switch event.Type {
			case "response.completed", "response.incomplete", "response.done", "response.failed", "response.cancelled", "error":
				terminal = true
			}
			// 在发送终态前解除接收门闩，避免客户端已经收到终态却因结算尚未结束被误判为并发生成；下一轮仍在入口串行执行。
			if terminal && w.OnTerminal != nil {
				w.OnTerminal()
			}
			ctx, cancel := context.WithTimeout(w.Context, 30*time.Second)
			err := w.Conn.Write(ctx, websocket.MessageText, text)
			cancel()
			if err != nil {
				return 0, err
			}
			w.Terminal = w.Terminal || terminal
		}
		w.pending = rest
	}
	return len(data), nil
}

// webSocketFailure 只按 WebSocket 错误事件的结构化状态分类，普通输出中的 error 文字不参与健康结算。
func webSocketFailure(data []byte) (int, []byte) {
	var event struct {
		// Type、Status、Error 是顶层协议字段，Response.Error 承载 response.failed 的错误信封。
		Type   string          `json:"type"`
		Status int             `json:"status"`
		Error  json.RawMessage `json:"error"`
		// Response.Error 是 response.failed 携带的结构化上游错误，不读取其他生成字段。
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil {
		return 0, nil
	}
	if event.Type == "response.cancelled" {
		return 499, nil
	}
	if event.Type != "error" && event.Type != "response.failed" {
		return 0, nil
	}
	raw := event.Error
	if len(raw) == 0 {
		raw = event.Response.Error
	}
	var envelope struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &envelope)
	status := event.Status
	if status < 400 || status > 599 {
		status = 502
		for _, code := range []string{envelope.Code, envelope.Type} {
			switch code {
			case "invalid_request_error":
				status = 400
			case "authentication_error", "invalid_api_key", "invalid_authentication":
				status = 401
			case "permission_error":
				status = 403
			case "insufficient_quota", "credit_balance_too_low", "billing_hard_limit_reached":
				status = 402
			case "rate_limit_error", "rate_limit_exceeded":
				status = 429
			case "server_error", "api_error":
				status = 500
			case "overloaded_error", "server_overloaded":
				status = 503
			}
		}
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"error": raw})
	return status, body
}

// upstreamSocketMessage 是上游一个完整消息或读取故障，通道最多排队一个消息，避免空闲期无界积累。
type upstreamSocketMessage struct {
	// kind、data、err 分别保存文本类型、原始字节与连接错误；出现 err 后读循环结束。
	kind websocket.MessageType
	data []byte
	err  error
}

// readUpstreamSocket 在整个连接生命周期读取并处理控制帧，而非只在生成期间读取；关闭会话可立即取消。
func readUpstreamSocket(ctx context.Context, conn *websocket.Conn, messages chan<- upstreamSocketMessage) {
	for {
		kind, data, err := conn.Read(ctx)
		select {
		case messages <- upstreamSocketMessage{kind: kind, data: data, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
