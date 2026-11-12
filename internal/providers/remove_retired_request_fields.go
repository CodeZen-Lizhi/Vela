package providers

import (
	"context"
	"encoding/json"
)

// removeRetiredRequestFields 在初始化事务中移除已取消的请求策略字段，兼容明文及整文档加密。
// 清理历史转移限制与传输失败开关，兼容明文和密文；保留其他字段，失败全部回滚。
func (s *Store) removeRetiredRequestFields(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // 提交后无作用；解密或更新失败时保留所有原记录。
	rows, err := tx.QueryContext(ctx, "SELECT id,document FROM providers")
	if err != nil {
		return err
	}
	updates := map[string]string{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var document map[string]json.RawMessage
		if err = s.decodeDocument([]byte(raw), &document); err != nil {
			break
		}
		var request map[string]json.RawMessage
		if err = json.Unmarshal(document["request"], &request); err != nil {
			break
		}
		changed := false
		for _, field := range []string{"transportFailover", "maxTransfers", "maxTransferSeconds"} {
			if _, exists := request[field]; exists {
				delete(request, field)
				changed = true
			}
		}
		if !changed {
			continue
		}
		document["request"], err = json.Marshal(request)
		if err != nil {
			break
		}
		var data []byte
		data, err = s.encodeDocument(document)
		if err != nil {
			break
		}
		updates[id] = string(data)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	for id, data := range updates {
		if _, err := tx.ExecContext(ctx, "UPDATE providers SET document=? WHERE id=?", data, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
