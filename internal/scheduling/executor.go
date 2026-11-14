package scheduling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
	"io"
	"log"
	"net/http"
	"time"
)

// ExecutionRequest 是网关交给调度的同格式请求；正文在本次调用结束后不保留。
type ExecutionRequest struct {
	// Request 包含逻辑模型、协议和会话身份。
	Request Request
	// Type 是业务类型，空值沿用 chat；用于候选能力过滤与记录分类。
	Type string
	// Method 是实际 HTTP 动作，空值为 POST；非生成资源查询沿用原目标绑定。
	Method string
	// WireBody 非 nil 时是 multipart/二进制真实出站正文；Body 仍保存 JSON 路由元数据。
	WireBody []byte
	// ResourceID 是入口为无 JSON 的资源创建生成的内部绑定标识；runner 成功后通过 ResponseID 登记。
	ResourceID string
	// Revalidate 在内部资源轮询出站前复核当前候选的权限及凭据版本；失败时不得切换目标。
	Revalidate func(context.Context) (string, error)
	// Response 在成功提交前传递真实状态及安全响应头，失败尝试不得调用。
	Response func(int, http.Header)
	// Realtime 是已升级的原生实时客户端连接，由独立 runner 管理双向传输。
	Realtime *websocket.Conn
	// Finalize 在返回前、记录结算前调用一次；出口已接入记录采集，调用者不得在返回后重复写错误。
	Finalize func(io.Writer, ExecutionResult, error)
	// Path 是供应商根地址后的协议路径，不允许完整 URL。
	Path string
	// Body 是原始 JSON；传输适配器只做模型映射、Codex 独立上下文和流式用量等同格式必要整理。
	Body []byte
	// Headers 只允许转发必要的协议头，不转发客户端鉴权和 Cookie。
	Headers http.Header
	// Stream 表示 SSE 响应，由传输适配器逐段转发。
	Stream bool
	// RetrySafe 由协议入口确认尚未提交结果且创建语义允许重试；未知异步创建必须为 false。
	RetrySafe bool
	// PreviousResponseID 非空时必须核对原响应绑定，不能普通选路。
	PreviousResponseID string
	// Principal 是网关认证主体的不可逆摘要；访问密钥变化后不能续接旧主体的上游状态。
	Principal string
	// WebSocket 非空表示原生 Responses 会话；普通 HTTP/SSE 不使用供应商 WebSocket 开关。
	WebSocket *WebSocketSession
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
	// FailureSent 表示上游原生错误事件已经写给客户端，入口无需重复附加另一条错误。
	FailureSent bool
	// Retryable 表示故障分类允许重试，并仍受请求及配置边界约束。
	Retryable bool
	// Status 是上游或本地协议错误状态，诊断不得包含密钥及正文。
	Status int
	// ResponseID 仅在完整成功时用于本机续接绑定，不作为普通诊断输出。
	ResponseID string
	// ResourceIDs 是同一次成功列表返回的具名资源，全部绑定到本次实际候选及认证主体。
	ResourceIDs []string
	// Warmup 表示 generate:false 的有效协议预热，不将无生成内容误判失败，也不给模型健康回分。
	Warmup bool
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
	// Records 接入真实请求记录；独立调度单元可不配置。
	Records *records.Store
	// Policies、Runtime、Source、Runner 由应用入口各创建一次，不允许请求间复制运行状态。
	Policies *Store
	Runtime  *Runtime
	Source   CandidateSource
	Runner   AttemptRunner
	// Continuation 返回连续请求原有目标；缺失或无法验证时返回409，不建立第二份生成。
	Continuation func(context.Context, ExecutionRequest) (string, string, bool)
	// Continuations 保存实际成功响应的完整目标身份，优先于旧的宿主解析回调。
	Continuations *Continuations
	// Persist 在每次真实尝试结算后保存健康；本地保存失败单独报告，不改变已确认的上游结果。
	Persist func(context.Context) error
}

// ErrUnsupportedBackground 表示尚无 retrieve 支持，禁止创建无法继续跟踪的异步任务。
var ErrUnsupportedBackground = errors.New("暂不支持 background:true；尚未接入异步任务查询")

// ErrNoCandidates 表示没有符合权限、配置与运行准入的上游，不等同于上游失败。
var ErrNoCandidates = errors.New("没有可用的调度候选")

// ErrContinuation 表示连续请求无法核实原目标，入口应返回 HTTP 409。
var ErrContinuation = errors.New("连续会话的原上游绑定无法验证")

// ErrHealthMissing 表示当前凭据尚未建立可恢复的运行健康记录。
var ErrHealthMissing = errors.New("Key 健康状态尚未建立")

// ResetHealth 将当前凭据恢复为满分并立即持久化；保存失败时还原旧状态，避免界面与重启结果分叉。
func (e *Executor) ResetHealth(ctx context.Context, keyID, version string) error {
	if e == nil || e.Runtime == nil {
		return errors.New("调度运行核心尚未初始化")
	}
	if e.Persist == nil {
		return errors.New("调度健康持久化尚未初始化")
	}
	previous, ok := e.Runtime.resetHealth(keyID, version)
	if !ok {
		return ErrHealthMissing
	}
	if err := e.Persist(ctx); err != nil {
		e.Runtime.rollbackHealth(keyID, version, previous)
		return fmt.Errorf("保存恢复后的健康度: %w", err)
	}
	return nil
}

// Execute 按一次配置快照完成选路、有限重试和故障转移；写出结果后绝不重放。
func (e *Executor) Execute(ctx context.Context, req ExecutionRequest, w io.Writer) (result ExecutionResult, executionErr error) {
	var finish func(error) error
	defer func() {
		if req.Finalize != nil {
			req.Finalize(w, result, executionErr)
		}
		if finish != nil {
			executionErr = errors.Join(executionErr, finish(executionErr))
		}
	}()
	if req.Type == "" {
		req.Type = "chat"
	}
	if e.Records != nil && !records.HasSession(ctx) {
		var err error
		method := req.Method
		if method == "" {
			method = http.MethodPost
		}
		ctx, finish, err = e.Records.Begin(ctx, records.Input{Method: method, Path: req.Path, ModelID: req.Request.Model, Protocol: req.Request.Protocol, Type: req.Type, Source: "business", Stream: req.Stream, Transport: requestTransport(req), Body: req.Body, Headers: req.Headers, ResourceQuery: (req.Method == http.MethodGet || req.Method == http.MethodDelete) && (req.Type == "video" || req.Type == "files" || req.Type == "interactions")})
		if err != nil {
			return result, err
		}
	}
	w = records.Writer(ctx, w)
	if e.Policies == nil || e.Runtime == nil || e.Source == nil || e.Runner == nil {
		return result, errors.New("调度执行依赖尚未初始化")
	}
	accepted := time.Now()
	var input map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &input); err != nil || input == nil {
		return result, errors.New("调用正文必须是 JSON 对象")
	}
	if bytes.Equal(bytes.TrimSpace(input["background"]), []byte("true")) {
		result.Result.Status = http.StatusBadRequest
		return result, ErrUnsupportedBackground
	}
	if previous, present := input["previous_response_id"]; present && !bytes.Equal(bytes.TrimSpace(previous), []byte("null")) {
		var id string
		if json.Unmarshal(previous, &id) != nil || id == "" || len(id) > 256 || (req.PreviousResponseID != "" && req.PreviousResponseID != id) {
			return result, ErrContinuation
		}
		if req.Request.Protocol != "openai:responses" && req.Request.Protocol != "responses" {
			return result, ErrContinuation
		}
		req.PreviousResponseID = id
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
	if req.WebSocket != nil {
		candidates, err = req.WebSocket.filter(req, candidates)
		if err != nil {
			return result, err
		}
	}
	if req.PreviousResponseID != "" {
		if e.Continuations != nil {
			candidates = e.Continuations.resolve(req, candidates)
		} else {
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
		}
		if len(candidates) == 0 {
			return result, ErrContinuation
		}
	}
	allCandidates := candidates
	candidates = e.Runtime.Rank(policy, req.Request, candidates)
	if req.PreviousResponseID != "" && len(candidates) == 0 {
		return result, ErrContinuation
	}
	targets := make([]records.Target, 0, len(candidates))
	for _, c := range candidates {
		targets = append(targets, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier})
	}
	for _, c := range allCandidates {
		found := false
		for _, ranked := range candidates {
			if ranked.Key.ID == c.Key.ID {
				found = true
				break
			}
		}
		if !found {
			targets = append(targets, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier})
		}
	}
	if err := records.Candidates(ctx, targets); err != nil {
		return result, err
	}
	for _, c := range allCandidates {
		if reason := e.Runtime.UnavailableReason(c); reason != nil {
			if err := records.Skipped(ctx, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier}, c.CredentialVersion, reason.Error(), errors.Is(reason, ErrCircuit), recordProtection(e.Runtime.Health(c.Key.ID, c.CredentialVersion))); err != nil {
				return result, err
			}
		}
	}

	deadlineSet := false
	lastErr := error(ErrNoCandidates)
	for _, c := range candidates {
		attempts := c.Provider.Request.Attempts
		if attempts == 0 {
			attempts = 1
		}
		waited := time.Duration(0)
		for n := 0; n < attempts; n++ {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			secret, checkErr := e.Source.Credential(ctx, c, req)
			if checkErr != nil {
				lastErr = checkErr
				if recordErr := records.Skipped(ctx, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier}, c.CredentialVersion, "候选配置或凭据已失效", false, records.Protection{}); recordErr != nil {
					return result, recordErr
				}
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
					if recordErr := records.Skipped(ctx, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier}, c.CredentialVersion, acquireErr.Error(), errors.Is(acquireErr, ErrCircuit), recordProtection(e.Runtime.Health(c.Key.ID, c.CredentialVersion))); recordErr != nil {
						return result, recordErr
					}
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
			result.ProviderID = c.Provider.ID
			result.KeyID = c.Key.ID
			result.Attempts++
			attemptCtx := records.WithTarget(ctx, records.Target{ProviderID: c.Provider.ID, ProviderName: c.Provider.Name, KeyID: c.Key.ID, KeyName: c.Key.Name, Multiplier: c.Key.Multiplier})
			attemptReq := req
			attemptReq.Revalidate = func(pollCtx context.Context) (string, error) {
				return e.Source.Credential(pollCtx, c, req)
			}
			outcome, runErr := e.Runner.Run(attemptCtx, c, attemptReq, secret, w)
			if recordErr := records.AttemptResult(attemptCtx, runErr); recordErr != nil {
				log.Printf("保存上游尝试结果失败: %v", recordErr)
			}
			attempt.Finish(outcome.Outcome)
			if recordErr := records.AttemptProtection(attemptCtx, recordProtection(e.Runtime.Health(c.Key.ID, c.CredentialVersion))); recordErr != nil {
				log.Printf("保存尝试保护状态失败: %v", recordErr)
			}
			result.Result = outcome
			if e.Runtime.Health(c.Key.ID, c.CredentialVersion).CircuitOpen {
				if err := records.MarkCircuit(ctx, c.Key.ID, c.CredentialVersion); err != nil {
					log.Printf("保存熔断来源失败: %v", err)
				}
			}
			if e.Persist != nil {
				// 请求结束或取消也要落健康事实；持久化使用独立、有限的清理上下文。
				saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				saveErr := e.Persist(saveCtx)
				saveCancel()
				if saveErr != nil {
					log.Printf("保存调度健康状态失败: %v", saveErr)
				}
			}
			if runErr == nil && (outcome.Outcome.Success || outcome.Warmup) {
				e.rememberResources(req, c, outcome)
				return result, nil
			}
			lastErr = runErr
			if lastErr == nil {
				lastErr = fmt.Errorf("上游调用未成功（%d）", outcome.Status)
			}
			if outcome.Committed || !req.RetrySafe || req.PreviousResponseID != "" || !outcome.Retryable {
				return result, lastErr
			}
		}
	}
	return result, lastErr
}

// requestTransport 为实际客户端连接标注传输方式，WebSocket 不冒充 HTTP 流式。
func requestTransport(req ExecutionRequest) string {
	if req.WebSocket != nil || req.Realtime != nil {
		return "websocket"
	}
	if req.Stream {
		return "stream"
	}
	return "standard"
}

// recordProtection 将运行时健康快照转换为历史诊断字段，零时刻不编码为负数。
func recordProtection(health KeyHealth) records.Protection {
	result := records.Protection{CircuitOpen: health.CircuitOpen}
	if !health.CooldownUntil.IsZero() {
		result.CooldownUntil = health.CooldownUntil.UnixMilli()
	}
	if !health.NextProbe.IsZero() {
		result.NextProbe = health.NextProbe.UnixMilli()
	}
	return result
}
