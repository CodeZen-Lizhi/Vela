package verification

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/verification/fingerprint"
)

// schema 以真实资源外键级联清理局部线路和报告；报告不依赖计划，允许计划外固定目标复测。
const schema = `
CREATE TABLE IF NOT EXISTS verification_plans(
 provider_id TEXT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL CHECK(enabled IN(0,1)), hours INTEGER NOT NULL CHECK(hours IN(1,6,12,24)),
 next_at INTEGER NOT NULL, last_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS verification_lines(
 provider_id TEXT NOT NULL REFERENCES verification_plans(provider_id) ON DELETE CASCADE,
 model_id TEXT NOT NULL, key_id TEXT NOT NULL, protocol TEXT NOT NULL, effort TEXT NOT NULL,
 position INTEGER NOT NULL,
 reference_model TEXT NOT NULL DEFAULT '', -- 可选参考身份；空串按模型原始 ID 对应。
 PRIMARY KEY(provider_id,model_id,key_id),
 FOREIGN KEY(provider_id,model_id) REFERENCES provider_models(provider_id,model_id) ON DELETE CASCADE,
 FOREIGN KEY(key_id,provider_id) REFERENCES provider_keys(id,provider_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS verification_reports(
 id TEXT PRIMARY KEY, provider_id TEXT NOT NULL, model_id TEXT NOT NULL, key_id TEXT NOT NULL,
 document TEXT NOT NULL,
 FOREIGN KEY(provider_id,model_id) REFERENCES provider_models(provider_id,model_id) ON DELETE CASCADE,
 FOREIGN KEY(key_id,provider_id) REFERENCES provider_keys(id,provider_id) ON DELETE CASCADE
);`

// Manager 维护共享数据库和单轮任务；运行取消及后台循环的生命周期由桌面宿主管理。
type Manager struct {
	// Records 由宿主运行前注入，采样费用独立于最新报告保留。
	Records *records.Store
	// db、vendors、catalog 复用现有模型、供应商、加密凭据及代理能力。
	db      *sql.DB
	vendors *providers.Store
	catalog *models.Store
	// mu 保护进度与取消句柄；wg 等待在途任务结束后才关闭数据库。
	mu       sync.Mutex
	progress VerificationProgress
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	// closed 阻止宿主退出后再次接受异步任务，确保WaitGroup等待期间不新增任务。
	closed bool
	// suspended 由宿主系统睡眠事件维护，不将窗口隐藏误当睡眠。
	suspended bool
	// now 用于定时与可控时钟验收；emit 在进度变化时由宿主推送。
	now  func() time.Time
	emit func(VerificationProgress)
}

// New 建立验证表，启动时不加载检测参考数据或访问网络。
func New(db *sql.DB, vendors *providers.Store, catalog *models.Store) (*Manager, error) {
	if db == nil || vendors == nil || catalog == nil {
		return nil, errors.New("模型验证依赖尚未初始化")
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("初始化模型验证: %w", err)
	}
	// 旧计划增加可选参考身份，空值保留按原始模型 ID 对应的默认行为。
	var hasReference bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('verification_lines') WHERE name='reference_model')").Scan(&hasReference); err != nil {
		return nil, fmt.Errorf("检查验证线路版本: %w", err)
	}
	if !hasReference {
		if _, err := db.Exec("ALTER TABLE verification_lines ADD COLUMN reference_model TEXT NOT NULL DEFAULT ''"); err != nil {
			return nil, fmt.Errorf("升级验证线路: %w", err)
		}
	}
	return &Manager{db: db, vendors: vendors, catalog: catalog, now: time.Now}, nil
}

// identity 绑定完整执行目标；参考选择只改变比较依据，复测仍替换同一组合的最近报告。
func identity(t VerificationTarget) string {
	data, _ := json.Marshal([]string{t.ProviderID, t.KeyID, t.ModelID, t.RequestModel, t.URL, t.Protocol, t.Effort})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Targets 从真实供应商关联和Key权限产生不含凭据的选择项，停用项保留具体原因。
func (m *Manager) Targets(ctx context.Context) ([]VerificationTarget, error) {
	snapshot, err := m.vendors.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := []VerificationTarget{}
	for _, row := range snapshot.Providers {
		p := row.Provider
		relations, err := m.vendors.Models(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		keys, err := m.vendors.Keys(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			permissions, err := m.vendors.ModelPermissions(ctx, key.ID)
			if err != nil {
				return nil, err
			}
			for _, model := range relations {
				if model.Kind != "chat" {
					continue
				}
				request := providers.ResolveModelRequestName(model.ModelName, model.Mappings)
				t := VerificationTarget{VerificationLine: VerificationLine{ModelID: model.ModelID, KeyID: key.ID}, ProviderID: p.ID, ProviderName: p.Name, KeyName: key.Name, ModelName: model.Name, OriginalModel: model.ModelName, RequestModel: request, URL: p.URL}
				switch {
				case !p.Enabled:
					t.Reason = "供应商已停用"
				case !key.Enabled:
					t.Reason = "Key 已停用"
				case !key.HasSecret:
					t.Reason = "Key 尚未设置密钥"
				case !model.Enabled:
					t.Reason = "供应商模型已停用"
				case !permissions.AllModels && !slices.Contains(permissions.ModelIDs, model.ModelID):
					t.Reason = "Key 没有该模型权限"
				}
				result = append(result, t)
			}
		}
	}
	return result, nil
}

// resolve 在操作处重新匹配真实资源，拒绝把已停用、不兼容或删除的目标继续执行。
func (m *Manager) resolve(ctx context.Context, providerID string, line VerificationLine) (VerificationTarget, error) {
	targets, err := m.Targets(ctx)
	if err != nil {
		return VerificationTarget{}, err
	}
	for _, t := range targets {
		if t.ProviderID == providerID && t.ModelID == line.ModelID && t.KeyID == line.KeyID {
			t.VerificationLine = line
			if t.Reason != "" {
				return t, errors.New(t.Reason)
			}
			return t, nil
		}
	}
	return VerificationTarget{}, errors.New("检测目标已删除或不再关联")
}

// SavePlan 校验参考身份与线路兼容性后原子保存配置及计时；失败保留旧计划，手动执行不经过此方法。
func (m *Manager) SavePlan(ctx context.Context, p VerificationPlan) error {
	if !slices.Contains([]int{1, 6, 12, 24}, p.Hours) {
		return errors.New("检测周期只支持 1、6、12、24 小时")
	}
	if len(p.Lines) == 0 {
		return errors.New("至少配置一条完整验证线路")
	}
	// 只在保存显式参考选择时加载目录，普通页面和启动不加载参考数据。
	references := map[string]bool{}
	if slices.ContainsFunc(p.Lines, func(line VerificationLine) bool { return line.ReferenceModel != "" }) {
		options, err := m.ReferenceModels()
		if err != nil {
			return err
		}
		for _, option := range options {
			references[option.ID] = true
		}
	}
	seen := map[string]bool{}
	efforts := map[string]string{}
	for _, line := range p.Lines {
		if line.ReferenceModel != "" && !references[line.ReferenceModel] {
			return errors.New("预期参考模型不在当前参考库中，请重新选择")
		}
		if !slices.Contains([]string{"chat_completions", "responses", "messages", "gemini"}, line.Protocol) {
			return errors.New("请选择请求协议")
		}
		if !slices.Contains([]string{"auto", "low", "medium", "high"}, line.Effort) {
			return errors.New("请选择有效思考强度")
		}
		key := line.ModelID + "/" + line.KeyID
		if seen[key] {
			return errors.New("同模型同 Key 不能重复绑定")
		}
		seen[key] = true
		if value, ok := efforts[line.ModelID]; ok && value != line.Effort {
			return errors.New("同模型的思考强度必须一致")
		}
		efforts[line.ModelID] = line.Effort
		if _, err := m.resolve(ctx, p.ProviderID, line); err != nil {
			return err
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldEnabled bool
	var oldHours int
	var next, last int64
	err = tx.QueryRowContext(ctx, "SELECT enabled,hours,next_at,last_at FROM verification_plans WHERE provider_id=?", p.ProviderID).Scan(&oldEnabled, &oldHours, &next, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !oldEnabled && p.Enabled || oldHours != p.Hours || next == 0 {
		next = m.now().Add(time.Duration(p.Hours) * time.Hour).UnixMilli()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO verification_plans VALUES(?,?,?,?,?) ON CONFLICT(provider_id) DO UPDATE SET enabled=excluded.enabled,hours=excluded.hours,next_at=excluded.next_at`, p.ProviderID, p.Enabled, p.Hours, next, last); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM verification_lines WHERE provider_id=?", p.ProviderID); err != nil {
		return err
	}
	for i, line := range p.Lines {
		if _, err = tx.ExecContext(ctx, "INSERT INTO verification_lines(provider_id,model_id,key_id,protocol,effort,position,reference_model) VALUES(?,?,?,?,?,?,?)", p.ProviderID, line.ModelID, line.KeyID, line.Protocol, line.Effort, i, line.ReferenceModel); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Plans 读取配置及线路；外键删除后留下的空计划保持可编辑但不再执行。
func (m *Manager) Plans(ctx context.Context) ([]VerificationPlan, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT provider_id,enabled,hours,next_at,last_at FROM verification_plans ORDER BY provider_id")
	if err != nil {
		return nil, err
	}
	result := []VerificationPlan{}
	for rows.Next() {
		var p VerificationPlan
		if err = rows.Scan(&p.ProviderID, &p.Enabled, &p.Hours, &p.NextAt, &p.LastAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.Lines = []VerificationLine{}
		result = append(result, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		rows, err = m.db.QueryContext(ctx, "SELECT model_id,key_id,protocol,effort,reference_model FROM verification_lines WHERE provider_id=? ORDER BY position", result[i].ProviderID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var line VerificationLine
			if err = rows.Scan(&line.ModelID, &line.KeyID, &line.Protocol, &line.Effort, &line.ReferenceModel); err != nil {
				rows.Close()
				return nil, err
			}
			result[i].Lines = append(result[i].Lines, line)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// SetEnabled 只切换自动运行；从关闭到开启重新等待完整周期，不立即采样。
func (m *Manager) SetEnabled(ctx context.Context, id string, enabled bool) error {
	if !enabled {
		result, err := m.db.ExecContext(ctx, "UPDATE verification_plans SET enabled=0 WHERE provider_id=?", id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("检测计划不存在")
		}
		return nil
	}

	plans, err := m.Plans(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if p.ProviderID == id {
			if p.Enabled == enabled {
				return nil
			}
			p.Enabled = enabled
			return m.SavePlan(ctx, p)
		}
	}
	return errors.New("检测计划不存在")
}

// Reports 只返回每组合最近报告，配置地址/映射变化时清理该组合旧快照，读取不会采样。
func (m *Manager) Reports(ctx context.Context, targets []VerificationTarget) ([]VerificationReport, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT document FROM verification_reports")
	if err != nil {
		return nil, err
	}
	result := []VerificationReport{}
	stale := []string{}
	for rows.Next() {
		var text string
		var r VerificationReport
		if err = rows.Scan(&text); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(text), &r); err != nil {
			rows.Close()
			return nil, err
		}
		valid := false
		for _, t := range targets {
			if t.ProviderID == r.Target.ProviderID && t.ModelID == r.Target.ModelID && t.KeyID == r.Target.KeyID && t.URL == r.Target.URL && t.RequestModel == r.Target.RequestModel {
				valid = true
				break
			}
		}
		if valid {
			result = append(result, r)
		} else {
			stale = append(stale, r.ID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err = m.db.ExecContext(ctx, "DELETE FROM verification_reports WHERE id=?", id); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(result, func(a, b VerificationReport) int {
		if a.Conclusion == "difference" && b.Conclusion != "difference" {
			return -1
		}
		if b.Conclusion == "difference" && a.Conclusion != "difference" {
			return 1
		}
		if a.Time > b.Time {
			return -1
		}
		if a.Time < b.Time {
			return 1
		}
		return 0
	})
	return result, nil
}

// Snapshot 读取真实配置和任务状态，不加载参考库或访问上游。
func (m *Manager) Snapshot(ctx context.Context) (VerificationSnapshot, error) {
	result := VerificationSnapshot{}
	var err error
	if result.Targets, err = m.Targets(ctx); err != nil {
		return result, err
	}
	if result.Plans, err = m.Plans(ctx); err != nil {
		return result, err
	}
	if result.Reports, err = m.Reports(ctx, result.Targets); err != nil {
		return result, err
	}
	m.mu.Lock()
	result.Progress = m.progress
	m.mu.Unlock()
	return result, nil
}

// ReferenceModels 按编辑器请求读取内置参考身份，不访问网络，不保留全局评分器。
func (m *Manager) ReferenceModels() ([]ReferenceModel, error) {
	detector, err := fingerprint.LoadBundled()
	if err != nil {
		return nil, err
	}
	result := []ReferenceModel{}
	for _, model := range detector.Models() {
		result = append(result, ReferenceModel{ID: model.ID, Name: model.DisplayName})
	}
	return result, nil
}
