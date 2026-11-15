package scheduling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// compactionWaitSeconds 为原生压缩保留默认 20 分钟等待；显式请求超时覆盖该默认值。
func compactionWaitSeconds(c Candidate) int {
	if seconds := c.Provider.Request.RequestSeconds; seconds != 0 {
		return seconds
	}
	return 1200
}

// isCompactionRequest 只识别原生 compact 协议和顶层 input 中的压缩触发项，不猜测用户文本。
func isCompactionRequest(req ExecutionRequest) bool {
	if req.Request.Protocol == "openai:responses:compact" {
		return true
	}
	if req.Request.Protocol != "openai:responses" && req.Request.Protocol != "responses" {
		return false
	}
	var body struct {
		// Input 只读取原生输入项，嵌套工具输出中的同名文本不构成压缩请求。
		Input []struct {
			// Type 是原生输入项类型。
			Type string `json:"type"`
		} `json:"input"`
	}
	if json.Unmarshal(req.Body, &body) != nil {
		return false
	}
	for _, item := range body.Input {
		if item.Type == "compaction_trigger" {
			return true
		}
	}
	return false
}

// compactionReference 只为包含非空密文的原生压缩项生成带命名空间的摘要，不保留密文本身。
func compactionReference(item map[string]any) string {
	if item["type"] != "compaction" || !nonemptyText(item["encrypted_content"]) {
		return ""
	}
	digest := sha256.Sum256([]byte(item["encrypted_content"].(string)))
	return "compaction:" + hex.EncodeToString(digest[:])
}

// responseCompactions 提取完整响应中的压缩摘要；普通文字、推理和工具结果不冒充压缩项。
func responseCompactions(body []byte) []string {
	var document map[string]any
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	var refs []string
	items, _ := document["output"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if ref := compactionReference(item); ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return refs
}

// resolveCompactions 对本进程已知且有效的压缩摘要固定原目标；未知外部摘要保持原生透传，不虚构归属。
// 复用续接表的 24 小时和 1024 条上限，不扩大为持久化密文或新的一套会话存储。
func (s *Continuations) resolveCompactions(req ExecutionRequest, candidates []Candidate) ([]Candidate, bool) {
	var body struct {
		// Input 只检查顶层原生压缩项，不能从用户或工具文本中提取绑定。
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(req.Body, &body) != nil {
		return candidates, false
	}
	req.Request.Protocol, req.WebSocket = "openai:responses", nil
	s.mu.Lock()
	defer s.mu.Unlock()
	bound := false
	for _, item := range body.Input {
		ref := compactionReference(item)
		if ref == "" {
			continue
		}
		key := sha256.Sum256([]byte(req.Principal + "\x00" + ref))
		binding, ok := s.entries[key]
		if !ok || !time.Now().Before(binding.expires) {
			continue
		}
		bound = true
		filtered := make([]Candidate, 0, len(candidates))
		for _, c := range candidates {
			if binding.matches(req, c) {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}
	return candidates, bound
}
