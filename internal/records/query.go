package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// conditions 校验公开查询并构造参数化条件；列与 JSON 路径只取固定白名单。
func conditions(q Query) (string, []any, error) {
	clauses := []string{"rowid<=?"}
	args := []any{q.Before}
	if len(q.Search) > 512 {
		return "", nil, errors.New("搜索内容过长")
	}
	if q.Start > 0 && q.End > 0 && q.End < q.Start {
		return "", nil, errors.New("结束时间不能早于开始时间")
	}
	if q.Start > 0 {
		clauses = append(clauses, "started_at>=?")
		args = append(args, q.Start)
	}
	if q.End > 0 {
		clauses = append(clauses, "started_at<=?")
		args = append(args, q.End)
	}
	for _, item := range []struct{ column, value string }{{"model_id", q.ModelID}, {"provider_id", q.ProviderID}, {"protocol", q.Protocol}, {"source", q.Source}} {
		if item.value != "" {
			clauses = append(clauses, item.column+"=?")
			args = append(args, item.value)
		}
	}
	for _, word := range strings.Fields(strings.ToLower(q.Search)) {
		word = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(word)
		clauses = append(clauses, `search LIKE ? ESCAPE '\'`)
		args = append(args, "%"+word+"%")
	}
	for _, group := range []struct {
		expression      string
		values, allowed []string
	}{
		{"state", q.States, []string{"active", "success", "failed", "local"}},
		{"json_extract(document,'$.type')", q.Types, []string{"chat", "image", "video", "embedding", "rerank"}},
		{"CASE json_extract(document,'$.stream') WHEN 1 THEN 'stream' ELSE 'standard' END", q.Transports, []string{"stream", "standard"}},
		{"json_extract(document,'$.execution')", q.Executions, []string{"direct", "retry", "transfer", "both"}},
	} {
		if len(group.values) > len(group.allowed) {
			return "", nil, errors.New("筛选项无效")
		}
		if len(group.values) > 0 {
			marks := []string{}
			for _, value := range group.values {
				if !slices.Contains(group.allowed, value) {
					return "", nil, errors.New("筛选值无效")
				}
				marks = append(marks, "?")
				args = append(args, value)
			}
			clauses = append(clauses, group.expression+" IN("+strings.Join(marks, ",")+")")
		}
	}
	if len(q.Executions) > 0 {
		clauses = append(clauses, "state!='local'")
	}
	return strings.Join(clauses, " AND "), args, nil
}

// Query 在一个只读事务取得整组摘要和分页，正文始终不进入列表；Before 支持稳定阅读。
func (s *Store) Query(ctx context.Context, q Query) (Page, error) {
	page := Page{Records: []Record{}, Models: []Option{}, Providers: []Option{}, Protocols: []Option{}, Types: []Option{}}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Size == 0 {
		q.Size = 20
	}
	if q.Page < 1 || q.Page > 100001 || !slices.Contains([]int{10, 20, 50, 100}, q.Size) {
		return page, errors.New("分页参数无效")
	}
	if q.Before == 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM request_records`).Scan(&q.Before); err != nil {
			return page, err
		}
	}
	page.Before = q.Before
	where, args, err := conditions(q)
	if err != nil {
		return page, err
	}
	where += " AND (ended_at=0 OR ended_at>?)"
	args = append(args, retentionDeadline(s, 30))
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	sqlSummary := `SELECT COUNT(*),COALESCE(SUM(state='active'),0),COALESCE(SUM(state='success'),0),COALESCE(SUM(state='failed'),0),COALESCE(SUM(state='local'),0),
 COALESCE(SUM(CASE WHEN state!='active' THEN input_context+output_tokens END),0),
 COALESCE(SUM(CASE WHEN state!='active' THEN input_context END),0),
 COALESCE(SUM(CASE WHEN state!='active' THEN cache_read END),0),
 COALESCE(SUM(CASE WHEN state!='active' THEN cost END),0),
 COALESCE(SUM(state NOT IN('active','local') AND usage_known=0),0),
 COALESCE(SUM(state NOT IN('active','local') AND cost IS NULL),0),
 COALESCE(AVG(CASE WHEN ended_at>0 THEN duration_ms END),0),COALESCE(SUM(ended_at>0),0) FROM request_records WHERE ` + where
	a := &page.Summary
	err = tx.QueryRowContext(ctx, sqlSummary, args...).Scan(&a.Total, &a.Active, &a.Success, &a.Failed, &a.Local, &a.Tokens, &a.InputContext, &a.CacheRead, &a.Cost, &a.UnknownUsage, &a.UnknownCost, &a.AverageMillis, &a.Timed)
	if err != nil {
		return page, err
	}
	params := append(slices.Clone(args), q.Size, (q.Page-1)*q.Size)
	rows, err := tx.QueryContext(ctx, `SELECT document FROM request_records WHERE `+where+` ORDER BY started_at DESC,id DESC LIMIT ? OFFSET ?`, params...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var raw string
		var record Record
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &record)
		}
		if err != nil {
			break
		}
		page.Records = append(page.Records, record)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}

	for _, group := range []struct {
		kind string
		dest *[]Option
	}{{"model", &page.Models}, {"provider", &page.Providers}, {"protocol", &page.Protocols}, {"type", &page.Types}} {
		rows, err = tx.QueryContext(ctx, `SELECT value,label FROM request_options WHERE kind=? ORDER BY label`, group.kind)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var option Option
			if err = rows.Scan(&option.Value, &option.Label); err != nil {
				break
			}
			*group.dest = append(*group.dest, option)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return page, err
		}
	}

	return page, tx.Commit()
}

// Get 返回指定记录完整详情，过期正文即使到期任务尚未执行也不可读。
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	var r Record
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM request_records WHERE id=? AND (ended_at=0 OR ended_at>?)`, id, retentionDeadline(s, 30)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errors.New("请求记录不存在或已清理")
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT number,document FROM request_payloads WHERE request_id=? ORDER BY number`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var number int
		var p Payload
		if err = rows.Scan(&number, &raw); err != nil {
			return r, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return r, err
		}
		if r.EndedAt > 0 && r.EndedAt <= retentionDeadline(s, 7) {
			p.RequestBody = ""
			p.ResponseBody = ""
			p.BodyState = "expired"
		}
		if number == 0 {
			r.Client = &p
		} else if number <= len(r.Attempts) {
			r.Attempts[number-1].Payload = &p
		}
	}
	if err = rows.Err(); err != nil {
		return r, err
	}
	rows.Close()
	r.CurrentProviderName = "已删除"
	r.CurrentModelName = "已删除"
	r.CurrentKeyName = "已删除"
	for _, item := range []struct {
		query, id string
		dest      *string
	}{{"SELECT name FROM providers WHERE id=?", r.ProviderID, &r.CurrentProviderName}, {"SELECT json_extract(document,'$.name') FROM models WHERE id=?", r.ModelID, &r.CurrentModelName}} {
		if item.id == "" {
			*item.dest = "—"
			continue
		}
		e := s.db.QueryRowContext(ctx, item.query, item.id).Scan(item.dest)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return r, e
		}
	}
	return r, nil
}

// Active 按已展示身份更新，关闭实时或翻页时不会顺带插入新记录。
func (s *Store) Active(ctx context.Context, ids []string) ([]Record, error) {
	if len(ids) > 100 {
		return nil, errors.New("活跃记录数量超过一页上限")
	}
	result := []Record{}
	if len(ids) == 0 {
		return result, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT document FROM request_records WHERE id IN(`+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r Record
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Totals 读取清理后仍保留的日、供应商、模型和协议累计，不从明细回算。
func (s *Store) Totals(ctx context.Context, q Query) ([]Aggregate, error) {
	if q.Source != "" || q.Search != "" || len(q.States) > 0 || len(q.Types) > 0 || len(q.Executions) > 0 || len(q.Transports) > 0 {
		return nil, errors.New("长期统计仅支持时间、供应商、模型与协议筛选")
	}
	where := []string{"1=1"}
	args := []any{}
	for _, v := range []struct{ column, value string }{{"t.provider_id", q.ProviderID}, {"t.model_id", q.ModelID}, {"t.protocol", q.Protocol}} {
		if v.value != "" {
			where = append(where, v.column+"=?")
			args = append(args, v.value)
		}
	}
	if q.Start > 0 {
		where = append(where, "t.day>=?")
		args = append(args, timeDay(s, q.Start))
	}
	if q.End > 0 {
		where = append(where, "t.day<=?")
		args = append(args, timeDay(s, q.End))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.day,t.provider_id,t.model_id,t.protocol,COALESCE(p.name,t.provider_name||CASE WHEN t.provider_id!='' THEN '（已删除）' ELSE '' END),COALESCE(json_extract(m.document,'$.name'),t.model_name||'（已删除）'),t.requests,t.tokens,t.cost,t.unknown_usage,t.unknown_cost FROM request_totals t LEFT JOIN providers p ON p.id=t.provider_id LEFT JOIN models m ON m.id=t.model_id WHERE `+strings.Join(where, " AND ")+` ORDER BY t.day DESC,t.provider_id,t.model_id,t.protocol LIMIT 10001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Aggregate{}
	for rows.Next() {
		var a Aggregate
		if err = rows.Scan(&a.Day, &a.ProviderID, &a.ModelID, &a.Protocol, &a.ProviderName, &a.ModelName, &a.Requests, &a.Tokens, &a.Cost, &a.UnknownUsage, &a.UnknownCost); err != nil {
			return nil, fmt.Errorf("读取长期统计: %w", err)
		}
		result = append(result, a)
	}
	if len(result) > 10000 {
		return nil, errors.New("统计维度过多，请缩小时间或资源范围")
	}
	return result, rows.Err()
}

// timeDay 在首次固定的统计时区解释查询边界。
func timeDay(s *Store, ms int64) string { return time.UnixMilli(ms).In(s.zone).Format("2006-01-02") }
