package scheduling

import (
	"errors"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// runtimeCandidate 构造不含秘密且可准入的真实配置快照。
func runtimeCandidate(id string, order, priority int, multiplier *float64) Candidate {
	return Candidate{Provider: providers.Provider{ID: "p" + id, Enabled: true}, Key: providers.ProviderKey{ID: id, ProviderID: "p" + id, Enabled: true, HasSecret: true, Priority: priority, Multiplier: multiplier}, Model: "upstream", CredentialVersion: "v1", ConfigVersion: 1, ProviderOrder: order}
}

// runtimeClock 用固定时间运行真实状态机，避免睡眠掩盖时间边界。
func runtimeClock() (*Runtime, *time.Time) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	r := NewRuntime()
	r.now = func() time.Time { return now }
	return r, &now
}

// finishRuntime 通过实际准入结算一次真实尝试，不直接改健康状态。
func finishRuntime(t *testing.T, r *Runtime, c Candidate, outcome Outcome) {
	t.Helper()
	a, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	a.Finish(outcome)
}

// TestRuntimeHealthSequence 验证混合故障共享连续计数、中性保持和完整成功恢复。
func TestRuntimeHealthSequence(t *testing.T) {
	r, _ := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	sequence := []struct {
		// outcome 是实际结算事实，score 和 streak 是可观察健康结果。
		outcome Outcome
		score   float64
		streak  int
	}{{Outcome{Penalty: Light}, 9, 1}, {Outcome{Penalty: Heavy}, 7, 2}, {Outcome{}, 7, 2}, {Outcome{Penalty: Light}, 5, 3}, {Outcome{Success: true}, 6, 0}, {Outcome{Penalty: Heavy}, 4, 1}, {Outcome{Penalty: Light}, 3, 2}, {Outcome{Penalty: Heavy}, 0, 3}}
	for i, step := range sequence {
		finishRuntime(t, r, c, step.outcome)
		h := r.Health(c.Key.ID, c.CredentialVersion)
		if h.Score != step.score || h.ConsecutiveFailures != step.streak || h.Inflight != 0 {
			t.Fatalf("step %d: %+v", i, h)
		}
	}
	if !r.Health("a", "v1").CircuitOpen {
		t.Fatal("zero must open circuit")
	}
	if _, err := r.Acquire(c); !errors.Is(err, ErrCircuit) {
		t.Fatalf("circuit admission: %v", err)
	}
}

// TestRuntimeProbeBackoffAndRecovery 验证零分只由真实请求试探、失败直接重新熔断、默认和显式上限。
func TestRuntimeProbeBackoffAndRecovery(t *testing.T) {
	for _, limit := range []int{0, 1, 32} {
		t.Run("limit_"+strconv.Itoa(limit), func(t *testing.T) {
			r, now := runtimeClock()
			c := runtimeCandidate("a", 0, 0, nil)
			c.Key.ProbeMinutes = limit
			finishRuntime(t, r, c, Outcome{Penalty: Unavailable})
			capMinutes := limit
			if capMinutes == 0 {
				capMinutes = 10
			}
			interval := 1
			for i := 0; i < 7; i++ {
				h := r.Health("a", "v1")
				if h.Score != 0 || h.ProbeInterval != time.Duration(interval)*time.Minute || h.NextProbe != now.Add(h.ProbeInterval) {
					t.Fatalf("step %d: %+v", i, h)
				}
				*now = h.NextProbe.Add(-time.Nanosecond)
				if _, err := r.Acquire(c); !errors.Is(err, ErrCircuit) {
					t.Fatalf("early probe: %v", err)
				}
				*now = h.NextProbe
				a, err := r.Acquire(c)
				if err != nil {
					t.Fatal(err)
				}
				a.Finish(Outcome{Penalty: Light})
				interval = min(2*interval, capMinutes)
			}
			*now = r.Health("a", "v1").NextProbe
			finishRuntime(t, r, c, Outcome{Success: true})
			h := r.Health("a", "v1")
			if h.Score != 1 || h.CircuitOpen || h.ConsecutiveFailures != 0 || h.ProbeInterval != 0 {
				t.Fatalf("probe recovery: %+v", h)
			}
			finishRuntime(t, r, c, Outcome{Success: true})
			if r.Health("a", "v1").Score != 2 {
				t.Fatal("ordinary recovery must add 1")
			}
		})
	}
}

// TestRuntimeSingleProbeAcrossProtocolsAndDuplicateFinish 验证并发真实请求仅获整 Key 单探测且重复完成不重复结算。
func TestRuntimeSingleProbeAcrossProtocolsAndDuplicateFinish(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	finishRuntime(t, r, c, Outcome{Penalty: Unavailable})
	*now = r.Health("a", "v1").NextProbe
	var count atomic.Int32
	var winner *Attempt
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ranked := r.Rank(SchedulingPolicy{Strategy: CacheAffinity}, Request{Protocol: string(rune(i))}, []Candidate{c})
			if len(ranked) == 0 {
				return
			}
			a, err := r.Acquire(ranked[0])
			if err == nil {
				count.Add(1)
				mu.Lock()
				winner = a
				mu.Unlock()
			} else if !errors.Is(err, ErrCircuit) {
				t.Errorf("unexpected admission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("admitted %d probes", count.Load())
	}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); winner.Finish(Outcome{Success: true}) }()
	}
	wg.Wait()
	if h := r.Health("a", "v1"); h.Score != 1 || h.Inflight != 0 || h.Probing {
		t.Fatalf("duplicate finish: %+v", h)
	}
}

// TestRuntimeOldGenerationAndCredential 验证迟到成功不修复新熔断、凭据替换后旧结果不污染新状态。
func TestRuntimeOldGenerationAndCredential(t *testing.T) {
	r, _ := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	late, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	finishRuntime(t, r, c, Outcome{Penalty: Unavailable})
	late.Finish(Outcome{Success: true})
	if h := r.Health("a", "v1"); h.Score != 0 || !h.CircuitOpen || h.Inflight != 0 {
		t.Fatalf("late generation: %+v", h)
	}
	r2, _ := runtimeClock()
	old, err := r2.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	c.CredentialVersion = "v2"
	c.ConfigVersion = 2
	ranked := r2.Rank(SchedulingPolicy{Strategy: CacheAffinity}, Request{Session: "s"}, []Candidate{c})
	if len(ranked) != 1 {
		t.Fatal("replacement should be available")
	}
	old.Finish(Outcome{Penalty: Unavailable})
	if h := r2.Health("a", "v2"); h.Score != 10 || h.Inflight != 0 {
		t.Fatalf("old credential: %+v", h)
	}
	c.CredentialVersion = "v1"
	if _, err := r2.Acquire(c); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale credential: %v", err)
	}
}

// TestRuntimeActualAdmissionLimits 验证排序不消耗额度、失败准入不占 RPM、并发释放与滚动分钟边界。
func TestRuntimeActualAdmissionLimits(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	c.Key.ConcurrencyLimit = 1
	c.Key.RPMLimit = 2
	p := SchedulingPolicy{Strategy: FixedOrder}
	for i := 0; i < 4; i++ {
		if len(r.Rank(p, Request{}, []Candidate{c})) != 1 {
			t.Fatal("Rank must not consume quotas")
		}
	}
	a, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Acquire(c); !errors.Is(err, ErrConcurrency) {
		t.Fatalf("concurrency: %v", err)
	}
	a.Finish(Outcome{})
	finishRuntime(t, r, c, Outcome{})
	if _, err = r.Acquire(c); !errors.Is(err, ErrRPM) {
		t.Fatalf("RPM: %v", err)
	}
	*now = now.Add(time.Minute - time.Nanosecond)
	if _, err = r.Acquire(c); !errors.Is(err, ErrRPM) {
		t.Fatalf("minute boundary: %v", err)
	}
	*now = now.Add(time.Nanosecond)
	finishRuntime(t, r, c, Outcome{})
}

// TestRuntimeCooldown 验证限流独立冷却、上限 600 秒、503 Retry-After、中性释放试探和后续恢复。
func TestRuntimeCooldown(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	finishRuntime(t, r, c, Outcome{Penalty: Light, RateLimited: true})
	h := r.Health("a", "v1")
	if h.CooldownUntil != now.Add(time.Second) || h.Score != 9 {
		t.Fatalf("first rate limit: %+v", h)
	}
	if _, err := r.Acquire(c); !errors.Is(err, ErrCooldown) {
		t.Fatalf("cooldown: %v", err)
	}
	*now = h.CooldownUntil
	probe, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Acquire(c); !errors.Is(err, ErrCircuit) {
		t.Fatalf("cooldown probe exclusion: %v", err)
	}
	probe.Finish(Outcome{})
	finishRuntime(t, r, c, Outcome{Penalty: Light, RetryAfter: time.Hour})
	h = r.Health("a", "v1")
	if h.CooldownUntil != now.Add(600*time.Second) || h.Score != 8 {
		t.Fatalf("503 retry after: %+v", h)
	}
	*now = h.CooldownUntil
	finishRuntime(t, r, c, Outcome{Success: true})
	if h = r.Health("a", "v1"); !h.CooldownUntil.IsZero() || h.ConsecutiveRateLimits != 0 || h.Score != 9 {
		t.Fatalf("cooldown recovery: %+v", h)
	}
}

// TestRuntimePersistence 验证真实熔断快照往返后不丢分、不重置等待且不保留进程内占用。
func TestRuntimePersistence(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	finishRuntime(t, r, c, Outcome{Penalty: Unavailable, RetryAfter: 90 * time.Second})
	records := r.ExportHealth()
	restored, _ := runtimeClock()
	if err := restored.RestoreHealth(records); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(records, restored.ExportHealth()) {
		t.Fatal("health snapshot changed")
	}
	if _, err := restored.Acquire(c); !errors.Is(err, ErrCooldown) {
		t.Fatalf("restart cooldown: %v", err)
	}
	*now = r.Health("a", "v1").NextProbe.Add(time.Minute)
	a, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := r.ExportHealth()
	if snapshot[0].Health.Inflight != 0 || snapshot[0].Health.Probing {
		t.Fatal("must not persist local ownership")
	}
	a.Finish(Outcome{})
	if err := restored.RestoreHealth(records); err == nil {
		t.Fatal("cannot overwrite used state")
	}
	invalid := NewRuntime()
	records[0].Health.Score = 11
	if err := invalid.RestoreHealth(records); err == nil || len(invalid.ExportHealth()) != 0 {
		t.Fatal("invalid snapshot must reject atomically")
	}
}

// TestRuntimeConcurrentAdmission 验证并发竞争不会穿透实际并发或 RPM 上限。
func TestRuntimeConcurrentAdmission(t *testing.T) {
	for _, limitRPM := range []bool{false, true} {
		r, _ := runtimeClock()
		c := runtimeCandidate("a", 0, 0, nil)
		if limitRPM {
			c.Key.RPMLimit = 5
		} else {
			c.Key.ConcurrencyLimit = 5
		}
		var wg sync.WaitGroup
		attempts := make(chan *Attempt, 64)
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a, err := r.Acquire(c)
				if err == nil {
					attempts <- a
					return
				}
				expected := ErrConcurrency
				if limitRPM {
					expected = ErrRPM
				}
				if !errors.Is(err, expected) {
					t.Errorf("admission: %v", err)
				}
			}()
		}
		wg.Wait()
		close(attempts)
		if len(attempts) != 5 {
			t.Fatalf("admitted %d requests, want 5", len(attempts))
		}
		for a := range attempts {
			a.Finish(Outcome{})
		}
		if r.Health("a", "v1").Inflight != 0 {
			t.Fatal("all attempts must release")
		}
	}
}

// TestRuntimeRetryAfterZero 验证显式零秒不会被未提供时的本地退避替代。
func TestRuntimeRetryAfterZero(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	finishRuntime(t, r, c, Outcome{Penalty: Light, RateLimited: true, RetryAfterSet: true})
	h := r.Health("a", "v1")
	if !h.CooldownUntil.Equal(*now) {
		t.Fatalf("explicit zero: %+v", h)
	}
	a, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Acquire(c); !errors.Is(err, ErrCircuit) {
		t.Fatalf("zero cooldown probe: %v", err)
	}
	a.Finish(Outcome{})
}

// TestRuntimeNeutralProbeReleasesOnlyItsOwnership 验证中性试探不改分、不重排等待，重复旧完成不释放新试探。
func TestRuntimeNeutralProbeReleasesOnlyItsOwnership(t *testing.T) {
	r, now := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	finishRuntime(t, r, c, Outcome{Penalty: Unavailable})
	initial := r.Health("a", "v1")
	*now = initial.NextProbe
	old, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	old.Finish(Outcome{})
	neutral := r.Health("a", "v1")
	if neutral.Score != 0 || !neutral.CircuitOpen || neutral.ProbeInterval != initial.ProbeInterval || neutral.NextProbe != initial.NextProbe || neutral.ConsecutiveFailures != initial.ConsecutiveFailures || neutral.Probing {
		t.Fatalf("neutral probe changed health: %+v", neutral)
	}
	current, err := r.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	old.Finish(Outcome{Success: true})
	if _, err = r.Acquire(c); !errors.Is(err, ErrCircuit) {
		t.Fatalf("old owner released current probe: %v", err)
	}
	current.Finish(Outcome{Penalty: Light})
	if h := r.Health("a", "v1"); h.Score != 0 || h.ProbeInterval != 2*time.Minute {
		t.Fatalf("next real fault: %+v", h)
	}
}

// TestRuntimeLightPenaltyLadder 验证第3～4次追加1分、第5次起追加2分，完整成功不超过10分。
func TestRuntimeLightPenaltyLadder(t *testing.T) {
	r, _ := runtimeClock()
	c := runtimeCandidate("a", 0, 0, nil)
	for i := 0; i < 3; i++ {
		finishRuntime(t, r, c, Outcome{Success: true})
	}
	if r.Health("a", "v1").Score != 10 {
		t.Fatal("success must cap at 10")
	}
	for i, want := range []float64{9, 8, 6, 4, 1, 0} {
		finishRuntime(t, r, c, Outcome{Penalty: Light})
		h := r.Health("a", "v1")
		if h.Score != want || h.ConsecutiveFailures != i+1 {
			t.Fatalf("failure %d: %+v, want score %v", i+1, h, want)
		}
	}
}
