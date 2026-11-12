package scheduling

import (
	"io"
	"net/http"
	"strings"
)

// forwardBusinessHeaders 保留标准业务、版本、幂等和追踪头，剔除客户端凭据、Cookie、压缩及逐跳传输状态。
func forwardBusinessHeaders(dst, src http.Header) {
	blocked := map[string]bool{}
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Cookie", "Cookie2", "Set-Cookie", "X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Host", "Content-Length", "Content-Type", "Content-Encoding", "Accept-Encoding", "Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
		blocked[http.CanonicalHeaderKey(name)] = true
	}
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			blocked[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		if blocked[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// httpCommitWriter 在首次真实写出时传播成功状态和响应头，提交前的预读失败不触碰客户端。
type httpCommitWriter struct {
	// destination 是客户端采集出口；status、headers 是本次上游成功响应及客户端实际格式。
	destination io.Writer
	status      int
	headers     http.Header
	// respond 在首次写入前调用一次，committed 防止后续 SSE 增量重复提交头部。
	respond   func(int, http.Header)
	committed bool
}

// Write 在协议校验允许写出后提交头部，随后保留原出口的写入和记录语义。
func (w *httpCommitWriter) Write(p []byte) (int, error) {
	if !w.committed {
		w.committed = true
		w.respond(w.status, w.headers)
	}
	return w.destination.Write(p)
}

// Flush 保留 SSE 的逐段刷新能力，空前奏不触发状态提交。
func (w *httpCommitWriter) Flush() {
	if flusher, ok := w.destination.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}
