package scheduling

import (
	"crypto/sha256"
	"time"
)

// ResourceModel 只读取同一认证主体和协议创建的资源绑定，供无 model 字段的原生查询恢复身份。
func (e *Executor) ResourceModel(principal, protocol, id string) (string, bool) {
	if e.Continuations == nil || id == "" || len(id) > 256 {
		return "", false
	}
	s := e.Continuations
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.entries[sha256.Sum256([]byte(principal+"\x00"+id))]
	if !ok || binding.conflict || binding.protocol != protocol || !time.Now().Before(binding.expires) {
		return "", false
	}
	return binding.model, true
}

// rememberResources 将完整成功的响应及列表资源登记到实际原目标，失败尝试不产生可用绑定。
func (e *Executor) rememberResources(req ExecutionRequest, c Candidate, outcome AttemptResult) {
	if e.Continuations == nil {
		return
	}
	if req.Request.Protocol != "openai:responses" && req.Request.Protocol != "responses" && req.Type != "video" && req.Type != "files" && req.Type != "interactions" {
		return
	}
	e.Continuations.remember(req, c, outcome.ResponseID)
	for _, id := range outcome.ResourceIDs {
		e.Continuations.remember(req, c, id)
	}
}
