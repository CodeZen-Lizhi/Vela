package providers

import (
	"context"
	"encoding/json"
)

// removeProviderNotes 删除旧供应商文档中的备注，兼容明文及本机加密配置。
// 只移除顶层 note，保留未知字段；所有供应商在一个事务中更新，失败全部回滚。
func (s *Store) removeProviderNotes(ctx context.Context) error {
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
		if _, exists := document["note"]; !exists {
			continue
		}
		delete(document, "note")
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
