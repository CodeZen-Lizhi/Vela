package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OverviewSnapshot 合并长期结算统计与当前进行中调用；空库 firstDay 为空，rows 为空数组。
type OverviewSnapshot struct {
	// Timezone 为记录模块固定时区；FirstDay 是最早统计日（YYYY-MM-DD）。
	Timezone string `json:"timezone"`
	FirstDay string `json:"firstDay"`
	// Rows 包含小时汇总及无法恢复小时的历史日累计（归入当日零点）。
	Rows []OverviewRow `json:"rows"`
}

// OverviewRow 以稳定身份分组；KeyID 为空是逻辑请求，非空是实际尝试，二者禁止混加。
type OverviewRow struct {
	// Time 为小时起点 Unix 毫秒；Day 使用固定统计时区。
	Time int64  `json:"time"`
	Day  string `json:"day"`
	// 资源身份稳定；名称读取当前配置，不存在时明确标记已删除。
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	ModelID      string `json:"modelId"`
	ModelName    string `json:"modelName"`
	Protocol     string `json:"protocol"`
	KeyID        string `json:"keyId"`
	KeyName      string `json:"keyName"`
	// Metrics 是该维度的可加总指标。
	Metrics OverviewMetrics `json:"metrics"`
}

// OverviewMetrics 只累计已知量；未知值以数量表达，不将未知结果推断为成功。
type OverviewMetrics struct {
	// Requests 为逻辑请求数或实际尝试数；Success、Failed 仅统计已知结果，DetailsKnown 为可恢复详情数。
	Requests     float64 `json:"requests"`
	Success      float64 `json:"success"`
	Failed       float64 `json:"failed"`
	DetailsKnown float64 `json:"detailsKnown"`
	// Active 为正在执行的逻辑请求，不参与已完成成功率与用量结算。
	Active float64 `json:"active"`
	// Tokens 为 Context+Output；Input 为非缓存输入，Cached 为缓存读取，均以 Token 计。
	Tokens  float64 `json:"tokens"`
	Input   float64 `json:"input"`
	Output  float64 `json:"output"`
	Cached  float64 `json:"cached"`
	Context float64 `json:"context"`
	// Cost、StandardCost 为估算 USD；标准费用使用开始时价格快照和倍率一。
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standardCost"`
	// UnknownUsage、UnknownCost、UnknownStandard 为对应未知的调用数量。
	UnknownUsage    float64 `json:"unknownUsage"`
	UnknownCost     float64 `json:"unknownCost"`
	UnknownStandard float64 `json:"unknownStandard"`
	// FirstByteTotal 为首字毫秒合计，OutputRateTotal 为每秒输出 Token 速率合计；Count 为各自样本数。
	FirstByteTotal  float64 `json:"firstByteTotal"`
	FirstByteCount  float64 `json:"firstByteCount"`
	OutputRateTotal float64 `json:"outputRateTotal"`
	OutputRateCount float64 `json:"outputRateCount"`
}

// overviewSchema 保存长期小时累计，主键包含 Key 身份以隔离请求与尝试；metrics 是 OverviewMetrics JSON。
const overviewSchema = `CREATE TABLE IF NOT EXISTS request_overview(
 time INTEGER NOT NULL,day TEXT NOT NULL,provider_id TEXT NOT NULL,model_id TEXT NOT NULL,protocol TEXT NOT NULL,key_id TEXT NOT NULL,
 provider_name TEXT NOT NULL,model_name TEXT NOT NULL,key_name TEXT NOT NULL,metrics TEXT NOT NULL CHECK(json_valid(metrics)),
 PRIMARY KEY(time,provider_id,model_id,protocol,key_id));
 CREATE INDEX IF NOT EXISTS request_overview_day ON request_overview(day,provider_id,model_id,protocol,key_id);`

// add 合并同一维度的已知指标及未知数量，不推断缺失字段。
func (m *OverviewMetrics) add(n OverviewMetrics) {
	m.Requests += n.Requests
	m.Success += n.Success
	m.Failed += n.Failed
	m.DetailsKnown += n.DetailsKnown
	m.Active += n.Active
	m.Tokens += n.Tokens
	m.Input += n.Input
	m.Output += n.Output
	m.Cached += n.Cached
	m.Context += n.Context
	m.Cost += n.Cost
	m.StandardCost += n.StandardCost
	m.UnknownUsage += n.UnknownUsage
	m.UnknownCost += n.UnknownCost
	m.UnknownStandard += n.UnknownStandard
	m.FirstByteTotal += n.FirstByteTotal
	m.FirstByteCount += n.FirstByteCount
	m.OutputRateTotal += n.OutputRateTotal
	m.OutputRateCount += n.OutputRateCount
}

// overviewMetrics 从已完成事实提取用量、费用、状态与性能；local 是已知未出站，不计成功。
func overviewMetrics(r Record) OverviewMetrics {
	m := OverviewMetrics{Requests: 1, DetailsKnown: 1}
	if r.State == "active" {
		m.Active = 1
		return m
	}
	if r.State == "success" {
		m.Success = 1
	} else if r.State == "failed" || r.State == "local" {
		m.Failed = 1
	}
	if r.Usage != nil {
		m.Input = float64(r.Usage.Input)
		m.Output = float64(r.Usage.Output)
		m.Cached = float64(r.Usage.CacheRead)
		m.Context = float64(r.Usage.Context)
		m.Tokens = m.Context + m.Output
	} else if r.State != "local" {
		m.UnknownUsage = 1
	}
	if r.Cost != nil {
		m.Cost = *r.Cost
	} else if r.State != "local" {
		m.UnknownCost = 1
	}
	if r.State != "local" {
		one := 1.0
		usages := []*Usage{r.Usage}
		if len(r.Attempts) > 0 {
			usages = nil
			for _, a := range r.Attempts {
				if a.BillingPending && r.Usage != nil {
					continue
				}
				usages = append(usages, a.Usage)
			}
		}
		// 每次尝试独立套用阶梯与时间规则，不能将重试用量合并后重新定价。
		for _, usage := range usages {
			standard, _ := estimate(r.Pricing, usage, &one, r.Tier, time.UnixMilli(r.StartedAt))
			if standard != nil {
				m.StandardCost += *standard
			} else {
				m.UnknownStandard = 1
			}
		}
	}
	if r.FirstByteMillis != nil {
		m.FirstByteTotal = float64(*r.FirstByteMillis)
		m.FirstByteCount = 1
	}
	if r.OutputRate != nil {
		m.OutputRateTotal = *r.OutputRate
		m.OutputRateCount = 1
	}
	return m
}

// saveOverview 与终态、日累计同事务写入；活跃请求不进入已完成汇总。
func (s *Store) saveOverview(ctx context.Context, tx *sql.Tx, r Record) error {
	if r.EndedAt == 0 || r.State == "active" {
		return nil
	}
	start := time.UnixMilli(r.StartedAt).In(s.zone)
	// 用本地分钟数回退以保留半小时偏移时区及夏令时重复小时的实际时间。
	hour := start.Add(-time.Duration(start.Minute())*time.Minute - time.Duration(start.Second())*time.Second - time.Duration(start.Nanosecond()))
	row := OverviewRow{Time: hour.UnixMilli(), Day: start.Format("2006-01-02"), ProviderID: r.ProviderID, ProviderName: r.ProviderName, ModelID: r.ModelID, ModelName: r.ModelName, Protocol: r.Protocol, Metrics: overviewMetrics(r)}
	if err := putOverview(ctx, tx, row); err != nil {
		return err
	}
	for _, a := range r.Attempts {
		// 无 Key 身份的尝试不能伪装成逻辑请求行；逻辑消耗仍由上行保留。
		if a.KeyID == "" {
			continue
		}
		state := "unknown"
		if a.Error != "" || a.Status >= 400 {
			state = "failed"
		} else if a.Status >= 200 && a.Status < 400 || a.Status == 101 {
			state = "success"
		}
		attempt := Record{StartedAt: r.StartedAt, State: state, Usage: a.Usage, Cost: a.Cost, Pricing: r.Pricing, Tier: r.Tier, FirstByteMillis: a.FirstByteMillis}
		row.ProviderID = a.ProviderID
		row.ProviderName = a.ProviderName
		row.KeyID = a.KeyID
		row.KeyName = a.KeyName
		row.Metrics = overviewMetrics(attempt)
		// 视频提交/轮询的待结算响应不是第二笔消耗，与已有 Key 累计保持一致。
		if a.BillingPending && r.Usage != nil {
			row.Metrics.UnknownUsage = 0
			row.Metrics.UnknownCost = 0
			row.Metrics.UnknownStandard = 0
		}
		if a.Usage != nil && a.FirstByteMillis != nil {
			duration := a.DurationMillis
			if r.Stream {
				duration -= *a.FirstByteMillis
			}
			if duration > 0 && a.Usage.Output > 0 {
				rate := float64(a.Usage.Output) * 1000 / float64(duration)
				if !(r.Stream && float64(duration)/float64(max(1, a.DurationMillis)) < 0.1 && rate > 5000) {
					row.Metrics.OutputRateTotal = rate
					row.Metrics.OutputRateCount = 1
				}
			}
		}
		if err := putOverview(ctx, tx, row); err != nil {
			return err
		}
	}
	return nil
}

// putOverview 累加主键维度的 JSON 指标，解析失败使整个结算或迁移回滚。
func putOverview(ctx context.Context, tx *sql.Tx, row OverviewRow) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT metrics FROM request_overview WHERE time=? AND provider_id=? AND model_id=? AND protocol=? AND key_id=?`, row.Time, row.ProviderID, row.ModelID, row.Protocol, row.KeyID).Scan(&raw)
	if err == nil {
		var old OverviewMetrics
		if err = json.Unmarshal([]byte(raw), &old); err != nil {
			return fmt.Errorf("读取概览累计: %w", err)
		}
		row.Metrics.add(old)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	data, err := json.Marshal(row.Metrics)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO request_overview VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(time,provider_id,model_id,protocol,key_id) DO UPDATE SET provider_name=excluded.provider_name,model_name=excluded.model_name,key_name=excluded.key_name,metrics=excluded.metrics`, row.Time, row.Day, row.ProviderID, row.ModelID, row.Protocol, row.KeyID, row.ProviderName, row.ModelName, row.KeyName, string(data))
	return err
}

// migrateOverview 一次性、原子回填存续明细及已清理日累计；完成标记与回填一同提交，重启不会重复。
func (s *Store) migrateOverview() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, overviewSchema); err != nil {
		return err
	}
	var done string
	err = tx.QueryRowContext(ctx, `SELECT document FROM settings WHERE name='records-overview-v1'`).Scan(&done)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// keyset 分批读取正文，每批关闭游标再写，避免单连接 SQLite 的游标生命周期冲突。
	last := ""
	for {
		rows, e := tx.QueryContext(ctx, `SELECT id,document FROM request_records WHERE ended_at>0 AND state!='active' AND id>? ORDER BY id LIMIT 256`, last)
		if e != nil {
			return e
		}
		batch := []Record{}
		for rows.Next() {
			var id, raw string
			if e = rows.Scan(&id, &raw); e != nil {
				break
			}
			var r Record
			if e = json.Unmarshal([]byte(raw), &r); e != nil {
				break
			}
			if r.ID != id || r.EndedAt <= 0 || r.State == "active" {
				e = errors.New("历史请求正文与结算状态不一致")
				break
			}
			last = id
			batch = append(batch, r)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return fmt.Errorf("回填概览明细: %w", e)
		}
		for _, r := range batch {
			if e = s.saveOverview(ctx, tx, r); e != nil {
				return e
			}
		}
		if len(batch) < 256 {
			break
		}
	}
	if err = s.backfillOverviewTotals(ctx, tx, false); err != nil {
		return err
	}
	if err = s.backfillOverviewTotals(ctx, tx, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(name,document) VALUES('records-overview-v1','done')`); err != nil {
		return err
	}
	return tx.Commit()
}

// backfillOverviewTotals 按日及完整身份减去已回填明细，只补已清理部分；无法恢复的状态、分项和标准费用保持未知。
func (s *Store) backfillOverviewTotals(ctx context.Context, tx *sql.Tx, keys bool) error {
	source := `SELECT day,provider_id,model_id,protocol,'' AS key_id,provider_name,model_name,'' AS key_name,requests,tokens,cost,unknown_usage,unknown_cost FROM request_totals`
	if keys {
		source = `SELECT day,provider_id,model_id,protocol,key_id,'' AS provider_name,'' AS model_name,key_name,attempts AS requests,tokens,cost,unknown_usage,unknown_cost FROM request_key_totals WHERE key_id!=''`
	}
	// 子查询按完整维度求和后相减，不能仅减 requests 而重复保留已回填 tokens/cost。
	query := `WITH known AS (SELECT day,provider_id,model_id,protocol,key_id,SUM(json_extract(metrics,'$.requests')) requests,SUM(json_extract(metrics,'$.tokens')) tokens,SUM(json_extract(metrics,'$.cost')) cost,SUM(json_extract(metrics,'$.unknownUsage')) unknown_usage,SUM(json_extract(metrics,'$.unknownCost')) unknown_cost FROM request_overview GROUP BY day,provider_id,model_id,protocol,key_id)
 SELECT t.day,t.provider_id,t.model_id,t.protocol,t.key_id,t.provider_name,t.model_name,t.key_name,t.requests-COALESCE(k.requests,0),t.tokens-COALESCE(k.tokens,0),t.cost-COALESCE(k.cost,0),t.unknown_usage-COALESCE(k.unknown_usage,0),t.unknown_cost-COALESCE(k.unknown_cost,0)
 FROM (` + source + `) t LEFT JOIN known k USING(day,provider_id,model_id,protocol,key_id)
 ORDER BY t.day,t.provider_id,t.model_id,t.protocol,t.key_id LIMIT 256 OFFSET ?`
	for offset := 0; ; offset += 256 {
		rows, err := tx.QueryContext(ctx, query, offset)
		if err != nil {
			return err
		}
		batch := []OverviewRow{}
		for rows.Next() {
			var r OverviewRow
			m := &r.Metrics
			if err = rows.Scan(&r.Day, &r.ProviderID, &r.ModelID, &r.Protocol, &r.KeyID, &r.ProviderName, &r.ModelName, &r.KeyName, &m.Requests, &m.Tokens, &m.Cost, &m.UnknownUsage, &m.UnknownCost); err != nil {
				break
			}
			day, e := time.ParseInLocation("2006-01-02", r.Day, s.zone)
			if e != nil {
				err = fmt.Errorf("历史统计日期无效: %w", e)
				break
			}
			r.Time = day.UnixMilli()
			m.UnknownStandard = m.Requests
			if m.Requests < 0 || m.Tokens < 0 || m.UnknownUsage < 0 || m.UnknownCost < 0 {
				err = errors.New("历史日累计少于存续明细，概览迁移已回滚")
				break
			}
			batch = append(batch, r)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range batch {
			if r.Metrics.Requests == 0 {
				continue
			}
			if err = putOverview(ctx, tx, r); err != nil {
				return err
			}
		}
		if len(batch) < 256 {
			return nil
		}
	}
}

// Overview 在只读事务中解析模型、供应商名称与小时累计；Key 配置已加密，仅判断存在性，当前名称由应用层通过供应商模块解析。
func (s *Store) Overview(ctx context.Context) (OverviewSnapshot, error) {
	result := OverviewSnapshot{Timezone: s.zone.String(), Rows: []OverviewRow{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT o.time,o.day,o.provider_id,CASE WHEN o.provider_id='' THEN '' ELSE COALESCE(p.name,o.provider_name||'（已删除）') END,o.model_id,COALESCE(json_extract(m.document,'$.name'),o.model_name||'（已删除）'),o.protocol,o.key_id,CASE WHEN o.key_id='' THEN '' WHEN k.id IS NULL THEN o.key_name||'（已删除）' ELSE o.key_name END,o.metrics FROM request_overview o LEFT JOIN providers p ON p.id=o.provider_id LEFT JOIN models m ON m.id=o.model_id LEFT JOIN provider_keys k ON k.id=o.key_id AND k.provider_id=o.provider_id ORDER BY o.time,o.provider_id,o.model_id,o.protocol,o.key_id LIMIT 100001`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var r OverviewRow
		var raw string
		if err = rows.Scan(&r.Time, &r.Day, &r.ProviderID, &r.ProviderName, &r.ModelID, &r.ModelName, &r.Protocol, &r.KeyID, &r.KeyName, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &r.Metrics); err != nil {
			break
		}
		if result.FirstDay == "" || r.Day < result.FirstDay {
			result.FirstDay = r.Day
		}
		result.Rows = append(result.Rows, r)
		if len(result.Rows) > 100000 {
			err = errors.New("概览统计维度超过十万，无法返回完整快照")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return OverviewSnapshot{}, fmt.Errorf("读取概览统计: %w", err)
	}
	// 活跃请求只在读取时合并，不写长期表；终态后由同事务结算替换，避免重复计数。
	active, err := tx.QueryContext(ctx, `SELECT document FROM request_records WHERE state='active' LIMIT 100001`)
	if err != nil {
		return OverviewSnapshot{}, err
	}
	for active.Next() {
		var raw string
		var r Record
		if err = active.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err != nil {
			break
		}
		start := time.UnixMilli(r.StartedAt).In(s.zone)
		day := start.Format("2006-01-02")
		if result.FirstDay == "" || day < result.FirstDay {
			result.FirstDay = day
		}
		result.Rows = append(result.Rows, OverviewRow{Time: r.StartedAt, Day: day, ProviderID: r.ProviderID, ProviderName: r.ProviderName, ModelID: r.ModelID, ModelName: r.ModelName, Protocol: r.Protocol, Metrics: overviewMetrics(r)})
		if len(result.Rows) > 100000 {
			err = errors.New("概览统计维度超过十万，无法返回完整快照")
			break
		}
	}
	if err == nil {
		err = active.Err()
	}
	active.Close()
	if err != nil {
		return OverviewSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return OverviewSnapshot{}, err
	}
	return result, nil
}
