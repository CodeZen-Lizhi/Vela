package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// uploadBinding 仅保存当前主体的分段上传原路径，不能由客户端提供任意上游 URL。
type uploadBinding struct {
	// principal、path、expires 为认证摘要、已由出站器校验的相对路径和有效期。
	principal string
	path      string
	expires   time.Time
}

// prepareUpload 按本次接入的本机监听地址改写 resumable URL，拒绝使用客户端 Host，后续上传仍经过原 Key 校验。
func (s *Server) prepareUpload(r *http.Request, req *scheduling.ExecutionRequest) error {
	address, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || address == nil || !address.IP.IsLoopback() || address.Port <= 0 {
		return errors.New("无法确定上传会话的本机监听地址")
	}
	baseURL := "http://" + address.String()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("无法创建上传会话")
	}
	id := hex.EncodeToString(random[:])
	req.ResourceID = id
	principal := requestPrincipal(r)
	req.Response = func(_ int, headers http.Header) {
		target, err := url.Parse(headers.Get("X-Goog-Upload-URL"))
		if err != nil || target == nil || target.Path == "" {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.uploads == nil {
			s.uploads = make(map[string]uploadBinding)
		}
		now := time.Now()
		for key, binding := range s.uploads {
			if !now.Before(binding.expires) {
				delete(s.uploads, key)
			}
		}
		if len(s.uploads) >= 1024 {
			var oldest string
			var expiry time.Time
			for key, binding := range s.uploads {
				if expiry.IsZero() || binding.expires.Before(expiry) {
					oldest, expiry = key, binding.expires
				}
			}
			delete(s.uploads, oldest)
		}
		s.uploads[id] = uploadBinding{principal: principal, path: target.RequestURI(), expires: now.Add(24 * time.Hour)}
		headers.Set("X-Goog-Upload-URL", baseURL+"/upload/v1beta/files/"+id)
	}
	return nil
}

// uploadChunk 验证本地上传 ID 和认证主体后，原样发送上传命令、偏移量及二进制分段。
func (s *Server) uploadChunk(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/upload/v1beta/files/")
	principal := requestPrincipal(r)
	s.mu.Lock()
	binding, ok := s.uploads[id]
	s.mu.Unlock()
	if !ok || binding.principal != principal || !time.Now().Before(binding.expires) || r.Method != http.MethodPost {
		writeClientError(w, r, 409, "upload_binding_unavailable", "上传会话未知、已过期或身份不匹配")
		return
	}
	wire, err := readRequestBody(r)
	if err != nil {
		writeClientError(w, r, bodyErrorStatus(err), "invalid_request", err.Error())
		return
	}
	s.executeHTTP(w, r, scheduling.ExecutionRequest{Request: scheduling.Request{Model: "gemini-files", Protocol: "gemini:files"}, Type: "files", Method: r.Method, Path: binding.path, Body: []byte(`{"model":"gemini-files"}`), WireBody: wire, PreviousResponseID: id, RetrySafe: false})
}
