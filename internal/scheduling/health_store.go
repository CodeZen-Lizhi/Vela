package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
)

// RestoreRuntime 从真实 SQLite 恢复整Key健康，启动失败不得把旧熔断状态改成满分。
func (s *Store) RestoreRuntime(ctx context.Context, r *Runtime) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduling_health(
 key_id TEXT PRIMARY KEY, -- 稳定Key身份；旧凭据状态不会用于新凭据
 document TEXT NOT NULL -- 凭据版本、分数、连续故障、熔断及冷却；不保存进程内租约
 )`); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT document FROM scheduling_health")
	if err != nil {
		return err
	}
	records := []HealthRecord{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return err
		}
		var record HealthRecord
		if err = json.Unmarshal([]byte(data), &record); err != nil {
			rows.Close()
			return fmt.Errorf("读取调度健康状态: %w", err)
		}
		records = append(records, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return r.RestoreHealth(records)
}

// SaveRuntime 在同一事务内保存运行核心当前快照；写入失败保留上次成功快照。
func (s *Store) SaveRuntime(ctx context.Context, r *Runtime) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 获得数据库写入顺序之后再取快照，避免并发结算以旧快照覆盖新状态。
	records := r.ExportHealth()
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO scheduling_health(key_id,document) VALUES(?,?) ON CONFLICT(key_id) DO UPDATE SET document=excluded.document", record.KeyID, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
