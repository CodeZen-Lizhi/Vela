package scheduling

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"
)

// affinityScope 是完整亲和身份；没有 Session 时不创建记录。
type affinityScope struct {
	// session、model、protocol 分别隔离会话、逻辑模型与客户端协议。
	session, model, protocol string
	// strategy 隔离缓存亲和和成本优先的绑定。
	strategy SchedulingStrategy
	// revision 隔离成功保存的新配置，旧请求无法复活已失效绑定。
	revision int64
}

// affinityEntry 是最近完整成功的目标和绑定寿命。
type affinityEntry struct {
	// provider、key、version 绑定供应商和具体凭据，替换 Key 凭据后不可命中旧绑定。
	provider, key, version string
	// expires 是从完整成功开始计算的固定 5 分钟期限。
	expires time.Time
	// order 防止较早请求的迟到结果覆盖较新请求的绑定。
	order uint64
}

// rankScope 是 Rank 产生的只读请求上下文，不含秘密。
type rankScope struct {
	// policy 是请求开始时的策略版本，顺序已复制到候选。
	policy SchedulingPolicy
	// affinity 是该请求的完整亲和身份。
	affinity affinityScope
	// order 是请求开始顺序，不是完成顺序。
	order uint64
}

// Rank 过滤当前不可准入的候选并按策略排序；返回副本，内部不修改输入切片。
// 输入必须是当前配置下已完成权限和协议匹配的候选，旧 Revision 返回空列表。
// 同级排序沿用旧版主链：固定顺序按身份，亲和/成本按本次请求种子散列，不按正健康分或压力分摊。
func (r *Runtime) Rank(policy SchedulingPolicy, req Request, candidates []Candidate) []Candidate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if policy.Revision < r.revision || policy.Strategy.Validate() != nil {
		return []Candidate{}
	}
	now := r.now()
	if policy.Revision > r.revision {
		r.revision = policy.Revision
		clear(r.affinities)
	}
	for scope, entry := range r.affinities {
		if !now.Before(entry.expires) {
			delete(r.affinities, scope)
		}
	}
	r.order = max(r.order, req.Order)
	if req.Order == 0 {
		r.order++
		req.Order = r.order
	}
	scope := &rankScope{policy: SchedulingPolicy{Strategy: policy.Strategy, Revision: policy.Revision}, order: req.Order, affinity: affinityScope{session: req.Session, model: req.Model, protocol: req.Protocol, strategy: policy.Strategy, revision: policy.Revision}}
	binding, bound := r.affinities[scope.affinity]
	bound = bound && req.Session != "" && policy.Strategy != FixedOrder
	positions := make(map[string]int, len(policy.ProviderOrder))
	for i, id := range policy.ProviderOrder {
		positions[id] = i
	}
	result := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !candidateEnabled(c) {
			continue
		}
		s := r.stateForRank(c)
		if s == nil || s.unavailable(c, now) != nil {
			continue
		}
		if p, exists := positions[c.Provider.ID]; exists {
			c.ProviderOrder = p
		}
		c.scope = scope
		if c.Key.Multiplier != nil {
			value := *c.Key.Multiplier
			c.Key.Multiplier = &value
		}
		result = append(result, c)
	}
	matches := func(c Candidate) bool {
		return bound && binding.provider == c.Provider.ID && binding.key == c.Key.ID && binding.version == c.CredentialVersion
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		unknown := false
		if policy.Strategy == CostBased {
			am, bm := a.Key.Multiplier, b.Key.Multiplier
			if am == nil && bm != nil {
				return false
			}
			if am != nil && bm == nil {
				return true
			}
			unknown = am == nil && bm == nil
			if !unknown && *am != *bm {
				return *am < *bm
			}
		}
		if policy.Strategy != FixedOrder && !unknown && matches(a) != matches(b) {
			return matches(a)
		}
		if a.ProviderOrder != b.ProviderOrder {
			return a.ProviderOrder < b.ProviderOrder
		}
		if a.Key.Priority != b.Key.Priority {
			return a.Key.Priority < b.Key.Priority
		}
		if policy.Strategy != FixedOrder {
			ha, hb := candidateRankHash(a, req.Order), candidateRankHash(b, req.Order)
			if ha != hb {
				return ha < hb
			}
		}
		if a.Provider.ID != b.Provider.ID {
			return a.Provider.ID < b.Provider.ID
		}
		if a.Key.ID != b.Key.ID {
			return a.Key.ID < b.Key.ID
		}
		return cmp.Compare(a.Model, b.Model) < 0
	})
	return result
}

// candidateRankHash 沿用旧版带请求种子的 SHA-256 同级比较；新版单地址取消 endpoint 维度。
func candidateRankHash(c Candidate, order uint64) uint64 {
	seed := make([]byte, 8)
	binary.BigEndian.PutUint64(seed, order)
	data := append(seed, []byte(":tie:"+c.Provider.ID+":"+c.Key.ID+":"+c.Model)...)
	digest := sha256.Sum256(data)
	return binary.BigEndian.Uint64(digest[:8])
}

// remember 只在当前配置、当前凭据且完整成功后续期或迁移，调用方持锁。
func (r *Runtime) remember(c Candidate, now time.Time) {
	scope := c.scope
	if scope == nil || scope.policy.Strategy == FixedOrder || scope.affinity.session == "" || scope.policy.Revision != r.revision {
		return
	}
	old, exists := r.affinities[scope.affinity]
	if exists && old.order >= scope.order {
		return
	}
	r.affinities[scope.affinity] = affinityEntry{provider: c.Provider.ID, key: c.Key.ID, version: c.CredentialVersion, expires: now.Add(5 * time.Minute), order: scope.order}
}
