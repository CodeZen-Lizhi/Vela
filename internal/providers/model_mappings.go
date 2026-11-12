package providers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// normalizeModelMappings 校验请求名和优先级，保持列表顺序并独立复制输入。
func normalizeModelMappings(input []ProviderModelMapping) ([]ProviderModelMapping, error) {
	result := make([]ProviderModelMapping, 0, len(input))
	names := map[string]bool{}
	for i, item := range input {
		item.RequestName = strings.TrimSpace(item.RequestName)
		if item.RequestName == "" {
			return nil, errors.New("上游请求名不能为空")
		}
		if names[item.RequestName] {
			return nil, errors.New("同一模型的上游请求名不能重复")
		}
		names[item.RequestName] = true
		if item.Priority < 0 {
			return nil, errors.New("映射优先级必须为正整数")
		}
		if item.Priority == 0 {
			item.Priority = i + 1
		}
		result = append(result, item)
	}
	return result, nil
}

// SaveMappings 将提交的全部模型草稿在一个事务中保存，未提交的模型保留；空映射恢复默认名，任一错误全部回滚。
func (s *Store) SaveMappings(ctx context.Context, providerID string, drafts []ProviderModelMappingsDraft) error {
	// 在写事务前完成纯输入校验，避免无效草稿占用数据库。
	normalized := make([]ProviderModelMappingsDraft, 0, len(drafts))
	seen := map[string]bool{}
	for _, draft := range drafts {
		if seen[draft.ModelID] {
			return errors.New("同一模型不能重复提交映射")
		}
		seen[draft.ModelID] = true
		items, err := normalizeModelMappings(draft.Mappings)
		if err != nil {
			return err
		}
		normalized = append(normalized, ProviderModelMappingsDraft{ModelID: draft.ModelID, Mappings: items})
	}
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
	for _, draft := range normalized {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_models WHERE provider_id=? AND model_id=?)", providerID, draft.ModelID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("模型未关联到当前供应商，请刷新列表")
		}
		if len(draft.Mappings) == 0 {
			if _, err = tx.ExecContext(ctx, "DELETE FROM provider_model_mappings WHERE provider_id=? AND model_id=?", providerID, draft.ModelID); err != nil {
				return err
			}
			continue
		}
		data, err := json.Marshal(draft.Mappings)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_model_mappings(provider_id,model_id,document) VALUES(?,?,?) ON CONFLICT(provider_id,model_id) DO UPDATE SET document=excluded.document", providerID, draft.ModelID, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResolveModelRequestName 按优先级、请求名选择映射；无配置时使用默认名，不执行选路或启停准入。
// 历史同优先级映射仍按请求名排序，不重写其存储顺序。
func ResolveModelRequestName(defaultName string, mappings []ProviderModelMapping) string {
	var selected *ProviderModelMapping
	for i := range mappings {
		item := &mappings[i]
		if selected == nil || item.Priority < selected.Priority ||
			(item.Priority == selected.Priority && item.RequestName < selected.RequestName) {
			selected = item
		}
	}
	if selected != nil {
		return selected.RequestName
	}
	return defaultName
}
