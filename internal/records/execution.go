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
func Skipped(ctx context.Context, target Target, version, reason string, circuit bool) error {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	for i := range run.record.Candidates {
		if run.record.Candidates[i].KeyID == target.KeyID {
			run.record.Candidates[i].Reason = reason
		}
	}
	if circuit && len(run.record.Attempts) == 0 {
		run.record.Target = target
		var id string
		err := run.store.db.QueryRowContext(ctx, `SELECT request_id FROM request_circuits WHERE key_id=? AND version=?`, target.KeyID, version).Scan(&id)
		if err == nil {
			run.record.CircuitSourceID = id
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return run.store.save(ctx, run.record, false)
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

// responseWriter 旁路保留真正提交给客户端的内容，不复制被丢弃的上游失败响应。
type responseWriter struct {
	// destination 是原客户端出口；run 为当前逻辑请求；raw 仅保留有界快照。
	destination io.Writer
	run         *session
	raw         bytes.Buffer
	truncated   bool
}

// Writer 将客户端正文记录接入原输出，不改变 io.Writer 的返回语义。
func Writer(ctx context.Context, w io.Writer) io.Writer {
	run, _ := ctx.Value(sessionKey{}).(*session)
	if run == nil {
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
	p.BodyState = "available"
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
