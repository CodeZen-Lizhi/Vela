package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/coder/websocket"
)

// realtimeQuery 要求唯一且有界的模型参数，不允许通过查询串另带认证或选择私有 Live 通道。
func realtimeQuery(r *http.Request) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errors.New("Realtime 查询参数无效")
	}
	model := ""
	for name, values := range query {
		switch strings.ToLower(name) {
		case "model":
			if model != "" || len(values) != 1 || values[0] == "" {
				return nil, errors.New("Realtime 需要唯一的 model 参数")
			}
			model = values[0]
			delete(query, name)
		case "key", "api_key", "api-key", "x-api-key", "access_token", "authorization", "token", "client_secret", "secret_key", "signature", "sig":
			return nil, errors.New("Realtime 查询串不接受认证参数")
		}
	}
	if model == "" || len(model) > 256 || strings.TrimSpace(model) != model || strings.ContainsFunc(model, unicode.IsControl) {
		return nil, errors.New("Realtime model 必须是 1～256 字节的有效模型名")
	}
	query.Set("model", model)
	return query, nil
}

// realtime 接收标准 OpenAI Realtime 连接；整条连接经同一次真实调度准入并随暂停/退出清理。
func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	query, err := realtimeQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	model := query.Get("model")
	if err = s.requireModel(r, model); err != nil {
		writeError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
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
	body, _ := json.Marshal(map[string]string{"model": model})
	// Realtime 使用连接总期限；不启用 HTTP SSE 的逐次 StreamTotalMillis 限时。
	_, err = s.executor.Execute(ctx, scheduling.ExecutionRequest{
		Request: scheduling.Request{Model: model, Protocol: "openai:realtime"},
		Type:    "realtime", Method: http.MethodGet, Path: "/v1/realtime?" + query.Encode(),
		Body: body, Headers: clientHeaders(r.Header), Realtime: conn,
		RetrySafe: true, Principal: requestPrincipal(r),
	}, io.Discard)
	if err != nil && ctx.Err() == nil {
		_ = sendSocketError(ctx, conn, http.StatusBadGateway, "realtime_error", "Realtime 连接未完成，请检查模型权限与上游状态")
	}
}
