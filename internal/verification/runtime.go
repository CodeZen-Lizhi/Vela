package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/outbound"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/verification/fingerprint"
)

// publish 合并更新可复制的任务进度，回调不在任务锁内执行。
func (m *Manager) publish(change func(*VerificationProgress)) {
	m.mu.Lock()
	change(&m.progress)
	p := m.progress
	emit := m.emit
	m.mu.Unlock()
	if emit != nil {
		emit(p)
	}
}

// SetProgressHandler 由宿主在运行前注入事件推送，不让业务核心依赖MyGo。
func (m *Manager) SetProgressHandler(fn func(VerificationProgress)) {
	m.mu.Lock()
	m.emit = fn
	m.mu.Unlock()
}

// Start 接受一个计划或已有结果固定目标，进程只允许一轮，网络任务不继承短暂RPC上下文。
func (m *Manager) Start(ctx context.Context, providerID, reportID string) error {
	targets := []VerificationTarget{}
	mode := "plan"
	if reportID != "" {
		snapshot, err := m.Snapshot(ctx)
		if err != nil {
			return err
		}
		for _, r := range snapshot.Reports {
			if r.ID == reportID && r.Target.ProviderID == providerID {
				t, err := m.resolve(ctx, providerID, r.Target.VerificationLine)
				if err != nil {
					return err
				}
				if identity(t) != r.ID {
					return errors.New("检测目标已变化，请从计划重新执行")
				}
				targets = append(targets, t)
			}
		}
		mode = "single"
	} else {
		plans, err := m.Plans(ctx)
		if err != nil {
			return err
		}
		for _, p := range plans {
			if p.ProviderID == providerID {
				for _, line := range p.Lines {
					t, err := m.resolve(ctx, providerID, line)
					if err == nil {
						targets = append(targets, t)
					}
				}
			}
		}
	}
	if len(targets) == 0 {
		return errors.New("没有可执行的固定检测组合，请检查计划、Key与模型权限")
	}
	m.mu.Lock()
	if m.closed || m.suspended {
		m.mu.Unlock()
		return errors.New("模型验证正在退出")
	}
	if m.progress.Running {
		m.mu.Unlock()
		return errors.New("已有检测进行中")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.progress = VerificationProgress{Running: true, Mode: mode, Total: len(targets) * 3, Target: targets[0]}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() { defer m.wg.Done(); defer cancel(); m.runRound(runCtx, targets) }()
	return nil
}

// Stop 取消在途与后续样本，结果由后台任务收尾，不能撤回已产生的上游费用。
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close 等待取消后的任务停止，必须在共享数据库关闭前调用。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// Suspend 停止在途采样并禁止新的检测；窗口隐藏不调用此方法。
func (m *Manager) Suspend() {
	m.mu.Lock()
	m.suspended = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Resume 跳过睡眠期间到期的任务并恢复定时；不发起网络请求。
func (m *Manager) Resume(ctx context.Context) error {
	if err := m.tick(ctx, m.now(), true); err != nil {
		return err
	}
	m.mu.Lock()
	m.suspended = false
	m.mu.Unlock()
	return nil
}

// runRound 只在执行时加载参考数据，完成后释放；组合串行，组合内三条样本并发。
func (m *Manager) runRound(ctx context.Context, targets []VerificationTarget) {
	var finalErr error
	defer func() {
		m.publish(func(p *VerificationProgress) {
			p.Running = false
			if finalErr != nil {
				p.Error = finalErr.Error()
			}
		})
	}()
	detector, err := fingerprint.LoadBundled()
	if err != nil {
		finalErr = err
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		m.publish(func(p *VerificationProgress) { p.Target = target })
		report := m.detect(ctx, detector, target)
		// 目标删除或配置变化后不把已失效快照重新写回。
		current, resolveErr := m.resolve(context.Background(), target.ProviderID, target.VerificationLine)
		if resolveErr != nil || identity(current) != identity(target) {
			// 删除或变更目标不写回旧快照；仅停用/撤权且目标身份仍相同时记录本轮终止。
			if current.ProviderID == "" || identity(current) != identity(target) {
				m.publish(func(p *VerificationProgress) {
					p.Error = "本轮目标已删除或配置发生变化，旧结果不再适用"
				})
				continue
			}
			report.State = "failed"
			report.Conclusion = "insufficient"
			report.Reason = "执行目标已失效：" + resolveErr.Error()
		}
		if err = m.saveReport(context.Background(), report); err != nil {
			finalErr = err
			return
		}
		if _, err = m.db.ExecContext(context.Background(), "UPDATE verification_plans SET last_at=? WHERE provider_id=?", m.now().UnixMilli(), target.ProviderID); err != nil {
			finalErr = err
			return
		}
	}
}

// detect 保持固定目标并合并上游真实评分；只有身份可确定且置信度不低于75%才产生比较结论。
func (m *Manager) detect(ctx context.Context, d *fingerprint.Detector, target VerificationTarget) VerificationReport {
	report := VerificationReport{ID: identity(target), Target: target, AlgorithmVersion: "lm-detector/a7e9d3056e019827904dd50722c20b4a0c896738", DataVersion: d.BuiltAt(), Conclusion: "insufficient", State: "complete", Candidates: []VerificationCandidate{}, Samples: make([]VerificationSample, 3)}
	challenges, err := fingerprint.GenerateChallenges(3)
	if err != nil {
		report.State = "failed"
		report.Reason = err.Error()
		report.Time = m.now().UnixMilli()
		return report
	}
	samples := make([]fingerprint.Sample, 3)
	var wg sync.WaitGroup
	for i, challenge := range challenges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			samples[i] = m.sample(ctx, target, challenge)
			m.publish(func(p *VerificationProgress) { p.Done++ })
		}()
	}
	wg.Wait()
	outputs := make([]fingerprint.Output, 3)
	for i, s := range samples {
		sampleErr := ""
		if s.Error != nil {
			sampleErr = *s.Error
		}
		report.Samples[i] = VerificationSample{State: s.State, Text: s.RawText, Error: sampleErr, Count: s.Count, ExpectedCount: s.ExpectedCount}
		outputs[i] = fingerprint.Output{ExpectedCount: s.ExpectedCount}
		if s.State == "complete" || s.State == "truncated" {
			outputs[i].Text = s.Text
		} else {
			report.State = "failed"
		}
	}
	if ctx.Err() != nil {
		report.State = "cancelled"
		report.Reason = "本轮已取消，未完成有效评分"
	} else {
		analysis, err := d.Analyze(outputs, false)
		if err != nil {
			report.Reason = err.Error()
		} else if analysis != nil {
			report.Reason = analysis.Evidence.Reason
			for _, c := range analysis.Results {
				report.Candidates = append(report.Candidates, VerificationCandidate{Model: c.Model, Name: c.DisplayName, Family: c.FamilyName, Score: c.Score, Probability: c.Probability})
			}
			if len(report.Candidates) > 0 && report.Candidates[0].Probability != nil && *report.Candidates[0].Probability >= 0.75 && analysis.Decision == "scored" && analysis.UsedOutputs == 3 {
				expected := ""
				for _, c := range report.Candidates {
					if c.Model == target.RequestModel {
						expected = c.Model
						break
					}
				}
				if expected != "" {
					report.Conclusion = "difference"
					if analysis.Prediction == expected {
						report.Conclusion = "match"
					}
				} else {
					report.Reason = "预期请求模型无法对应参考库身份，仅展示候选排名"
				}
			}
		}
	}
	report.Time = m.now().UnixMilli()
	return report
}

// sample 每次尝试前复核固定目标和凭据，暂时性失败额外重试两次，不切换Key或协议。
func (m *Manager) sample(ctx context.Context, t VerificationTarget, c fingerprint.Challenge) fingerprint.Sample {
	failed := fingerprint.Sample{State: "failed", ExpectedCount: c.ExpectedCount}
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			failed.State = "cancelled"
			message := "检测已取消"
			failed.Error = &message
			return failed
		}
		current, err := m.resolve(ctx, t.ProviderID, t.VerificationLine)
		if err != nil || identity(current) != identity(t) {
			message := "检测目标已停用、删除或发生变化"
			failed.Error = &message
			return failed
		}
		candidate, secret, err := m.credential(ctx, t)
		if err != nil {
			message := err.Error()
			failed.Error = &message
			return failed
		}
		client, err := m.client(ctx, candidate)
		if err != nil {
			message := err.Error()
			failed.Error = &message
			return failed
		}
		bounded, cancel := context.WithTimeout(ctx, 120*time.Second)
		failed = requestSample(bounded, client, t, secret, c)
		cancel()
		client.CloseIdleConnections()
		if failed.State != "failed" || !sampleRetryable(failed) {
			return failed
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return failed
}

// credential 复用供应商实际执行准入与凭据解密，不经过健康调度或故障转移。
func (m *Manager) credential(ctx context.Context, t VerificationTarget) (providers.ExecutionCandidate, string, error) {
	models, err := m.vendors.Models(ctx, t.ProviderID)
	if err != nil {
		return providers.ExecutionCandidate{}, "", err
	}
	modelName := ""
	for _, model := range models {
		if model.ModelID == t.ModelID {
			modelName = model.ModelName
		}
	}
	candidates, err := m.vendors.ExecutionCandidates(ctx, modelName)
	if err != nil {
		return providers.ExecutionCandidate{}, "", err
	}
	for _, c := range candidates {
		if c.Provider.ID == t.ProviderID && c.Key.ID == t.KeyID && c.Model == t.RequestModel && c.Provider.URL == t.URL {
			secret, err := m.vendors.ExecutionSecret(ctx, modelName, c.Key.ID, c.Version, c.Model)
			return c, secret, err
		}
	}
	return providers.ExecutionCandidate{}, "", errors.New("固定检测目标不再可用")
}

// client 复用项目代理解析与传输层，禁止重定向携带认证，调用方释放连接。
func (m *Manager) client(ctx context.Context, c providers.ExecutionCandidate) (*http.Client, error) {
	choice := c.Key.Proxy
	if choice.Mode == providers.ProxyProvider || choice.Mode == "" {
		choice = c.Provider.Request.Proxy
	}
	var proxy *url.URL
	if choice.Mode == providers.ProxyGlobal {
		global, err := m.catalog.GetProxy(ctx)
		if err != nil {
			return nil, err
		}
		if global.Enabled {
			proxy, err = outbound.ParseProxy(global.URL)
			if err != nil {
				return nil, err
			}
		}
	} else if choice.Mode != providers.ProxyDirect {
		return nil, errors.New("无效网络代理方式")
	}
	return &http.Client{Transport: outbound.Transport(proxy), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// saveReport 替换组合唯一最近报告，外键确保删除目标不会被重新创建。
func (m *Manager) saveReport(ctx context.Context, r VerificationReport) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO verification_reports VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET document=excluded.document`, r.ID, r.Target.ProviderID, r.Target.ModelID, r.Target.KeyID, string(data))
	return err
}

// Run 定时轮询仅处理正常运行时到期计划，系统睡眠由宿主 Suspend/Resume 事件控制。
func (m *Manager) Run(ctx context.Context) error {
	// 每次启动重排所有启用计划，退出期间不补测。
	if _, err := m.db.ExecContext(ctx, "UPDATE verification_plans SET next_at=?+hours*3600000 WHERE enabled=1", m.now().UnixMilli()); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			now := m.now()
			m.mu.Lock()
			suspended := m.suspended
			m.mu.Unlock()
			if suspended {
				continue
			}
			if err := m.tick(ctx, now, false); err != nil {
				return fmt.Errorf("模型验证定时任务: %w", err)
			}
		}
	}
}

// tick 将错过的周期向后推进；暂停恢复只跳过，正常到期且忙碌时下一次轮询再运行。
func (m *Manager) tick(ctx context.Context, now time.Time, resumed bool) error {
	plans, err := m.Plans(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if !p.Enabled || p.NextAt > now.UnixMilli() {
			continue
		}
		next := time.UnixMilli(p.NextAt)
		for !next.After(now) {
			next = next.Add(time.Duration(p.Hours) * time.Hour)
		}
		if resumed {
			if _, err = m.db.ExecContext(ctx, "UPDATE verification_plans SET next_at=? WHERE provider_id=?", next.UnixMilli(), p.ProviderID); err != nil {
				return err
			}
			continue
		}
		m.mu.Lock()
		busy := m.progress.Running
		m.mu.Unlock()
		if busy {
			continue
		}
		if err = m.Start(ctx, p.ProviderID, ""); err != nil {
			m.publish(func(progress *VerificationProgress) { progress.Error = err.Error() })
		}
		if _, err = m.db.ExecContext(ctx, "UPDATE verification_plans SET next_at=? WHERE provider_id=?", next.UnixMilli(), p.ProviderID); err != nil {
			return err
		}
	}
	return nil
}
