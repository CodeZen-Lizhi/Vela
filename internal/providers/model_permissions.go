package providers

import (
	"context"
	"database/sql"
	"errors"
)

// ModelPermissions 读取一把 Key 的独立权限模式和指定模型，指定零个不会被解释为全部。
func (s *Store) ModelPermissions(ctx context.Context, keyID string) (ProviderModelPermissions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProviderModelPermissions{}, err
	}
	defer tx.Rollback()
	p := ProviderModelPermissions{ModelIDs: []string{}}
	if err = tx.QueryRowContext(ctx, "SELECT all_models FROM provider_keys WHERE id=?", keyID).Scan(&p.AllModels); errors.Is(err, sql.ErrNoRows) {
		return p, errors.New("上游 Key 不存在，请刷新列表")
	} else if err != nil {
		return p, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT model_id FROM provider_key_models WHERE key_id=? ORDER BY model_id", keyID)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return p, err
		}
		p.ModelIDs = append(p.ModelIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// SaveModelPermissions 原子替换当前 Key 的权限；全部模式必须传空列表，指定空列表明确表示不允许任何模型。
// 界面主动取消全部选择后应传 AllModels=true，清理关联则只删对应行而保留原模式。
func (s *Store) SaveModelPermissions(ctx context.Context, keyID string, p ProviderModelPermissions) error {
	if p.AllModels && len(p.ModelIDs) > 0 {
		return errors.New("全部模型模式不能同时指定模型")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var providerID string
	if err = tx.QueryRowContext(ctx, "SELECT provider_id FROM provider_keys WHERE id=?", keyID).Scan(&providerID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("上游 Key 不存在，请刷新列表")
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM provider_key_models WHERE key_id=?", keyID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range p.ModelIDs {
		if seen[id] {
			return errors.New("指定模型不能重复")
		}
		seen[id] = true
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_models WHERE provider_id=? AND model_id=?)", providerID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("指定模型未关联到当前供应商")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_key_models(key_id,provider_id,model_id) VALUES(?,?,?)", keyID, providerID, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE provider_keys SET all_models=? WHERE id=?", p.AllModels, keyID); err != nil {
		return err
	}
	return tx.Commit()
}
