// Package gateway 提供仅监听本机的 OpenAI 兼容 HTTP 入口。
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

const (
	// requestBodyLimit 限制客户端请求正文，避免本机入口无界读取内存。
	requestBodyLimit = 16 << 20
	// shutdownTimeout 是本机网关停止监听时等待在途请求结束的上限。
	shutdownTimeout = 2 * time.Second
)

var (
	// ErrAccessKeyRequired 表示尚未设置访问密钥，拒绝以无鉴权方式开放本机端口。
	ErrAccessKeyRequired = errors.New("请先设置网关访问密钥")
	// ErrNotListening 表示网关尚未监听，当前无法暂停或恢复。
	ErrNotListening = errors.New("网关尚未监听")
)

// Executor 是网关依赖的调度执行边界；生产实现复用真实供应商、凭据和请求记录。
type Executor interface {
	// Execute 完成一次逻辑请求，并仅在协议成功时向 writer 写出完整终态。
	Execute(context.Context, scheduling.ExecutionRequest, io.Writer) (scheduling.ExecutionResult, error)
}

// Server 管理本机网关监听生命周期；访问密钥可在不中断监听的情况下替换。
type Server struct {
	// catalog 提供模型页当前公开的统一模型清单。
	catalog *models.Store
	// executor 是复用真实调度链路的执行入口。
	executor Executor
	// mu 保护密钥、监听端口和替换中的 HTTP 服务。
	mu sync.Mutex
	// accessKey 是最近一次成功保存的网关访问密钥。
	accessKey string
	// port 是最近一次成功开始监听的端口。
	port int
	// server、listener 在监听期间非空；暂停或关闭后清空。
	server   *http.Server
	listener net.Listener
	// paused 区分用户主动暂停和尚未配置，供菜单栏恢复入口使用。
	paused bool
}

// New 创建网关生命周期；调用 Listen 后才开始接受连接。
func New(catalog *models.Store, executor Executor) *Server {
	return &Server{catalog: catalog, executor: executor}
}

// SetAccessKey 更新后续请求使用的访问密钥；空值会立即拒绝所有请求。
func (s *Server) SetAccessKey(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKey = value
}

// Listen 在 127.0.0.1 的指定端口开始监听；新端口绑定失败时保留原监听。
func (s *Server) Listen(ctx context.Context, port int) error {
	s.mu.Lock()
	if strings.TrimSpace(s.accessKey) == "" {
		s.mu.Unlock()
		return ErrAccessKeyRequired
	}
	if s.listener != nil && s.port == port && !s.paused {
		s.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("监听 127.0.0.1:%d 失败: %w", port, err)
	}
	next := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	previous := s.server
	s.server = next
	s.listener = listener
	s.port = port
	s.paused = false
	s.mu.Unlock()

	go func() {
		err := next.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("本机网关监听停止: %v", err)
		}
	}()
	if previous != nil {
		shutdownServer(ctx, previous)
	}
	return nil
}

// Pause 停止接收新请求并等待在途请求结束；未监听时保持幂等。
func (s *Server) Pause(ctx context.Context) error {
	s.mu.Lock()
	if s.server == nil {
		s.paused = true
		s.mu.Unlock()
		return nil
	}
	server := s.server
	s.server = nil
	s.listener = nil
	s.paused = true
	s.mu.Unlock()
	return shutdownServer(ctx, server)
}

// Resume 按最近保存的端口恢复监听；端口或访问密钥不可用时返回明确错误。
func (s *Server) Resume(ctx context.Context) error {
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	if port == 0 {
		return ErrNotListening
	}
	return s.Listen(ctx, port)
}

// Close 停止监听并禁止后续自动恢复，供应用退出时释放端口。
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	s.paused = false
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return shutdownServer(ctx, server)
}

// Running 报告当前是否接受请求。
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server != nil
}

// Paused 报告当前是否由用户主动暂停。
func (s *Server) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// Configured 报告访问密钥和监听端口是否足以恢复网关。
func (s *Server) Configured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.accessKey) != "" && s.port != 0
}

// shutdownServer 使用独立有限期限关闭服务，调用方取消不延长退出等待。
func shutdownServer(ctx context.Context, server *http.Server) error {
	closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return server.Shutdown(closeContext)
}

// handler 构造本机路由；所有请求先完成访问密钥验证。
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.authorize(s.health))
	mux.HandleFunc("/v1/models", s.authorize(s.listModels))
	mux.HandleFunc("/v1/chat/completions", s.authorize(s.chatCompletions))
	return mux
}

// authorize 只接受 Bearer 访问密钥，并在比较时避免长度差异提前结束。
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		s.mu.Lock()
		expected := s.accessKey
		s.mu.Unlock()
		if !ok || expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid_api_key", "访问密钥无效")
			return
		}
		next(w, r)
	}
}

// bearerToken 解析标准 Bearer 头，不接受其他认证形式。
func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// health 返回稳定的本机存活结果，不访问上游。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// modelObject 是 OpenAI 模型列表所需的稳定公开字段。
type modelObject struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name"`
}

// listModels 返回模型页全部已添加的对话模型；空列表仍是有效成功结果。
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	items, err := s.catalog.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_error", "读取模型列表失败")
		return
	}
	data := make([]modelObject, 0, len(items))
	for _, item := range items {
		if item.Kind != "chat" {
			continue
		}
		data = append(data, modelObject{ID: item.ModelID, Object: "model", OwnedBy: "vela", DisplayName: item.Name})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data}); err != nil {
		log.Printf("写入模型列表失败: %v", err)
	}
}

// chatCompletions 校验模型和请求正文后，交给现有调度执行器完成真实上游调用。
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, requestBodyLimit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "读取请求正文失败")
		return
	}
	if len(body) > requestBodyLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求正文超过 16 MiB")
		return
	}
	modelName, stream, err := parseChatRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err = s.requireChatModel(r, modelName); err != nil {
		writeError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	result, err := s.executor.Execute(r.Context(), scheduling.ExecutionRequest{
		Request:   scheduling.Request{Model: modelName, Protocol: "openai:chat"},
		Path:      "/v1/chat/completions",
		Body:      body,
		Headers:   clientHeaders(r.Header),
		Stream:    stream,
		RetrySafe: true,
	}, w)
	if err == nil || result.Result.Committed {
		return
	}
	status := result.Result.Status
	if status < 400 || status > 599 {
		switch {
		case errors.Is(err, scheduling.ErrNoCandidates):
			status = http.StatusServiceUnavailable
		case errors.Is(err, scheduling.ErrContinuation):
			status = http.StatusConflict
		default:
			status = http.StatusBadGateway
		}
	}
	writeError(w, status, "upstream_error", err.Error())
}

// parseChatRequest 只从 JSON 对象读取模型和显式流式开关，不重编码客户端正文。
func parseChatRequest(body []byte) (string, bool, error) {
	var document struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return "", false, errors.New("请求正文必须是 JSON 对象")
	}
	model := strings.TrimSpace(document.Model)
	if model == "" {
		return "", false, errors.New("model 为必填字段")
	}
	stream := document.Stream != nil && *document.Stream
	return model, stream, nil
}

// requireChatModel 确认请求名属于模型页当前公开的对话模型。
func (s *Server) requireChatModel(r *http.Request, modelName string) error {
	items, err := s.catalog.List(r.Context())
	if err != nil {
		return errors.New("读取模型列表失败")
	}
	for _, item := range items {
		if item.ModelID == modelName && item.Kind == "chat" {
			return nil
		}
	}
	return fmt.Errorf("模型 %q 不在 Vela 对话模型列表中", modelName)
}

// clientHeaders 仅保留用于会话识别的客户端头，不转发认证、Cookie 或传输控制头。
func clientHeaders(input http.Header) http.Header {
	blocked := map[string]bool{
		"authorization": true, "cookie": true, "proxy-authorization": true,
		"connection": true, "content-length": true, "transfer-encoding": true,
	}
	result := make(http.Header)
	for name, values := range input {
		if blocked[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			result.Add(name, value)
		}
	}
	return result
}

// writeError 写出 OpenAI 风格错误信封，不包含凭据或内部配置路径。
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": "gateway_error", "code": code},
	})
}
