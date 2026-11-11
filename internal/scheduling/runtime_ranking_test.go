package scheduling

import (
	"errors"
	"testing"
	"time"
)

// runtimeRankKeys 读取真实 Rank 输出，测试只核对业务可观察次序。
func runtimeRankKeys(r *Runtime, p SchedulingPolicy, req Request, cs ...Candidate) []string {
	result := r.Rank(p, req, cs)
	ids := make([]string, len(result))
	for i, c := range result {
		ids[i] = c.Key.ID
	}
	return ids
}

// requireRuntimeOrder 比较候选次序并保留调用点。
func requireRuntimeOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rank %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("rank %v, want %v", got, want)
		}
	}
}

// TestRuntimeStrategies 验证倍率、供应商顺序、Key优先级的作用层级以及正健康分不参与排序。
func TestRuntimeStrategies(t *testing.T) {
	r, _ := runtimeClock()
	zero, one, two := 0.0, 1.0, 2.0
	a := runtimeCandidate("a", 0, 0, &two)
	b := runtimeCandidate("b", 1, 0, &one)
	c := runtimeCandidate("c", 2, 0, &zero)
	d := runtimeCandidate("d", 3, 0, nil)
	fixed := SchedulingPolicy{Strategy: FixedOrder}
	requireRuntimeOrder(t, runtimeRankKeys(r, fixed, Request{}, d, c, b, a), "a", "b", "c", "d")
	fixed.ProviderOrder = []string{d.Provider.ID, c.Provider.ID, b.Provider.ID, a.Provider.ID}
	requireRuntimeOrder(t, runtimeRankKeys(r, fixed, Request{}, a, b, c, d), "d", "c", "b", "a")
	cost := SchedulingPolicy{Strategy: CostBased}
	requireRuntimeOrder(t, runtimeRankKeys(r, cost, Request{}, a, b, c, d), "c", "b", "a", "d")
	// 同供应商同优先级：固定策略按身份，主链亲和策略也不按正健康分重排。
	e := runtimeCandidate("e", 0, 0, &two)
	e.Provider = a.Provider
	e.Key.ProviderID = a.Provider.ID
	before := runtimeRankKeys(r, SchedulingPolicy{Strategy: CacheAffinity}, Request{Order: 100}, e, a)
	finishRuntime(t, r, a, Outcome{Penalty: Heavy})
	requireRuntimeOrder(t, runtimeRankKeys(r, SchedulingPolicy{Strategy: FixedOrder}, Request{}, e, a), "a", "e")
	requireRuntimeOrder(t, runtimeRankKeys(r, SchedulingPolicy{Strategy: CacheAffinity}, Request{Order: 100}, e, a), before...)
	e.Key.Priority = 1
	requireRuntimeOrder(t, runtimeRankKeys(r, SchedulingPolicy{Strategy: CacheAffinity}, Request{}, e, a), "a", "e")
}

// TestRuntimeAffinityScopesAndExpiry 验证完整成功续期、固定5分钟、无会话不共用和备用迁移。
func TestRuntimeAffinityScopesAndExpiry(t *testing.T) {
	r, now := runtimeClock()
	a, b := runtimeCandidate("a", 0, 0, nil), runtimeCandidate("b", 1, 0, nil)
	b.Key.CacheMinutes = 0 // 旧Key TTL配置不影响新版固定5分钟绑定。
	p := SchedulingPolicy{Strategy: CacheAffinity, Revision: 1}
	req := Request{Session: "s", Model: "m", Protocol: "chat"}
	cs := r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, cs[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "b", "a")
	for _, other := range []Request{{Session: "other", Model: "m", Protocol: "chat"}, {Session: "s", Model: "other", Protocol: "chat"}, {Session: "s", Model: "m", Protocol: "other"}, {Model: "m", Protocol: "chat"}} {
		requireRuntimeOrder(t, runtimeRankKeys(r, p, other, a, b), "a", "b")
	}
	*now = now.Add(4 * time.Minute)
	cs = r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, cs[0], Outcome{Success: true})
	*now = now.Add(4 * time.Minute)
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "b", "a")
	*now = now.Add(time.Minute)
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	cs = r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, cs[1], Outcome{})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	finishRuntime(t, r, cs[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "b", "a")
	// 绑定不越过真实准入，目标限流时继续使用可用备用，备用完整成功后迁移。
	b.Key.ConcurrencyLimit = 1
	active, err := r.Acquire(b)
	if err != nil {
		t.Fatal(err)
	}
	cs = r.Rank(p, req, []Candidate{a, b})
	requireRuntimeOrder(t, []string{cs[0].Key.ID}, "a")
	finishRuntime(t, r, cs[0], Outcome{Success: true})
	active.Finish(Outcome{})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
}

// TestRuntimeAffinityRevisionAndLateRequest 验证新配置失效、迟到与重复成功不能覆盖较新绑定。
func TestRuntimeAffinityRevisionAndLateRequest(t *testing.T) {
	r, _ := runtimeClock()
	a, b := runtimeCandidate("a", 0, 0, nil), runtimeCandidate("b", 1, 0, nil)
	p := SchedulingPolicy{Strategy: CacheAffinity, Revision: 1}
	req := Request{Session: "s", Model: "m", Protocol: "chat", Order: 1}
	old := r.Rank(p, req, []Candidate{a, b})
	req.Order = 2
	newer := r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, newer[0], Outcome{Success: true})
	finishRuntime(t, r, old[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	// 同一逻辑请求中重复成功也不能移走绑定。
	finishRuntime(t, r, newer[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	req.Order = 3
	old = r.Rank(p, req, []Candidate{a, b})
	p.Revision = 2
	runtimeRankKeys(r, p, req, a, b)
	finishRuntime(t, r, old[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	stale := p
	stale.Revision = 1
	if len(r.Rank(stale, req, []Candidate{a, b})) != 0 {
		t.Fatal("old config must not roll back revision")
	}
	req.Order = 4
	cs := r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, cs[1], Outcome{Success: true})
	b.CredentialVersion = "v2"
	b.ConfigVersion = 2
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
}

// TestRuntimeCostAffinityAndUnknownException 验证亲和只在已知同价打破平局，未知倍率忽略亲和。
func TestRuntimeCostAffinityAndUnknownException(t *testing.T) {
	r, _ := runtimeClock()
	price := 1.0
	a, b := runtimeCandidate("a", 0, 0, &price), runtimeCandidate("b", 1, 0, &price)
	p := SchedulingPolicy{Strategy: CostBased}
	req := Request{Session: "s"}
	cs := r.Rank(p, req, []Candidate{a, b})
	finishRuntime(t, r, cs[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "b", "a")
	zero := 0.0
	a.Key.Multiplier = &zero
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	a.Key.Multiplier = nil
	b.Key.Multiplier = nil
	requireRuntimeOrder(t, runtimeRankKeys(r, p, req, a, b), "a", "b")
	// 固定顺序既不读已有绑定，也不能写入缓存亲和范围。
	fixed := SchedulingPolicy{Strategy: FixedOrder}
	cs = r.Rank(fixed, req, []Candidate{a, b})
	finishRuntime(t, r, cs[1], Outcome{Success: true})
	requireRuntimeOrder(t, runtimeRankKeys(r, fixed, req, a, b), "a", "b")
	requireRuntimeOrder(t, runtimeRankKeys(r, SchedulingPolicy{Strategy: CacheAffinity}, req, a, b), "a", "b")
}

// TestRuntimeRotationRetainsAdmission 验证凭据轮换不绕过仍在途请求的并发与已消费RPM。
func TestRuntimeRotationRetainsAdmission(t *testing.T) {
	r, _ := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	c.Key.ConcurrencyLimit = 1
	c.Key.RPMLimit = 1
	old, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	c.CredentialVersion = "v2"
	c.ConfigVersion = 2
	if len(r.Rank(SchedulingPolicy{Strategy: FixedOrder}, Request{}, []Candidate{c})) != 0 {
		t.Fatal("rotation must retain inflight")
	}
	if _, err = r.Acquire(c); !errors.Is(err, ErrConcurrency) {
		t.Fatalf("rotated concurrency: %v", err)
	}
	old.Finish(Outcome{Penalty: Unavailable})
	if _, err = r.Acquire(c); !errors.Is(err, ErrRPM) {
		t.Fatalf("rotated RPM: %v", err)
	}
	h := r.Health("a", "v2")
	if h.Score != 10 || h.Inflight != 0 {
		t.Fatalf("rotation state: %+v", h)
	}
}

// TestRuntimeSnapshotFenceWithoutPolicyChange 验证同一策略版本内旧配置快照不会回滚凭据，包含首次见到的旧版本。
func TestRuntimeSnapshotFenceWithoutPolicyChange(t *testing.T) {
	r, _ := runtimeClock()
	old := runtimeCandidate("a", 0, 0, nil)
	old.ConfigVersion = 1
	newer := old
	newer.CredentialVersion = "v2"
	newer.ConfigVersion = 2
	p := SchedulingPolicy{Strategy: FixedOrder, Revision: 1}
	if len(r.Rank(p, Request{}, []Candidate{newer})) != 1 {
		t.Fatal("new snapshot must register")
	}
	if len(r.Rank(p, Request{}, []Candidate{old})) != 0 {
		t.Fatal("late unseen credential must not replace newer snapshot")
	}
	finishRuntime(t, r, newer, Outcome{Penalty: Heavy})
	if h := r.Health("a", "v2"); h.Score != 8 {
		t.Fatalf("new credential health: %+v", h)
	}
	old.ConfigVersion = 2
	if len(r.Rank(p, Request{}, []Candidate{old})) != 0 {
		t.Fatal("conflicting versions within same snapshot must reject")
	}
	newer.ConfigVersion = 3
	if len(r.Rank(p, Request{}, []Candidate{newer})) != 1 {
		t.Fatal("same credential should update source watermark")
	}
	old.ConfigVersion = 2
	if len(r.Rank(p, Request{}, []Candidate{old})) != 0 {
		t.Fatal("old source watermark must remain fenced")
	}
	if h := r.Health("a", "v2"); h.Score != 8 {
		t.Fatalf("rejected rank must not reset health: %+v", h)
	}
}
