package records

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Candidate 记录本次排序快照及未执行原因，不能将候选枚举当作实际调用。
type Candidate struct {
	Target
	// Protection 保留此次跳过时的本地保护快照；零值表示未采集。
	Protection
	// CircuitSourceID 是此 Key 已知的熔断触发请求，空值表示来源未知。
	CircuitSourceID string `json:"circuitSourceId"`
	// Reason 非空表示该候选在准入或凭据复核处跳过。
	Reason string `json:"reason"`
}

// Candidates 在调度开始时保留排序快照，不包含凭据及配置版本明文。
func Candidates(ctx context.Context, values []Target) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	for _, target := range values {
		run.record.Candidates = append(run.record.Candidates, Candidate{Target: target})
	}
	return run.store.save(ctx, run.record, false)
}

// Skipped 标记未准入原因，熔断时关联已知触发请求，不伪造上游响应。
func Skipped(ctx context.Context, target Target, version, reason string, circuit bool, protection Protection) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	var id string
	if circuit {
		err := run.store.db.QueryRowContext(ctx, `SELECT request_id FROM request_circuits WHERE key_id=? AND version=?`, target.KeyID, version).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for i := range run.record.Candidates {
		candidate := &run.record.Candidates[i]
		if candidate.KeyID == target.KeyID && candidate.ProviderID == target.ProviderID {
			candidate.Reason = reason
			candidate.Protection = protection
			candidate.CircuitSourceID = id
		}
	}
	// 所有准入跳过均保留首个真实阻断目标，不能只在熔断分支填写摘要身份。
	if len(run.record.Attempts) == 0 && run.record.ProviderID == "" {
		run.record.Target = target
	}
	if circuit && len(run.record.Attempts) == 0 && run.record.CircuitSourceID == "" {
		run.record.CircuitSourceID = id
	}
	return run.store.save(ctx, run.record, false)
}

// Protection 是请求当时的保护事实，时间均为 Unix 毫秒；到期仅表示可重新准入或探测，不代表健康恢复。
type Protection struct {
	// CooldownUntil 为限流冷却截止时刻；NextProbe 为熔断下次允许探测时刻，零表示未知或不适用。
	CooldownUntil int64 `json:"cooldownUntil"`
	NextProbe     int64 `json:"nextProbe"`
	// CircuitOpen 表示该次尝试结束或候选跳过时仍处于熔断。
	CircuitOpen bool `json:"circuitOpen"`
}

// AttemptProtection 为当前实际出站尝试补写健康结果，不创建虚构网络尝试。
func AttemptProtection(ctx context.Context, protection Protection) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	scope, _ := ctx.Value(attemptKey{}).(*attemptScope)
	if run == nil || scope == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if scope.number == 0 {
		return nil
	}
	run.record.Attempts[scope.number-1].Protection = protection
	return run.store.save(context.WithoutCancel(ctx), run.record, false)
}

// MarkCircuit 保存实际触发熔断的请求身份；同凭据后续本地拦截可定位此请求。
func MarkCircuit(ctx context.Context, key, version string) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
		return nil
	}
	_, err := run.store.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO request_circuits VALUES(?,?,?) ON CONFLICT(key_id) DO UPDATE SET version=excluded.version,request_id=excluded.request_id`, key, version, run.record.ID)
	return err
}

// AttemptResult 在 Runner 返回且响应已关闭后补写协议执行错误，保留上游实际 HTTP 状态。
// ctx 必须是该次 WithTarget 返回的 context；未真正出站时不改写尝试，图片进度仅在 Runner 返回后确认终态。
func AttemptResult(ctx context.Context, cause error) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	scope, _ := ctx.Value(attemptKey{}).(*attemptScope)
	if run == nil || scope == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if scope.number == 0 {
		return nil
	}
	attempt := &run.record.Attempts[scope.number-1]
	if cause != nil {
		attempt.Error = sanitize(cause.Error(), run.secrets)
	}
	if attempt.ImageProgress != nil {
		if cause != nil || attempt.Error != "" || attempt.Status >= 400 {
			attempt.ImageProgress.Phase = "failed"
		} else {
			attempt.ImageProgress.Phase = "completed"
		}
	}
	if cause == nil && attempt.ImageProgress == nil {
		return nil
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := run.store.save(saveCtx, run.record, false)
	run.writeErr = errors.Join(run.writeErr, err)
	return err
}

// responseWriter 旁路保留真正提交给客户端的内容，不复制被丢弃的上游失败响应。
type responseWriter struct {
	// destination 是原客户端出口；run 为当前逻辑请求；raw 仅保留有界快照。
	destination io.Writer
	run         *session
	raw         bytes.Buffer
	truncated   bool
	// omitted 标识实际写出过非文本媒体，正文快照只留元信息。
	omitted bool
}

// Writer 将客户端正文记录接入原输出；同一会话只安装一次，嵌套调用保留原出口及其刷新语义。
func Writer(ctx context.Context, w io.Writer) io.Writer {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
		return w
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.clientOutput != nil {
		return w
	}
	result := &responseWriter{destination: w, run: run}
	run.clientOutput = result
	return result
}

// Write 只记录成功提交的字节，客户端断开后的内容不假装已送达。
func (w *responseWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	w.run.mu.Lock()
	defer w.run.mu.Unlock()
	omit := w.run.binaryResponse
	if destination, ok := w.destination.(interface{ Header() http.Header }); ok {
		if contentType := destination.Header().Get("Content-Type"); contentType != "" {
			omit = !textResponse(contentType)
		}
	}
	if omit && n > 0 {
		w.omitted = true
		return n, err
	}
	room := captureLimit - w.raw.Len()
	if room > 0 {
		w.raw.Write(p[:min(n, room)])
	}
	if n > room {
		w.truncated = true
	}
	return n, err
}

// saveClient 将客户端实际提交内容保存到独立正文表，遵守七天期限。
func (s *session) saveClient(ctx context.Context) error {
	if s.clientOutput == nil {
		return nil
	}
	var raw string
	var p Payload
	if err := s.store.db.QueryRowContext(ctx, `SELECT document FROM request_payloads WHERE request_id=? AND number=0`, s.record.ID).Scan(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	p.ResponseBody = sanitize(s.clientOutput.raw.String(), s.secrets)
	if s.clientOutput.omitted && s.clientOutput.raw.Len() == 0 {
		p.ResponseBody = omittedResponseBody
		if p.BodyState != "truncated" {
			p.BodyState = "unavailable"
		}
		return s.store.putPayload(ctx, s.record.ID, 0, p)
	}
	if p.BodyState == "unavailable" {
		p.BodyState = "available"
	}
	if s.clientOutput.truncated {
		p.BodyState = "truncated"
	}
	return s.store.putPayload(ctx, s.record.ID, 0, p)
}

// retentionDeadline 用固定时长解释保留期限，不受夏令时自然日跳变影响。
func retentionDeadline(s *Store, days int) int64 {
	return s.now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
}

// Flush 保留原 SSE 出口刷新能力，包装器不能使客户端等待整个流结束。
func (w *responseWriter) Flush() {
	if f, ok := w.destination.(http.Flusher); ok {
		f.Flush()
	}
}

// redactSaved 清除其他候选响应里回显的已用凭据，覆盖正文中嵌套 JSON 的转义形式。
func (s *session) redactSaved(ctx context.Context) error {
	for _, secret := range s.secrets {
		if secret == "" {
			continue
		}
		variants := []string{secret, url.QueryEscape(secret), url.PathEscape(secret)}
		for _, value := range variants {
			for depth := 0; depth < 3; depth++ {
				encoded, _ := json.Marshal(value)
				value = string(encoded[1 : len(encoded)-1])
				if _, err := s.store.db.ExecContext(ctx, `UPDATE request_payloads SET document=replace(document,?,'[REDACTED]') WHERE request_id=?`, value, s.record.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
