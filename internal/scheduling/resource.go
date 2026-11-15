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

// rememberResources 将完整成功的响应、原生压缩摘要及列表资源登记到实际原目标，失败不建立绑定。
func (e *Executor) rememberResources(req ExecutionRequest, c Candidate, outcome AttemptResult) {
	if e.Continuations == nil {
		return
	}
	if len(outcome.compactions) > 0 && (req.Request.Protocol == "openai:responses" || req.Request.Protocol == "responses" || req.Request.Protocol == "openai:responses:compact") {
		compactReq := req
		// 原生密文不依赖 HTTP/WS 传输方式；compact 附属操作与后续 Responses 使用同一归属。
		compactReq.Request.Protocol, compactReq.WebSocket = "openai:responses", nil
		for _, ref := range outcome.compactions {
			e.Continuations.remember(compactReq, c, ref)
		}
	}
	if req.Request.Protocol != "openai:responses" && req.Request.Protocol != "responses" && req.Type != "video" && req.Type != "files" && req.Type != "interactions" {
		return
	}
	e.Continuations.remember(req, c, outcome.ResponseID)
	for _, id := range outcome.ResourceIDs {
		e.Continuations.remember(req, c, id)
	}
}
