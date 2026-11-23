package scheduling

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// keyState 保存当前凭据的健康、最近一分钟实际准入和试探所有权。
type keyState struct {
	// version 是当前凭据版本，不保存凭据本身。
	version string
	// configVersion 是最近观察到的源快照序号，拒绝迟到旧快照回滚凭据。
	configVersion uint64
	// health 是整 Key 共享状态。
	health KeyHealth
	// usage 在凭据更换前后共享整 Key 的实际额度，旧请求仍占并发。
	usage *keyUsage
	// probe 是当前唯一恢复尝试，释放必须核对所有者。
	probe *Attempt
}

// keyUsage 保存整 Key 的实际请求资源，凭据轮换不绕过准入限制。
type keyUsage struct {
	// inflight 是尚未 Finish 的真实尝试数，跨凭据代次共用。
	inflight int
	// admitted 是滚动一分钟内取得实际资格的时刻。
	admitted []time.Time
}

// NewRuntime 创建无后台任务的调度核心；持久化状态须在使用前通过 RestoreHealth 恢复。
func NewRuntime() *Runtime {
	return &Runtime{now: time.Now, keys: make(map[string]*keyState), affinities: make(map[affinityScope]affinityEntry)}
}

// SetHealthChanged 注册健康状态变化通知。回调可以重读运行状态，也可能被多个请求同时调用。
func (r *Runtime) SetHealthChanged(notify func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthChanged = notify
}

// notifyHealthChanged 在手动恢复成功后通知宿主重读指定 Key。
func (r *Runtime) notifyHealthChanged(keyID string) {
	r.mu.Lock()
	notify := r.healthChanged
	r.mu.Unlock()
	if notify != nil {
		notify(keyID)
	}
}

// stateForRank 登记源快照凭据，只有严格更新的快照才能替换凭据；旧快照返回 nil，调用方持锁。
func (r *Runtime) stateForRank(c Candidate) *keyState {
	s := r.keys[c.Key.ID]
	if s != nil && s.version != c.CredentialVersion && c.ConfigVersion <= s.configVersion {
		return nil
	}
	if s == nil || s.version != c.CredentialVersion {
		usage := &keyUsage{}
		if s != nil {
			usage = s.usage
		}
		s = &keyState{version: c.CredentialVersion, configVersion: c.ConfigVersion, health: KeyHealth{Score: 10}, usage: usage}
		r.keys[c.Key.ID] = s
	}
	s.configVersion = max(s.configVersion, c.ConfigVersion)
	return s
}

// candidateEnabled 检查可在快照中确认的配置，模型权限和实时 Secret 校验由候选源与 transport 负责。
func candidateEnabled(c Candidate) bool {
	return c.Provider.Enabled && c.Key.Enabled && c.Key.HasSecret && c.Key.ID != "" && c.Provider.ID != "" && c.Key.ProviderID == c.Provider.ID && c.CredentialVersion != ""
}

// unavailable 在同一锁内复核滚动 RPM、并发、冷却和单探测资格；调用方持锁。
func (s *keyState) unavailable(c Candidate, now time.Time) error {
	if !candidateEnabled(c) || s.version != c.CredentialVersion {
		return ErrUnavailable
	}
	i := 0
	for i < len(s.usage.admitted) && !s.usage.admitted[i].After(now.Add(-time.Minute)) {
		i++
	}
	s.usage.admitted = s.usage.admitted[i:]
	if c.Key.ConcurrencyLimit > 0 && s.usage.inflight >= c.Key.ConcurrencyLimit {
		return ErrConcurrency
	}
	if c.Key.RPMLimit > 0 && len(s.usage.admitted) >= c.Key.RPMLimit {
		return ErrRPM
	}
	if now.Before(s.health.CooldownUntil) {
		return ErrCooldown
	}
	if s.probe != nil {
		return ErrCircuit
	}
	if s.health.CircuitOpen && now.Before(s.health.NextProbe) {
		return ErrCircuit
	}
	return nil
}

// Acquire 在实际出站前原子取得整 Key 准入；Rank 仅排序，不占用 RPM、并发或探测资格。
// 允许直接准入首次使用的候选，但已有 Key 的凭据只能由当前配置的 Rank 更新。
func (r *Runtime) Acquire(c Candidate) (*Attempt, error) {
	r.mu.Lock()
	var notify func(string)
	defer func() {
		r.mu.Unlock()
		if notify != nil {
			notify(c.Key.ID)
		}
	}()
	if !candidateEnabled(c) {
		return nil, ErrUnavailable
	}
	s := r.keys[c.Key.ID]
	if s == nil {
		s = r.stateForRank(c)
	}
	now := r.now()
	if err := s.unavailable(c, now); err != nil {
		return nil, err
	}
	a := &Attempt{runtime: r, candidate: c, state: s, generation: s.health.Generation, probe: s.health.CircuitOpen || !s.health.CooldownUntil.IsZero()}
	if a.probe {
		s.probe = a
		s.health.Probing = true
		notify = r.healthChanged
	}
	s.usage.inflight++
	// 无 RPM 限制时无需保留无限实际请求历史；保留最近一分钟供之后设置上限使用。
	s.usage.admitted = append(s.usage.admitted, now)
	return a, nil
}

// Finish 幂等释放实际准入，只有当前凭据和熔断代次的结果能够计分和写亲和。
// 调用方应 defer Finish(Outcome{}) 覆盖取消等中性退出，并在完整终态时先调用 Finish(实际结果)。
func (a *Attempt) Finish(outcome Outcome) {
	a.once.Do(func() {
		r := a.runtime
		r.mu.Lock()
		s := a.state
		before := s.health
		defer func() {
			after := s.health
			changed := r.keys[a.candidate.Key.ID] == s &&
				(before.Score != after.Score || before.Probing != after.Probing ||
					before.CircuitOpen != after.CircuitOpen || !before.NextProbe.Equal(after.NextProbe) ||
					!before.CooldownUntil.Equal(after.CooldownUntil))
			notify := r.healthChanged
			r.mu.Unlock()
			if changed && notify != nil {
				notify(a.candidate.Key.ID)
			}
		}()
		s.usage.inflight--
		ownsProbe := s.probe == a
		if ownsProbe {
			s.probe = nil
			s.health.Probing = false
		}
		if r.keys[a.candidate.Key.ID] != s || s.health.Generation != a.generation || (a.probe && !ownsProbe) {
			return
		}
		now := r.now()
		if outcome.Success {
			s.health.Score = math.Min(10, s.health.Score+1)
			s.health.ConsecutiveFailures = 0
			if s.health.CircuitOpen {
				s.health.Generation++
			}
			s.health.CircuitOpen = false
			s.health.ProbeInterval = 0
			s.health.NextProbe = time.Time{}
			s.health.CooldownUntil = time.Time{}
			s.health.ConsecutiveRateLimits = 0
			r.remember(a.candidate, now)
			return
		}
		if outcome.RateLimited || outcome.RetryAfterSet || outcome.RetryAfter > 0 {
			s.cooldown(outcome, now)
		}
		if outcome.Penalty < Light || outcome.Penalty > Unavailable {
			return
		}
		s.health.ConsecutiveFailures++
		extra := 0
		if s.health.ConsecutiveFailures >= 5 {
			extra = 2
		} else if s.health.ConsecutiveFailures >= 3 {
			extra = 1
		}
		points := 1
		if outcome.Penalty == Heavy {
			points = 2
		}
		s.health.Score = math.Max(0, s.health.Score-float64(points+extra))
		if outcome.Penalty == Unavailable {
			s.health.Score = 0
		}
		if s.health.Score == 0 {
			interval := time.Minute
			if s.health.CircuitOpen {
				interval = 2 * s.health.ProbeInterval
			}
			maxMinutes := a.candidate.Key.ProbeMinutes
			if maxMinutes == 0 {
				maxMinutes = 10
			}
			maxMinutes = max(1, min(32, maxMinutes))
			s.health.ProbeInterval = min(interval, time.Duration(maxMinutes)*time.Minute)
			s.health.CircuitOpen = true
			s.health.Generation++
			s.health.NextProbe = now.Add(s.health.ProbeInterval)
		}
	})
}

// cooldown 继承旧版限流退避，保留更晚的已有冷却但不超过本次观察后 600 秒；调用方持锁。
func (s *keyState) cooldown(outcome Outcome, now time.Time) {
	s.health.ConsecutiveRateLimits++
	delay := outcome.RetryAfter
	if delay < 0 || (delay == 0 && !outcome.RetryAfterSet) {
		delay = time.Duration(min(60, 1<<min(6, s.health.ConsecutiveRateLimits-1))) * time.Second
	}
	until := now.Add(min(delay, 600*time.Second))
	s.health.CooldownUntil = maxTime(until, minTime(s.health.CooldownUntil, now.Add(600*time.Second)))
}

// maxTime 返回较晚时刻，用于保留已有有效冷却。
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// minTime 返回较早时刻，用于限制冷却窗口。
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Health 返回指定当前凭据的健康快照；无记录或凭据不匹配返回新 Key 的 10 分，不修改运行状态。
func (r *Runtime) Health(keyID, version string) KeyHealth {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.keys[keyID]; s != nil && s.version == version {
		h := s.health
		h.Inflight = s.usage.inflight
		return h
	}
	return KeyHealth{Score: 10}
}

// resetHealth 将被手动恢复的当前凭据重置为满分，返回旧状态供持久化失败时回滚。
// 熔断代次必须递增，恢复前准入的旧请求即使稍后完成也不能覆盖这次恢复。
func (r *Runtime) resetHealth(keyID, version string) (KeyHealth, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.keys[keyID]
	if s == nil || s.version != version {
		return KeyHealth{}, false
	}
	previous := s.health
	s.health = KeyHealth{Score: 10, Generation: previous.Generation + 1}
	// 已取得的恢复试探仍会正常释放并发计数，但其结果不再属于新的健康代次。
	s.probe = nil
	return previous, true
}

// rollbackHealth 在恢复后的持久化失败时还原旧分数，并再次递增代次隔离期间的新请求。
func (r *Runtime) rollbackHealth(keyID, version string, previous KeyHealth) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.keys[keyID]
	if s == nil || s.version != version {
		return
	}
	previous.Generation = max(previous.Generation+1, s.health.Generation)
	previous.Inflight = 0
	previous.Probing = false
	s.health = previous
	s.probe = nil
}

// ExportHealth 导出当前凭据的持久化快照；调用方负责 Finish 后串行保存，避免旧快照覆盖新状态。
// 在途数及单探测资格只在当前进程有效，导出不会延长或释放它们。
func (r *Runtime) ExportHealth() []HealthRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make([]HealthRecord, 0, len(r.keys))
	for id, s := range r.keys {
		h := s.health
		h.Inflight = 0
		h.Probing = false
		records = append(records, HealthRecord{KeyID: id, CredentialVersion: s.version, Health: h})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].KeyID < records[j].KeyID })
	return records
}

// RestoreHealth 仅在全新 Runtime 首次使用前恢复持久化状态；任何非法或重复记录使整批拒绝。
// 不恢复进程内 RPM 和探测占用，熔断分数及已安排的等待不会因重启清空。
func (r *Runtime) RestoreHealth(records []HealthRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.keys) != 0 || r.order != 0 {
		return fmt.Errorf("运行核心已使用，不能恢复健康快照")
	}
	states := make(map[string]*keyState, len(records))
	for _, record := range records {
		h := record.Health
		if record.KeyID == "" || record.CredentialVersion == "" || math.IsNaN(h.Score) || math.IsInf(h.Score, 0) || h.Score < 0 || h.Score > 10 || h.ConsecutiveFailures < 0 || h.ConsecutiveRateLimits < 0 || h.CircuitOpen != (h.Score == 0) || (h.CircuitOpen && (h.ProbeInterval < time.Minute || h.ProbeInterval > 32*time.Minute || h.NextProbe.IsZero())) {
			return fmt.Errorf("Key %q 的健康快照无效", record.KeyID)
		}
		if _, exists := states[record.KeyID]; exists {
			return fmt.Errorf("Key %q 的健康快照重复", record.KeyID)
		}
		h.Inflight = 0
		h.Probing = false
		states[record.KeyID] = &keyState{version: record.CredentialVersion, health: h, usage: &keyUsage{}}
	}
	r.keys = states
	return nil
}

// UnavailableReason 在不取得资格或占用额度的情况下读取候选的当前准入原因，供请求记录展示。
func (r *Runtime) UnavailableReason(c Candidate) error {
	r.mu.Lock()
	var notify func(string)
	defer func() {
		r.mu.Unlock()
		if notify != nil {
			notify(c.Key.ID)
		}
	}()
	if !candidateEnabled(c) {
		return ErrUnavailable
	}
	state := r.stateForRank(c)
	if state == nil {
		return ErrUnavailable
	}
	return state.unavailable(c, r.now())
}
