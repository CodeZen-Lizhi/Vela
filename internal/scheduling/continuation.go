package scheduling

import (
	"crypto/sha256"
	"sync"
	"time"
)

// Continuations 保存本进程已成功响应的短期绑定；重启或过期后拒绝续接，不猜测备用目标。
type Continuations struct {
	// mu 保护带容量及期限的注册表；键是请求主体与响应标识的摘要，不保存原始响应 ID。
	mu      sync.Mutex
	entries map[[32]byte]continuationBinding
}

// continuationBinding 绑定逻辑模型、协议、上游地址、模型及具体凭据，任一变化均不可续接。
type continuationBinding struct {
	// provider、key、version、url、upstream、model、protocol 是成功执行时的不可替代目标身份。
	provider, key, version, url, upstream, model, protocol string
	// expires 沿用 Aether 的 24 小时短期所有权期限；conflict 拒绝不同目标返回的重复响应 ID。
	expires  time.Time
	conflict bool
	// websocket 隔离 HTTP 与 WebSocket 所有权，不能把两种传输的上游状态相互冒用。
	websocket bool
}

// NewContinuations 创建独立的内存所有权表，不将上游响应标识或凭据明文持久化。
func NewContinuations() *Continuations {
	return &Continuations{entries: make(map[[32]byte]continuationBinding)}
}

// remember 只登记完整成功的响应，沿用 Aether 每主体最多 1024 条的有界原则（本机全表更严格）。
func (s *Continuations) remember(req ExecutionRequest, c Candidate, id string) {
	if id == "" || len(id) > 256 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, item := range s.entries {
		if !now.Before(item.expires) {
			delete(s.entries, key)
		}
	}
	key := sha256.Sum256([]byte(req.Principal + "\x00" + id))
	value := continuationBinding{provider: c.Provider.ID, key: c.Key.ID, version: c.CredentialVersion, url: c.Provider.URL, upstream: c.Model, model: req.Request.Model, protocol: req.Request.Protocol, expires: now.Add(24 * time.Hour), websocket: req.WebSocket != nil}
	if old, ok := s.entries[key]; ok {
		value.conflict = old.conflict || !old.matches(req, c)
	}
	_, replacing := s.entries[key]
	if !replacing && len(s.entries) >= 1024 {
		var oldest [32]byte
		var expiry time.Time
		for k, item := range s.entries {
			if expiry.IsZero() || item.expires.Before(expiry) {
				oldest, expiry = k, item.expires
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[key] = value
}

// matches 核对完整执行目标；请求主体已由注册表键隔离。
func (b continuationBinding) matches(req ExecutionRequest, c Candidate) bool {
	return !b.conflict && b.websocket == (req.WebSocket != nil) && b.provider == c.Provider.ID && b.key == c.Key.ID && b.version == c.CredentialVersion && b.url == c.Provider.URL && b.upstream == c.Model && b.model == req.Request.Model && b.protocol == req.Request.Protocol
}

// resolve 只返回仍可验证的原绑定候选，不选择其他凭据、地址或映射。
func (s *Continuations) resolve(req ExecutionRequest, candidates []Candidate) []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sha256.Sum256([]byte(req.Principal + "\x00" + req.PreviousResponseID))
	binding, ok := s.entries[key]
	if !ok || !time.Now().Before(binding.expires) {
		delete(s.entries, key)
		return nil
	}
	for _, c := range candidates {
		if binding.matches(req, c) {
			return []Candidate{c}
		}
	}
	return nil
}
