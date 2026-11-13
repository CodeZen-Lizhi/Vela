// Package gateway 提供仅监听本机的 OpenAI 兼容 HTTP 入口。
package gateway

import (
	"context"
	"crypto/sha256"
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
	requestBodyLimit = 128 << 20
	// shutdownTimeout 是停止接入后允许在途请求自然完成的时间，超时后取消并等待业务收尾。
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
	// lifecycleMu 串行化监听替换、暂停和关闭；等待请求收尾时不得持有 mu。
	lifecycleMu sync.Mutex
	// mu 保护密钥、监听端口和替换中的 HTTP 服务。
	mu sync.Mutex
	// accessKey 是最近一次成功保存的网关访问密钥。
	accessKey string
	// port 是最近一次成功开始监听的端口。
	port int
	// server、listener 在监听期间非空；暂停或关闭后清空。
	server   *httpLifecycle
	listener net.Listener
	// paused 区分用户主动暂停和尚未配置，供菜单栏恢复入口使用。
	paused bool
	// webSocketClients 跟踪升级后脱离 HTTP 生命周期的连接，暂停/退出时统一关闭。
	webSocketClients map[*responsesClient]struct{}
	// uploads 保存本进程分段上传的原生目标路径；认证及具体 Key 由调度资源绑定复核。
	uploads map[string]uploadBinding
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
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.listen(ctx, port)
}

// listen 在持有 lifecycleMu 时绑定并替换监听，返回前完成旧 HTTP 请求的取消及收尾。
func (s *Server) listen(ctx context.Context, port int) error {
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
	next := newHTTPLifecycle(s.handler())
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
		return shutdownServer(ctx, previous)
	}
	return nil
}

// Pause 停止接入并在自然等待超时后取消 HTTP 请求，等业务收尾完成；未监听时保持幂等。
func (s *Server) Pause(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.server == nil {
		s.paused = true
		s.mu.Unlock()
		s.closeWebSockets(ctx)
		return nil
	}
	server := s.server
	s.server = nil
	s.listener = nil
	s.paused = true
	s.mu.Unlock()
	server.stopAdmission()
	s.closeWebSockets(ctx)
	return shutdownServer(ctx, server)
}

// Resume 按最近保存的端口恢复监听；端口或访问密钥不可用时返回明确错误。
func (s *Server) Resume(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	if port == 0 {
		return ErrNotListening
	}
	return s.listen(ctx, port)
}

// Close 停止监听并等普通 HTTP handler 收尾完成，返回后宿主可关闭其数据库；仍允许显式重新 Listen。
func (s *Server) Close(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	s.paused = false
	s.mu.Unlock()
	if server != nil {
		server.stopAdmission()
	}
	s.closeWebSockets(ctx)
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

// shutdownServer 先拒绝新业务并有界等待，超时强制取消普通连接，始终等待已进入的 handler 收尾。
// 自然等待超时但强制关闭成功返回 nil；收尾等待不受调用方取消影响，避免宿主提前释放数据库。
func shutdownServer(ctx context.Context, server *httpLifecycle) error {
	server.stopAdmission()
	closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	err := server.Shutdown(closeContext)
	if err != nil {
		closeErr := server.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			err = closeErr
		} else {
			err = errors.Join(err, closeErr)
		}
	}
	server.handlers.Wait()
	return err
}

// handler 构造本机路由；所有请求先完成访问密钥验证。
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.authorize(s.health))
	mux.HandleFunc("/v1/models", s.authorize(s.listModels))
	mux.HandleFunc("/v1/chat/completions", s.authorize(s.chatCompletions))
	mux.HandleFunc("/v1/responses", s.authorize(s.responses))
	mux.HandleFunc("/v1/responses/compact", s.authorize(func(w http.ResponseWriter, r *http.Request) {
		s.forwardModelRequest(w, r, "openai:responses:compact", "/v1/responses/compact")
	}))
	mux.HandleFunc("/v1/messages", s.authorize(s.messages))
	mux.HandleFunc("/v1/messages/count_tokens", s.authorize(func(w http.ResponseWriter, r *http.Request) {
		s.forwardModelRequest(w, r, "claude:count_tokens", "/v1/messages/count_tokens")
	}))
	mux.HandleFunc("/v1beta/models/", s.authorize(s.generateContent))
	mux.HandleFunc("/v1/models/", s.authorize(s.generateContent))
	for path, protocol := range map[string]string{
		"/v1/embeddings":         "openai:embedding",
		"/v1/rerank":             "openai:rerank",
		"/v1/images/generations": "openai:image",
		"/v1/alpha/search":       "openai:search",
		"/v1beta/interactions":   "gemini:interactions",
		"/v1/interactions":       "gemini:interactions",
	} {
		mux.HandleFunc(path, s.authorize(func(w http.ResponseWriter, r *http.Request) {
			s.forwardModelRequest(w, r, protocol, path)
		}))
	}
	mux.HandleFunc("/v1/images/edits", s.authorize(func(w http.ResponseWriter, r *http.Request) {
		s.multipartRequest(w, r, "openai:image")
	}))
	mux.HandleFunc("/v1/videos", s.authorize(s.videos))
	mux.HandleFunc("/v1/videos/", s.authorize(s.videos))
	mux.HandleFunc("/v1beta/files", s.authorize(s.files))
	mux.HandleFunc("/v1beta/files/", s.authorize(s.files))
	mux.HandleFunc("/upload/v1beta/files", s.authorize(s.files))
	mux.HandleFunc("/upload/v1beta/files/", s.authorize(s.uploadChunk))
	mux.HandleFunc("/v1/realtime", s.authorize(s.realtime))
	mux.HandleFunc("/v1beta/interactions/", s.authorize(s.interactionResource))
	mux.HandleFunc("/v1/interactions/", s.authorize(s.interactionResource))
	return mux
}

// authorize 按客户端原生认证方式核对同一个网关密钥，并在比较时避免内容差异提前结束。
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := requestToken(r)
		s.mu.Lock()
		expected := s.accessKey
		s.mu.Unlock()
		if !ok || expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			writeClientError(w, r, http.StatusUnauthorized, "invalid_api_key", "访问密钥无效")
			return
		}
		next(w, r)
	}
}

// requestPrincipal 用认证后的密钥摘要隔离连续会话，不将原始认证保存到调度状态。
func requestPrincipal(r *http.Request) string {
	token, _ := requestToken(r)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

// requestToken 按客户端协议读取单一网关密钥；显式 Authorization 存在时不回退到其他认证来源。
func requestToken(r *http.Request) (string, bool) {
	if value := r.Header.Get("Authorization"); value != "" {
		return bearerToken(value)
	}
	if strings.HasPrefix(r.URL.Path, "/v1/messages") {
		value := r.Header.Get("X-Api-Key")
		return value, value != ""
	}
	if isGeminiAPI(r.URL.Path) {
		value := r.Header.Get("X-Goog-Api-Key")
		if value == "" {
			value = r.URL.Query().Get("key")
		}
		return value, value != ""
	}
	return "", false
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

// listModels 返回模型页全部已添加模型；各协议入口分别校验类型与上游权限。
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
		data = append(data, modelObject{ID: item.ModelID, Object: "model", OwnedBy: "vela", DisplayName: item.Name})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data}); err != nil {
		log.Printf("写入模型列表失败: %v", err)
	}
}

// chatCompletions 校验模型和请求正文后，交给现有调度执行器完成真实上游调用。
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	s.forwardModelRequest(w, r, "openai:chat", "/v1/chat/completions")
}

// responses 接收 OpenAI Responses 请求，并交给同一真实调度执行器完成上游调用。
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.responsesWebSocket(w, r)
		return
	}
	s.forwardModelRequest(w, r, "openai:responses", "/v1/responses")
}

// forwardModelRequest 校验模型和请求正文后，按指定协议与路径发起真实上游调用。
func (s *Server) forwardModelRequest(w http.ResponseWriter, r *http.Request, protocol, path string) {
	if r.Method != http.MethodPost {
		writeClientError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	body, err := readRequestBody(r)
	if err != nil {
		writeClientError(w, r, bodyErrorStatus(err), "invalid_request", err.Error())
		return
	}
	var modelName string
	var stream bool
	var previousInteraction string
	if isGeminiPath(r.URL.Path) {
		var document map[string]json.RawMessage
		if json.Unmarshal(body, &document) != nil || document == nil {
			err = errors.New("请求正文必须是 JSON 对象")
		}
		modelName, _, _ = strings.Cut(geminiModelPath(r.URL.Path), ":")
		stream = strings.HasSuffix(r.URL.Path, ":streamGenerateContent")
		if modelName == "" {
			err = errors.New("模型路径不能为空")
		}
	} else if protocol == "gemini:interactions" {
		modelName, previousInteraction, stream, err = parseInteractionRequest(body)
	} else {
		modelName, stream, err = parseModelRequest(body)
	}
	if err != nil {
		writeClientError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	kind := protocolType(protocol)
	if err = s.requireModelKind(r, modelName, modelKind(kind)); err != nil {
		writeClientError(w, r, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	s.executeHTTP(w, r, scheduling.ExecutionRequest{
		Request:            scheduling.Request{Model: modelName, Protocol: protocol},
		Type:               kind,
		Path:               path,
		Body:               body,
		Headers:            clientHeaders(r.Header),
		Stream:             stream,
		RetrySafe:          kind != "video" && kind != "interactions",
		Principal:          requestPrincipal(r),
		PreviousResponseID: previousInteraction,
	})
}

// finishHTTP 在记录收尾前交付最终错误；已提交的 SSE 明确发送错误事件，禁止伪造正常终态。
func finishHTTP(w http.ResponseWriter, r *http.Request, output io.Writer, result scheduling.ExecutionResult, err error) {
	if err == nil {
		return
	}
	if result.Result.Committed {
		if !result.Result.FailureSent && strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			data, _ := json.Marshal(clientErrorEnvelope(r, http.StatusBadGateway, "upstream_stream_error", "上游流式响应中断或未完整结束"))
			_, _ = fmt.Fprintf(output, "event: error\ndata: %s\n\n", data)
			if flusher, ok := output.(interface{ Flush() }); ok {
				flusher.Flush()
			}
		}
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
	writeClientErrorTo(w, r, output, status, "upstream_error", err.Error())
}

// messages 接收原生 Anthropic Messages，不将请求转换为其他协议。
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	s.forwardModelRequest(w, r, "claude:messages", "/v1/messages")
}

// generateContent 只接收 Gemini 同步或 SSE 对话动作，模型身份由路径提供。
func (s *Server) generateContent(w http.ResponseWriter, r *http.Request) {
	name := geminiModelPath(r.URL.Path)
	_, action, ok := strings.Cut(name, ":")
	if !ok || (action != "generateContent" && action != "streamGenerateContent" && action != "countTokens" && action != "embedContent" && action != "batchEmbedContents") {
		writeClientError(w, r, http.StatusNotFound, "invalid_action", "不支持的 Gemini 调用路径")
		return
	}
	path := r.URL.EscapedPath()
	query := r.URL.Query()
	query.Del("key")
	if action == "streamGenerateContent" {
		query.Set("alt", "sse")
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	protocol := "gemini:generate_content"
	if action == "countTokens" {
		protocol = "gemini:count_tokens"
	} else if action == "embedContent" || action == "batchEmbedContents" {
		protocol = "gemini:embedding"
	}
	s.forwardModelRequest(w, r, protocol, path)
}

// isGeminiPath 识别 Gemini 的稳定版与 beta 模型路径，不与 OpenAI 模型列表混淆。
func isGeminiPath(path string) bool {
	return strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/")
}

// isGeminiAPI 覆盖模型、文件、上传及具名 Interactions 路径，统一原生认证和错误信封。
func isGeminiAPI(path string) bool {
	return isGeminiPath(path) || strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/upload/v1beta/") || path == "/v1/interactions" || strings.HasPrefix(path, "/v1/interactions/")
}

// geminiModelPath 去除已知版本前缀，保留模型名及原生操作。
func geminiModelPath(path string) string {
	return strings.TrimPrefix(strings.TrimPrefix(path, "/v1beta/models/"), "/v1/models/")
}

// parseModelRequest 只从聊天类 JSON 对象读取模型和显式流式开关，不重编码客户端正文。
func parseModelRequest(body []byte) (string, bool, error) {
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

// requireModel 确认请求名属于模型页当前公开的对话模型。
func (s *Server) requireModel(r *http.Request, modelName string) error {
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
		"x-api-key": true, "x-goog-api-key": true,
		"api-key": true, "cookie2": true, "set-cookie": true,
		"connection": true, "content-length": true, "transfer-encoding": true,
	}
	for _, value := range input.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			blocked[strings.ToLower(strings.TrimSpace(name))] = true
		}
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

// writeClientError 按客户端原生协议返回错误信封，不把 Anthropic/Gemini 错误伪装成另一种协议。
func writeClientError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeClientErrorTo(w, r, w, status, code, message)
}

// writeClientErrorTo 将错误正文写入记录包装器，同时由原 HTTP 出口提交头部和状态。
func writeClientErrorTo(w http.ResponseWriter, r *http.Request, output io.Writer, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(output).Encode(clientErrorEnvelope(r, status, code, message))
}

// clientErrorEnvelope 只生成客户端协议信封，供 JSON 错误与已提交 SSE 错误共同使用。
func clientErrorEnvelope(r *http.Request, status int, code, message string) any {
	if strings.HasPrefix(r.URL.Path, "/v1/messages") {
		kind := "api_error"
		switch status {
		case 400, 405:
			kind = "invalid_request_error"
		case 401:
			kind = "authentication_error"
		case 403:
			kind = "permission_error"
		case 404:
			kind = "not_found_error"
		case 429:
			kind = "rate_limit_error"
		case 503:
			kind = "overloaded_error"
		}
		return map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}}
	}
	if isGeminiAPI(r.URL.Path) {
		kind := "INTERNAL"
		switch status {
		case 400, 405:
			kind = "INVALID_ARGUMENT"
		case 401:
			kind = "UNAUTHENTICATED"
		case 403:
			kind = "PERMISSION_DENIED"
		case 404:
			kind = "NOT_FOUND"
		case 429:
			kind = "RESOURCE_EXHAUSTED"
		case 503:
			kind = "UNAVAILABLE"
		case 504:
			kind = "DEADLINE_EXCEEDED"
		}
		return map[string]any{"error": map[string]any{"code": status, "status": kind, "message": message}}
	}
	return map[string]any{"error": map[string]any{"message": message, "type": "gateway_error", "code": code}}
}
