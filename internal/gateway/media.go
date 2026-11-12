package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// protocolType 将公开协议对应到业务类型，类型只控制权限、独立终态与记录，不触发协议转换。
func protocolType(protocol string) string {
	switch protocol {
	case "openai:embedding", "gemini:embedding":
		return "embedding"
	case "openai:rerank":
		return "rerank"
	case "openai:image":
		return "image"
	case "openai:video":
		return "video"
	case "openai:search":
		return "search"
	case "gemini:files":
		return "files"
	case "gemini:interactions":
		return "interactions"
	}
	return "chat"
}

// modelKind 返回正式入口对应的统一模型类别；关联、权限和凭据仍在实际出站处复核。
func modelKind(kind string) string {
	if kind == "embedding" || kind == "rerank" {
		return "vector"
	}
	if kind == "image" || kind == "video" {
		return kind
	}
	return "chat"
}

// parseInteractionRequest 保留 model/agent 原生二选一身份与服务端续接标识，不将续轮当作普通新请求。
func parseInteractionRequest(body []byte) (model, previous string, stream bool, err error) {
	var input struct {
		// Model、Agent 是两种原生目标；Stream 控制 SSE，Previous 关联上一轮的原 Key。
		Model    string  `json:"model"`
		Agent    string  `json:"agent"`
		Stream   bool    `json:"stream"`
		Previous *string `json:"previous_interaction_id"`
	}
	if json.Unmarshal(body, &input) != nil || input.Model == "" && input.Agent == "" || input.Model != "" && input.Agent != "" {
		return "", "", false, errors.New("Interactions 必须提供 model 或 agent 其中一个")
	}
	model = input.Model
	if model == "" {
		model = input.Agent
	}
	if input.Previous != nil {
		if *input.Previous == "" || len(*input.Previous) > 256 {
			return "", "", false, errors.New("previous_interaction_id 无效")
		}
		previous = *input.Previous
	}
	return model, previous, input.Stream, nil
}

// interactionResource 只在已知原绑定上查询或删除原生交互，不根据请求 ID 猜测供应商。
func (s *Server) interactionResource(w http.ResponseWriter, r *http.Request) {
	_, id, _ := strings.Cut(r.URL.Path, "/interactions/")
	if id == "" || strings.Contains(id, "/") || r.Method != http.MethodGet && r.Method != http.MethodDelete {
		writeClientError(w, r, 400, "invalid_request", "不支持的 Interactions 资源操作")
		return
	}
	s.resourceRequest(w, r, "gemini:interactions", id)
}

// multipartRequest 读取图片编辑和视频原生表单，只提取模型及计价元信息，文件字节交由原传输发送。
func (s *Server) multipartRequest(w http.ResponseWriter, r *http.Request, protocol string) {
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		s.forwardModelRequest(w, r, protocol, r.URL.Path)
		return
	}
	if r.Method != http.MethodPost || params["boundary"] == "" {
		writeClientError(w, r, 400, "invalid_request", "需要有效的 multipart POST 请求")
		return
	}
	wire, err := readRequestBody(r)
	if err != nil {
		writeClientError(w, r, bodyErrorStatus(err), "invalid_request", err.Error())
		return
	}
	metadata := make(map[string]string)
	reader := multipart.NewReader(bytes.NewReader(wire), params["boundary"])
	for {
		part, readErr := reader.NextPart()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			writeClientError(w, r, 400, "invalid_request", "multipart 请求无效")
			return
		}
		name := part.FormName()
		if part.FileName() == "" && (name == "model" || name == "size" || name == "quality" || name == "seconds" || name == "resolution") {
			data, fieldErr := io.ReadAll(io.LimitReader(part, 4097))
			if fieldErr != nil || len(data) > 4096 || metadata[name] != "" {
				_ = part.Close()
				writeClientError(w, r, 400, "invalid_request", "表单字段过长或重复")
				return
			}
			metadata[name] = string(data)
		}
		_ = part.Close()
	}
	kind := protocolType(protocol)
	if metadata["model"] == "" {
		writeClientError(w, r, 400, "invalid_request", "model 为必填字段")
		return
	}
	if err := s.requireModelKind(r, metadata["model"], modelKind(kind)); err != nil {
		writeClientError(w, r, 404, "model_not_found", err.Error())
		return
	}
	body, _ := json.Marshal(metadata)
	s.executeHTTP(w, r, scheduling.ExecutionRequest{Request: scheduling.Request{Model: metadata["model"], Protocol: protocol}, Type: kind, Path: r.URL.Path, Body: body, WireBody: wire, RetrySafe: kind == "image"})
}

// resourceRequest 按成功创建的任务恢复原模型和目标，查询、下载和删除不得重新挑选供应商。
func (s *Server) resourceRequest(w http.ResponseWriter, r *http.Request, protocol, id string) {
	resolver, ok := s.executor.(interface {
		ResourceModel(string, string, string) (string, bool)
	})
	if !ok {
		writeClientError(w, r, 409, "resource_binding_unavailable", "资源原上游绑定无法验证")
		return
	}
	model, ok := resolver.ResourceModel(requestPrincipal(r), protocol, id)
	if !ok {
		writeClientError(w, r, 409, "resource_binding_unavailable", "资源未知、已过期或原上游绑定无法验证")
		return
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	s.executeHTTP(w, r, scheduling.ExecutionRequest{Request: scheduling.Request{Model: model, Protocol: protocol}, Type: protocolType(protocol), Method: r.Method, Path: r.URL.RequestURI(), Body: body, PreviousResponseID: id, RetrySafe: r.Method == http.MethodGet})
}

// videos 处理原生视频创建及绑定任务的状态、下载和删除。
func (s *Server) videos(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/videos" {
		s.multipartRequest(w, r, "openai:video")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/videos/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." || len(parts) == 2 && parts[1] != "content" || r.Method != http.MethodGet && r.Method != http.MethodDelete {
		writeClientError(w, r, 400, "invalid_request", "不支持的视频操作")
		return
	}
	s.resourceRequest(w, r, "openai:video", parts[0])
}

// files 透传 Gemini 文件上传和列表；具名资源操作必须验证创建时的原 Key。
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/upload")
	if strings.HasPrefix(path, "/v1beta/files/") {
		id := strings.TrimPrefix(path, "/v1beta/")
		if strings.Contains(strings.TrimPrefix(id, "files/"), "/") || strings.HasSuffix(id, "/") || r.Method != http.MethodGet && r.Method != http.MethodDelete {
			writeClientError(w, r, 400, "invalid_request", "不支持的文件操作")
			return
		}
		s.resourceRequest(w, r, "gemini:files", id)
		return
	}
	if path != "/v1beta/files" || r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeClientError(w, r, 400, "invalid_request", "不支持的文件操作")
		return
	}
	wire, err := readRequestBody(r)
	if err != nil {
		writeClientError(w, r, bodyErrorStatus(err), "invalid_request", err.Error())
		return
	}
	// 无模型文件接口沿旧版按供应商/Key 选路；路由元信息不包含上传二进制。
	req := scheduling.ExecutionRequest{Request: scheduling.Request{Model: "gemini-files", Protocol: "gemini:files"}, Type: "files", Method: r.Method, Path: r.URL.RequestURI(), Body: []byte(`{"model":"gemini-files"}`), WireBody: wire, RetrySafe: r.Method == http.MethodGet}
	if strings.EqualFold(r.Header.Get("X-Goog-Upload-Protocol"), "resumable") {
		if err := s.prepareUpload(r, &req); err != nil {
			writeClientError(w, r, 500, "upload_setup_failed", err.Error())
			return
		}
	}
	s.executeHTTP(w, r, req)
}

// executeHTTP 统一响应提交和记录生命周期，所有正式 HTTP 业务入口复用同一执行器。
func (s *Server) executeHTTP(w http.ResponseWriter, r *http.Request, req scheduling.ExecutionRequest) {
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	req.Headers = clientHeaders(r.Header)
	req.Principal = requestPrincipal(r)
	response := req.Response
	req.Response = func(status int, headers http.Header) {
		if response != nil {
			response(status, headers)
		}
		copyResponseHeaders(w.Header(), headers)
		w.WriteHeader(status)
	}
	finalized := false
	req.Finalize = func(output io.Writer, result scheduling.ExecutionResult, failure error) {
		finalized = true
		finishHTTP(w, r, output, result, failure)
	}
	result, err := s.executor.Execute(r.Context(), req, w)
	if !finalized {
		req.Finalize(w, result, err)
	}
}

// copyResponseHeaders 保留限流、请求标识及媒体类型，过滤凭据、逐跳头和与聚合正文可能不一致的长度。
func copyResponseHeaders(destination, source http.Header) {
	blocked := map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true, "x-api-key": true, "x-goog-api-key": true, "content-length": true, "content-encoding": true, "connection": true, "transfer-encoding": true, "keep-alive": true, "upgrade": true, "trailer": true, "te": true}
	for _, name := range strings.Split(source.Get("Connection"), ",") {
		blocked[strings.ToLower(strings.TrimSpace(name))] = true
	}
	for name, values := range source {
		if !blocked[strings.ToLower(name)] {
			destination[name] = append([]string(nil), values...)
		}
	}
}

// requireModelKind 确认统一模型存在且类型适配实际入口，不让媒体请求借用对话模型权限。
func (s *Server) requireModelKind(r *http.Request, name, kind string) error {
	items, err := s.catalog.List(r.Context())
	if err != nil {
		return errors.New("读取模型列表失败")
	}
	for _, item := range items {
		if item.ModelID == name && item.Kind == kind {
			return nil
		}
	}
	return errors.New("模型不存在或不支持当前业务类型")
}
