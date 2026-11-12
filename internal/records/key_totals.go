package records

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// keySchema 独立保留各上游 Key 的实际尝试消耗，清理请求明细不会删除其归属。
const keySchema = `CREATE TABLE IF NOT EXISTS request_key_totals(
 day TEXT NOT NULL,provider_id TEXT NOT NULL,model_id TEXT NOT NULL,protocol TEXT NOT NULL,key_id TEXT NOT NULL,
 key_name TEXT NOT NULL,attempts INTEGER NOT NULL,tokens INTEGER NOT NULL,cost REAL NOT NULL,unknown_usage INTEGER NOT NULL,unknown_cost INTEGER NOT NULL,
 PRIMARY KEY(day,provider_id,model_id,protocol,key_id));`

// KeyAggregate 表达真实出站次数和 Key 消耗，不能作为逻辑请求数累加到页面摘要。
type KeyAggregate struct {
	// Day、ProviderID、ModelID、Protocol、KeyID 为稳定归属维度。
	Day        string `json:"day"`
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	Protocol   string `json:"protocol"`
	KeyID      string `json:"keyId"`
	// KeyName 保留最后已知名称，应用层优先解析当前配置名称。
	KeyName string `json:"keyName"`
	// Attempts 为实际网络尝试次数；未知用量和费用单独计数。
	Attempts     int64   `json:"attempts"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	UnknownUsage int64   `json:"unknownUsage"`
	UnknownCost  int64   `json:"unknownCost"`
}

// saveKeyTotals 与逻辑请求终态同事务写入，按每次实际尝试分配消耗，避免全归最终 Key。
func (s *Store) saveKeyTotals(ctx context.Context, tx *sql.Tx, r Record) error {
	day := time.UnixMilli(r.StartedAt).In(s.zone).Format("2006-01-02")
	for _, a := range r.Attempts {
		tokens, unknownUsage, unknownCost, cost := int64(0), 0, 0, 0.0
		if a.Usage != nil {
			tokens = a.Usage.Context + a.Usage.Output
		} else if !a.BillingPending || r.Usage == nil {
			unknownUsage = 1
		}
		if a.Cost != nil {
			cost = *a.Cost
		} else if !a.BillingPending || r.Usage == nil {
			unknownCost = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_key_totals VALUES(?,?,?,?,?,?,1,?,?,?,?) ON CONFLICT(day,provider_id,model_id,protocol,key_id) DO UPDATE SET key_name=excluded.key_name,attempts=attempts+1,tokens=tokens+excluded.tokens,cost=cost+excluded.cost,unknown_usage=unknown_usage+excluded.unknown_usage,unknown_cost=unknown_cost+excluded.unknown_cost`, day, a.ProviderID, r.ModelID, r.Protocol, a.KeyID, a.KeyName, tokens, cost, unknownUsage, unknownCost); err != nil {
			return err
		}
	}
	return nil
}

// KeyTotals 读取独立 Key 累计，只接受长期统计支持的筛选。
func (s *Store) KeyTotals(ctx context.Context, q Query) ([]KeyAggregate, error) {
	if q.Source != "" || q.Search != "" || len(q.States)+len(q.Types)+len(q.Executions)+len(q.Transports) > 0 {
		return nil, errors.New("Key 累计仅支持时间、供应商、模型与协议筛选")
	}
	where := []string{"1=1"}
	args := []any{}
	for _, item := range []struct{ column, value string }{{"provider_id", q.ProviderID}, {"model_id", q.ModelID}, {"protocol", q.Protocol}} {
		if item.value != "" {
			where = append(where, item.column+"=?")
			args = append(args, item.value)
		}
	}
	if q.Start > 0 {
		where = append(where, "day>=?")
		args = append(args, timeDay(s, q.Start))
	}
	if q.End > 0 {
		where = append(where, "day<=?")
		args = append(args, timeDay(s, q.End))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT day,provider_id,model_id,protocol,key_id,key_name,attempts,tokens,cost,unknown_usage,unknown_cost FROM request_key_totals WHERE `+strings.Join(where, " AND ")+` ORDER BY day DESC,provider_id,model_id,protocol,key_id LIMIT 10001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []KeyAggregate{}
	for rows.Next() {
		var a KeyAggregate
		if err = rows.Scan(&a.Day, &a.ProviderID, &a.ModelID, &a.Protocol, &a.KeyID, &a.KeyName, &a.Attempts, &a.Tokens, &a.Cost, &a.UnknownUsage, &a.UnknownCost); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	if len(result) > 10000 {
		return nil, errors.New("统计维度过多，请缩小时间或资源范围")
	}
	return result, rows.Err()
}

// RememberKeyNames 在配置删除前保存最后名称，不改写请求发生时快照。
func (s *Store) RememberKeyNames(ctx context.Context, names map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, name := range names {
		if _, err = tx.ExecContext(ctx, `UPDATE request_key_totals SET key_name=? WHERE key_id=?`, name, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
