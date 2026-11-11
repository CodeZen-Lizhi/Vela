package providers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// normalizeModelMappings 校验请求名和优先级，按旧版归一化操作范围；保持列表顺序并独立复制输入。
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
		operations := []string{}
		for _, raw := range item.Operations {
			op := strings.ToLower(strings.TrimSpace(raw))
			if op != "" && !slices.Contains(operations, op) {
				operations = append(operations, op)
			}
		}
		item.Operations = operations
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

// ResolveModelRequestName 只解析模型映射，不执行网关、Key 选路或启停准入；存在受限默认名且范围不符时返回不可用。
// 排序沿用旧调度：优先级、带操作范围者、请求名；没有适用映射且默认名未受限时使用默认名。
func ResolveModelRequestName(defaultName, operation string, mappings []ProviderModelMapping) (string, bool) {
	operation = strings.ToLower(strings.TrimSpace(operation))
	var selected *ProviderModelMapping
	defaultAvailable := true
	for i := range mappings {
		item := &mappings[i]
		matches := len(item.Operations) == 0 || slices.Contains(item.Operations, operation)
		if item.RequestName == defaultName {
			defaultAvailable = matches
		}
		if !matches {
			continue
		}
		if selected == nil || item.Priority < selected.Priority ||
			(item.Priority == selected.Priority && len(item.Operations) > 0 && len(selected.Operations) == 0) ||
			(item.Priority == selected.Priority && (len(item.Operations) > 0) == (len(selected.Operations) > 0) && item.RequestName < selected.RequestName) {
			selected = item
		}
	}
	if selected != nil {
		return selected.RequestName, true
	}
	if defaultAvailable {
		return defaultName, true
	}
	return "", false
}
