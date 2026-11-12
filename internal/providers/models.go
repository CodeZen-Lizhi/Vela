package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// modelSchema 用外键把关联、权限和映射连接到统一模型，删除任一主体时在原语句事务中清理从属记录。
const modelSchema = `
CREATE UNIQUE INDEX IF NOT EXISTS provider_keys_owner ON provider_keys(id,provider_id);
CREATE TABLE IF NOT EXISTS provider_models (
 provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE, -- 所属供应商
 model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE, -- 统一模型稳定身份
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), -- 供应商模型启停，不改变权限
 PRIMARY KEY(provider_id,model_id)
);
CREATE INDEX IF NOT EXISTS provider_models_model ON provider_models(model_id);
CREATE TABLE IF NOT EXISTS provider_key_models (
 key_id TEXT NOT NULL, -- 上游 Key 的稳定身份
 provider_id TEXT NOT NULL, -- 同时约束 Key 和模型关联归属
 model_id TEXT NOT NULL, -- 指定权限中的统一模型稳定身份
 PRIMARY KEY(key_id,model_id),
 FOREIGN KEY(key_id,provider_id) REFERENCES provider_keys(id,provider_id) ON DELETE CASCADE,
 FOREIGN KEY(provider_id,model_id) REFERENCES provider_models(provider_id,model_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS provider_key_models_relation ON provider_key_models(provider_id,model_id);
CREATE TABLE IF NOT EXISTS provider_model_mappings (
 provider_id TEXT NOT NULL, -- 所属供应商
 model_id TEXT NOT NULL, -- 已关联统一模型稳定身份
 document TEXT NOT NULL, -- 有序请求名及优先级的 JSON 数组
 PRIMARY KEY(provider_id,model_id),
 FOREIGN KEY(provider_id,model_id) REFERENCES provider_models(provider_id,model_id) ON DELETE CASCADE
);`

// InitModels 在 New 后初始化模型关联表；必须与模型模块复用同一已启用外键的数据库，并清理旧映射操作范围，失败时回滚表初始化和全部文档变更。
func (s *Store) InitModels() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(modelSchema); err != nil {
		return fmt.Errorf("初始化供应商模型: %w", err)
	}
	if err = removeMappingOperations(tx); err != nil {
		return fmt.Errorf("清理模型映射历史字段: %w", err)
	}
	return tx.Commit()
}

// requireProvider 在当前事务中确认供应商仍存在，避免空集合保存绕过归属检查。
func requireProvider(ctx context.Context, tx *sql.Tx, id string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE id=?)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errProviderMissing
	}
	return nil
}

// Models 读取供应商已关联模型和映射，按显示名称、稳定身份排序；空集合返回数组。
func (s *Store) Models(ctx context.Context, providerID string) ([]ProviderModel, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE id=?)", providerID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, errProviderMissing
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.model_id,COALESCE(json_extract(m.document,'$.name'),''),COALESCE(json_extract(m.document,'$.kind'),''),p.enabled,COALESCE(x.document,'[]') FROM provider_models p JOIN models m ON m.id=p.model_id LEFT JOIN provider_model_mappings x ON x.provider_id=p.provider_id AND x.model_id=p.model_id WHERE p.provider_id=? ORDER BY json_extract(m.document,'$.name'),m.id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProviderModel{}
	for rows.Next() {
		var m ProviderModel
		var data string
		if err = rows.Scan(&m.ModelID, &m.ModelName, &m.Name, &m.Kind, &m.Enabled, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &m.Mappings); err != nil {
			return nil, fmt.Errorf("读取模型映射: %w", err)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// SaveModels 在一个事务中替换全部关联，不为新增关联配置 Key 权限；移除项通过外键清理映射和指定权限，保留项状态不变。
func (s *Store) SaveModels(ctx context.Context, providerID string, draft ProviderModelsDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireProvider(ctx, tx, providerID); err != nil {
		return err
	}
	// 临时目标集合只在本次调用内保留；先读取并关闭结果集再写，适配单连接数据库。
	existing := map[string]bool{}
	rows, err := tx.QueryContext(ctx, "SELECT model_id FROM provider_models WHERE provider_id=?", providerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	target := map[string]bool{}
	added := []string{}
	for _, id := range draft.ModelIDs {
		if target[id] {
			return errors.New("关联模型不能重复")
		}
		target[id] = true
		if !existing[id] {
			added = append(added, id)
		}
	}
	for id := range existing {
		if !target[id] {
			if _, err = tx.ExecContext(ctx, "DELETE FROM provider_models WHERE provider_id=? AND model_id=?", providerID, id); err != nil {
				return err
			}
		}
	}
	for _, id := range added {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM models WHERE id=?)", id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("关联的统一模型不存在，请刷新列表")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_models(provider_id,model_id) VALUES(?,?)", providerID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetModelEnabled 即时修改供应商模型启停，不删除映射或 Key 权限。
func (s *Store) SetModelEnabled(ctx context.Context, providerID, modelID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "UPDATE provider_models SET enabled=? WHERE provider_id=? AND model_id=?", enabled, providerID, modelID)
	if err != nil {
		return err
	}
	return affected(result, errors.New("供应商模型关联不存在，请刷新列表"))
}
