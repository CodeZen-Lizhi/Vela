package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/coder/websocket"
)

// responsesClient 关联升级后的客户端与独立取消函数，暂停或退出时必须主动关闭被 HTTP 服务接管以外的连接。
type responsesClient struct {
	// conn 是已升级的客户端；cancel 取消整个连接及其当前轮次。
	conn   *websocket.Conn
	cancel context.CancelFunc
	// done 在轮次结算和上游清理完成后关闭，保证宿主随后可安全关闭数据库。
	done chan struct{}
}

// socketFrame 是一个有界的客户端完整消息；控制帧由 WebSocket 库自动处理。
type socketFrame struct {
	// kind 是 WebSocket 消息类型；data 是库按 16 MiB 上限读取的完整消息。
	kind websocket.MessageType
	data []byte
}

// closeWebSockets 关闭所有升级连接并取消生成，不依赖 http.Server.Shutdown 对 hijacked 连接的处理。
func (s *Server) closeWebSockets(ctx context.Context) {
	s.mu.Lock()
	clients := s.webSocketClients
	s.webSocketClients = make(map[*responsesClient]struct{})
	s.mu.Unlock()
	for client := range clients {
		client.cancel()
		_ = client.conn.CloseNow()
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	for client := range clients {
		select {
		case <-client.done:
		case <-bounded.Done():
			return
		}
	}
}

// sendSocketError 发送 Responses 标准错误事件；不回显原始消息、认证或内部数据库错误。
func sendSocketError(ctx context.Context, conn *websocket.Conn, status int, code, message string) error {
	data, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": map[string]any{"type": "invalid_request_error", "code": code, "message": message}})
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.Write(bounded, websocket.MessageText, data)
}

// responsesWebSocket 运行标准 Responses 默认通道；逐轮复核认证、模型和配置，原生上游连接随会话复用。
func (s *Server) responsesWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)
	ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
	defer cancel()
	client := &responsesClient{conn: conn, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	if s.server == nil || s.paused {
		s.mu.Unlock()
		return
	}
	if s.webSocketClients == nil {
		s.webSocketClients = make(map[*responsesClient]struct{})
	}
	s.webSocketClients[client] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.webSocketClients, client); s.mu.Unlock(); close(client.done) }()
	upstream := scheduling.NewWebSocketSession()
	defer upstream.Close()
	token, _ := requestToken(r)
	principal := requestPrincipal(r)
	frames := make(chan socketFrame, 1)
	var active atomic.Bool
	// 与 Aether 一样，首个事件必须在 60 秒内到达；连接总寿命一小时。读协程在客户端断开时立即取消上游生成。
	initial := time.AfterFunc(time.Minute, cancel)
	defer initial.Stop()
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			initial.Stop()
			if !active.CompareAndSwap(false, true) {
				if sendSocketError(ctx, conn, 400, "response_in_progress", "上一轮响应尚未结束") != nil {
					return
				}
				continue
			}
			select {
			case frames <- socketFrame{kind: kind, data: data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	var currentModel string
	for {
		var frame socketFrame
		select {
		case frame = <-frames:
		case <-ctx.Done():
			return
		}
		if frame.kind != websocket.MessageText {
			_ = conn.Close(websocket.StatusUnsupportedData, "只支持 JSON 文本消息")
			return
		}
		s.mu.Lock()
		valid := s.accessKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.accessKey)) == 1
		s.mu.Unlock()
		if !valid {
			_ = sendSocketError(ctx, conn, 401, "invalid_api_key", "网关访问密钥已失效")
			return
		}
		var body map[string]json.RawMessage
		parseErr := json.Unmarshal(frame.data, &body)
		var eventType string
		_ = json.Unmarshal(body["type"], &eventType)
		if parseErr != nil || body == nil || eventType != "response.create" {
			active.Store(false)
			if sendSocketError(ctx, conn, 400, "invalid_response_create", "消息必须是 response.create JSON 对象") != nil {
				return
			}
			continue
		}
		if _, named := body["stream_id"]; named {
			active.Store(false)
			if sendSocketError(ctx, conn, 400, "named_stream_unsupported", "当前只支持默认 WebSocket 通道，请省略 stream_id") != nil {
				return
			}
			continue
		}
		var model string
		if _, present := body["model"]; !present && currentModel != "" {
			body["model"], _ = json.Marshal(currentModel)
		}
		if json.Unmarshal(body["model"], &model) != nil || model == "" {
			active.Store(false)
			if sendSocketError(ctx, conn, 400, "invalid_model", "首次 response.create 必须提供 model") != nil {
				return
			}
			continue
		}
		if err = s.requireModel(r, model); err != nil {
			active.Store(false)
			if sendSocketError(ctx, conn, 404, "model_not_found", err.Error()) != nil {
				return
			}
			continue
		}
		data, _ := json.Marshal(body)
		turnCtx, turnCancel := context.WithCancel(ctx)
		writer := &scheduling.WebSocketEventWriter{Context: turnCtx, Conn: conn, OnTerminal: func() { active.Store(false) }}
		result, runErr := s.executor.Execute(turnCtx, scheduling.ExecutionRequest{Request: scheduling.Request{Model: model, Protocol: "openai:responses"}, Body: data, Path: "/v1/responses", Headers: clientHeaders(r.Header), Stream: true, RetrySafe: true, Principal: principal, WebSocket: upstream}, writer)
		turnCancel()
		if ctx.Err() != nil {
			return
		}
		if runErr != nil {
			if !writer.Terminal {
				status := result.Result.Status
				if status < 400 {
					status = 502
				}
				if errors.Is(runErr, scheduling.ErrNoCandidates) {
					status = 503
				}
				if errors.Is(runErr, scheduling.ErrContinuation) {
					status = 409
				}
				active.Store(false)
				if sendSocketError(ctx, conn, status, "upstream_error", runErr.Error()) != nil {
					return
				}
			}
			if result.Result.Committed && !writer.Terminal {
				return
			}
		} else {
			currentModel = model
		}
	}
}
