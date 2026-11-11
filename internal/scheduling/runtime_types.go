package scheduling

import (
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// Candidate 是已完成模型、协议和权限匹配的候选，不保存密钥明文。
// transport 必须使用 Rank 返回的值，在 Acquire 前复核当前供应商/Key 配置并在执行时读取 Secret。
type Candidate struct {
	// Provider 是供应商执行配置快照。
	Provider providers.Provider
	// Key 是 Key 配置快照，不含密钥。
	Key providers.ProviderKey
	// Model 是实际发送给上游的模型名。
	Model string
	// CredentialVersion 是持久、不可逆的凭据版本标记，替换凭据后必须变化，不能使用掩码。
	CredentialVersion string
	// ConfigVersion 是候选源串行读取快照时分配的进程内单调序号；替换凭据必须严格增加。
	// 0 可用于无配置更新的静态候选；运行中动态候选应从 1 开始，序号不能由请求完成顺序生成。
	ConfigVersion uint64
	// ProviderOrder 是供应商顺位，越小越优先；policy.ProviderOrder 有记录时以其为准。
	ProviderOrder int
	// scope 保存 Rank 产生的请求身份，防止外部伪造亲和写入。
	scope *rankScope
}

// Request 是亲和范围；Session 为空时不读写亲和，不把无会话请求合并。
type Request struct {
	// Model 是客户端请求的逻辑模型名。
	Model string
	// Protocol 是已归一化的协议标识。
	Protocol string
	// Session 是适配器从可信请求上下文取得的会话标识。
	Session string
	// Order 是递增的逻辑请求顺序；0 时由 Rank 分配，多次排序同一请求时应复用非零值。
	Order uint64
}

// Penalty 是 transport 按实际故障事实提供的健康分类；零值为中性。
type Penalty uint8

const (
	// Neutral 不加减分，也不清零连续失败；取消、本地错误使用该值。
	Neutral Penalty = iota
	// Light 基础扣 1 分，用于超时、明确限流和临时不可用。
	Light
	// Heavy 基础扣 2 分，用于传输、协议、一般 5xx 和未经确认的 401/403。
	Heavy
	// Unavailable 只用于已验证属于当前凭据的失效、停用或额度耗尽，直接归零。
	Unavailable
)

// Outcome 仅描述一次实际尝试的最终结果，HTTP 200 或仅收到响应头不等于 Success。
type Outcome struct {
	// Success 表示已验证协议完整成功；优先于故障分类。
	Success bool
	// Penalty 是失败计分分类，零值表示中性。
	Penalty Penalty
	// RateLimited 表示明确限流，无 RetryAfter 时使用本地 1、2、4…60 秒退避。
	RateLimited bool
	// RetryAfterSet 区分有效 Retry-After: 0 和未提供；正 RetryAfter 无需另设该值。
	RetryAfterSet bool
	// RetryAfter 是已解析有效的等待时长，正值触发冷却，最多 600 秒。
	RetryAfter time.Duration
}

// 准入错误允许 transport 跳过当前候选，不应记为上游故障。
var (
	// ErrUnavailable 表示候选未启用、无凭据或凭据版本已失效。
	ErrUnavailable = errors.New("调度候选已不可用")
	// ErrConcurrency 表示整 Key 的实际在途数已达到上限。
	ErrConcurrency = errors.New("上游 Key 已达到并发上限")
	// ErrRPM 表示整 Key 最近一分钟的准入次数已达到上限。
	ErrRPM = errors.New("上游 Key 已达到 RPM 上限")
	// ErrCooldown 表示整 Key 仍处于限流冷却。
	ErrCooldown = errors.New("上游 Key 正在冷却")
	// ErrCircuit 表示熔断尚未到期或恢复试探已由其他真实请求占用。
	ErrCircuit = errors.New("上游 Key 正在熔断或试探中")
)

// KeyHealth 是整 Key 的可复制健康快照，所有协议共用同一状态。
type KeyHealth struct {
	// Score 是 0～10 分；首次使用和无历史记录时为 10。
	Score float64 `json:"score"`
	// ConsecutiveFailures 是有效故障的连续计数，中性结果保持不变。
	ConsecutiveFailures int `json:"consecutiveFailures"`
	// CircuitOpen 表示零分熔断，时间到期仍不自动恢复分数。
	CircuitOpen bool `json:"circuitOpen"`
	// Generation 是熔断代次，阻止旧代次尝试覆盖新状态。
	Generation uint64 `json:"generation"`
	// ProbeInterval 是当前熔断等待间隔，单位为纳秒。
	ProbeInterval time.Duration `json:"probeInterval"`
	// NextProbe 是下次可由真实请求取得单探测资格的时刻。
	NextProbe time.Time `json:"nextProbe"`
	// CooldownUntil 是整 Key 冷却截止时刻。
	CooldownUntil time.Time `json:"cooldownUntil"`
	// ConsecutiveRateLimits 是本地限流退避计数，完整成功后归零。
	ConsecutiveRateLimits int `json:"consecutiveRateLimits"`
	// Inflight 是进程内已取得资格且未 Finish 的尝试数，不参与持久化。
	Inflight int `json:"-"`
	// Probing 表示进程内有一个熔断或冷却恢复试探，不参与持久化。
	Probing bool `json:"-"`
}

// HealthRecord 是供本地数据库持久化的整 Key 状态；不含明文凭据和进程内租约。
type HealthRecord struct {
	// KeyID 是稳定 Key 身份。
	KeyID string `json:"keyId"`
	// CredentialVersion 将健康记录绑定到对应凭据。
	CredentialVersion string `json:"credentialVersion"`
	// Health 是重启后必须保留的熔断、计分和冷却状态。
	Health KeyHealth `json:"health"`
}

// Runtime 拥有进程内排序、亲和及实际准入；所有公开方法均可并发调用，不可复制。
type Runtime struct {
	// mu 保护状态、亲和和请求序号，避免检查与实际取得资格分离。
	mu sync.Mutex
	// now 使用真实时钟，包内测试可替换以验证时间边界。
	now func() time.Time
	// keys 以稳定 Key ID 保存当前凭据代次。
	keys map[string]*keyState
	// affinities 按会话、模型、协议、策略和配置版本隔离。
	affinities map[affinityScope]affinityEntry
	// revision 是已观察到的最高有效配置版本，旧请求不能恢复旧亲和。
	revision int64
	// order 是自动分配的逻辑请求顺序。
	order uint64
}

// Attempt 是一次已准入真实请求的所有权；调用方必须在所有退出路径调用 Finish，且不能复制。
// 没有后台探测或租约超时：资格与真实请求同寿命，防止长流请求中途放入第二个探测。
type Attempt struct {
	// runtime 是拥有资格的运行核心。
	runtime *Runtime
	// candidate 是已准入候选，包含亲和写入上下文。
	candidate Candidate
	// state 是准入时的凭据状态，替换后仅释放旧状态资源。
	state *keyState
	// generation 是准入时熔断代次。
	generation uint64
	// probe 表示本尝试持有恢复试探资格。
	probe bool
	// once 保证每个真实尝试只释放和结算一次。
	once sync.Once
}
