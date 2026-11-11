package scheduling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ExecutionRequest 是网关交给调度的同格式请求；正文在本次调用结束后不保留。
type ExecutionRequest struct {
	// Request 包含逻辑模型、协议和会话身份。
	Request Request
	// Operation 是协议入口解析的映射操作范围；空值按chat处理。
	Operation string
	// Path 是供应商根地址后的协议路径，不允许完整 URL。
	Path string
	// Body 是原始 JSON，传输适配器只替换明确的模型字段。
	Body []byte
	// Headers 只允许转发必要的协议头，不转发客户端鉴权和 Cookie。
	Headers http.Header
	// Stream 表示 SSE 响应，由传输适配器逐段转发。
	Stream bool
	// RetrySafe 由协议入口确认尚未提交结果且创建语义允许重试；未知异步创建必须为 false。
	RetrySafe bool
	// PreviousResponseID 非空时必须由 Continuation 解析原执行目标，不能普通选路。
	PreviousResponseID string
}

// CandidateSource 是供应商模块提供给调度的可信配置与凭据边界，不能暴露为前端绑定。
type CandidateSource interface {
	// Candidates 必须已完成模型关联、Key 权限与同格式能力匹配；不存在的映射不能用全模型伪造。
	Candidates(context.Context, ExecutionRequest) ([]Candidate, error)
	// Credential 在实际出站前复核启停、权限及凭据版本，返回仅供本次传输使用的明文。
	Credential(context.Context, Candidate, ExecutionRequest) (string, error)
}

// AttemptResult 是一次真实出站结果；Committed 后禁止切目标重放。
type AttemptResult struct {
	// Outcome 是健康分类，成功必须由完整协议终态确认。
	Outcome Outcome
	// Committed 表示已向调用者输出不可替换内容。
	Committed bool
	// Retryable 表示故障分类允许重试，并仍受请求及配置边界约束。
	Retryable bool
	// TransportFailure 区分传输失败开关和普通上游状态错误。
	TransportFailure bool
	// Status 是上游或本地协议错误状态，诊断不得包含密钥及正文。
	Status int
}

// AttemptRunner 实际执行一次同格式上游调用，不负责重试、准入与健康结算。
type AttemptRunner interface {
	// Run 仅在调度已取得资格后调用；实现须响应取消并释放响应体。
	Run(context.Context, Candidate, ExecutionRequest, string, io.Writer) (AttemptResult, error)
}

// ExecutionResult 是完整逻辑请求的诊断摘要，不保存模型内容或凭据。
type ExecutionResult struct {
	// ProviderID、KeyID 是最后实际尝试身份；Attempts 为实际出站次数。
	ProviderID string
	KeyID      string
	Attempts   int
	// Result 是最后尝试的最终结果。
	Result AttemptResult
}

// Executor 组合已保存策略、配置复核、运行准入和真实传输，供 HTTP/桌面宿主外的网关复用。
type Executor struct {
	// Policies、Runtime、Source、Runner 由应用入口各创建一次，不允许请求间复制运行状态。
	Policies *Store
	Runtime  *Runtime
	Source   CandidateSource
	Runner   AttemptRunner
	// Continuation 返回连续请求原有目标；缺失或无法验证时返回409，不建立第二份生成。
	Continuation func(context.Context, ExecutionRequest) (string, string, bool)
	// Persist 将健康状态在每次真实尝试结算后持久化；失败须报告，不能伪装成功。
	Persist func(context.Context) error
}

// ErrNoCandidates 表示没有符合权限、配置与运行准入的上游，不等同于上游失败。
var ErrNoCandidates = errors.New("没有可用的调度候选")

// ErrContinuation 表示连续请求无法核实原目标，入口应返回 HTTP 409。
var ErrContinuation = errors.New("连续会话的原上游绑定无法验证")

// transferBudget 按供应商隔离，从该供应商首次真实尝试起计时；同Key重试不计换Key次数。
type transferBudget struct {
	// started、lastKey、transfers 分别记录供应商首次尝试时间、最后Key与已换Key次数。
	started   time.Time
	lastKey   string
	transfers int
}

// Execute 按一次配置快照完成选路、有限重试和故障转移；写出结果后绝不重放。
func (e *Executor) Execute(ctx context.Context, req ExecutionRequest, w io.Writer) (ExecutionResult, error) {
	var result ExecutionResult
	if e.Policies == nil || e.Runtime == nil || e.Source == nil || e.Runner == nil {
		return result, errors.New("调度执行依赖尚未初始化")
	}
	accepted := time.Now()
	var input map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &input); err != nil || input == nil {
		return result, errors.New("调用正文必须是 JSON 对象")
	}
	if previous, present := input["previous_response_id"]; present && !bytes.Equal(bytes.TrimSpace(previous), []byte("null")) {
		return result, ErrContinuation
	}
	if req.Request.Session == "" {
		req.Request.Session = SessionFromRequest(req.Request.Protocol, req.Headers, req.Body)
	}
	policy, err := e.Policies.Policy(ctx)
	if err != nil {
		return result, err
	}
	candidates, err := e.Source.Candidates(ctx, req)
	if err != nil {
		return result, err
	}
	if req.PreviousResponseID != "" {
		if e.Continuation == nil {
			return result, ErrContinuation
		}
		provider, key, ok := e.Continuation(ctx, req)
		if !ok {
			return result, ErrContinuation
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.Provider.ID == provider && c.Key.ID == key {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
		if len(candidates) == 0 {
			return result, ErrContinuation
		}
	}
	candidates = e.Runtime.Rank(policy, req.Request, candidates)
	budgets := map[string]*transferBudget{}
	deadlineSet := false
	lastErr := error(ErrNoCandidates)
	for _, c := range candidates {
		attempts := c.Provider.Request.Attempts
		if attempts == 0 {
			attempts = 1
		}
		budget := budgets[c.Provider.ID]
		waited := time.Duration(0)
		for n := 0; n < attempts; n++ {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			if budget != nil {
				p := c.Provider.Request
				if (p.MaxTransferSeconds > 0 && time.Since(budget.started) >= time.Duration(p.MaxTransferSeconds)*time.Second) || (p.MaxTransfers > 0 && budget.lastKey != c.Key.ID && budget.transfers >= p.MaxTransfers) {
					break
				}
			}
			secret, checkErr := e.Source.Credential(ctx, c, req)
			if checkErr != nil {
				lastErr = checkErr
				break
			}
			attempt, acquireErr := e.Runtime.Acquire(c)
			if acquireErr != nil {
				// 等待只用于同Key重试间隙，不占用并发或探测资格，且每请求Key最多2秒。
				if n > 0 && errors.Is(acquireErr, ErrCooldown) {
					health := e.Runtime.Health(c.Key.ID, c.CredentialVersion)
					delay := time.Until(health.CooldownUntil)
					if delay > 0 && delay <= 2*time.Second-waited {
						timer := time.NewTimer(delay)
						select {
						case <-ctx.Done():
							timer.Stop()
							return result, ctx.Err()
						case <-timer.C:
						}
						waited += delay
						attempt, acquireErr = e.Runtime.Acquire(c)
					}
				}
				if acquireErr != nil {
					lastErr = acquireErr
					break
				}
			}
			if req.Stream && !deadlineSet {
				duration := time.Duration(c.Provider.Request.RequestSeconds) * time.Second
				if duration == 0 {
					duration = 300 * time.Second
				}
				if req.Stream {
					duration = time.Duration(c.Provider.Request.StreamTotalMillis) * time.Millisecond
					if duration == 0 {
						duration = 120 * time.Second
					}
				}
				bounded, boundedCancel := context.WithDeadline(ctx, accepted.Add(duration))
				defer boundedCancel()
				ctx = bounded
				deadlineSet = true
			}
			if budget == nil {
				budget = &transferBudget{started: time.Now(), lastKey: c.Key.ID}
				budgets[c.Provider.ID] = budget
			} else if budget.lastKey != c.Key.ID {
				budget.transfers++
				budget.lastKey = c.Key.ID
			}
			result.ProviderID = c.Provider.ID
			result.KeyID = c.Key.ID
			result.Attempts++
			outcome, runErr := e.Runner.Run(ctx, c, req, secret, w)
			attempt.Finish(outcome.Outcome)
			result.Result = outcome
			if e.Persist != nil {
				// 请求结束或取消也要落健康事实；持久化使用独立、有限的清理上下文。
				saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				saveErr := e.Persist(saveCtx)
				saveCancel()
				if saveErr != nil {
					return result, fmt.Errorf("保存调度健康状态失败: %w", saveErr)
				}
			}
			if runErr == nil && outcome.Outcome.Success {
				return result, nil
			}
			lastErr = runErr
			if lastErr == nil {
				lastErr = fmt.Errorf("上游调用未成功（%d）", outcome.Status)
			}
			if outcome.Committed || !req.RetrySafe || req.PreviousResponseID != "" || !outcome.Retryable {
				return result, lastErr
			}
			if outcome.TransportFailure && !c.Provider.Request.TransportFailover {
				return result, lastErr
			}
		}
	}
	return result, lastErr
}
