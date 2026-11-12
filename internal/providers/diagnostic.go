package providers

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// DiagnosticSnapshot 包含主动诊断所需的关联与凭据版本；启停和健康不限制诊断资格。
type DiagnosticSnapshot struct {
	// Provider、Model 为当前供应商与真实已关联模型，不包含秘密。
	Provider Provider
	Model    ProviderModel
	// Keys 仅包含已设置密钥且有该模型权限的当前供应商 Key，按优先级及创建顺序排列。
	Keys []ExecutionCandidate
}

// Diagnostic 读取主动诊断配置，保留模型关联与 Key 权限检查，不改变正式调度准入。
func (s *Store) Diagnostic(ctx context.Context, providerID, modelID string) (DiagnosticSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diagnostic(ctx, providerID, modelID)
}

// diagnostic 在调用方持锁时读取完整诊断快照，结果集关闭后才读取 Key 权限。
func (s *Store) diagnostic(ctx context.Context, providerID, modelID string) (DiagnosticSnapshot, error) {
	var snapshot DiagnosticSnapshot
	var providerDoc, mappingDoc string
	err := s.db.QueryRowContext(ctx, `SELECT p.id,p.name,p.enabled,p.document,m.id,m.model_id,COALESCE(json_extract(m.document,'$.name'),''),json_extract(m.document,'$.kind'),r.enabled,COALESCE(x.document,'[]')
 FROM providers p JOIN provider_models r ON r.provider_id=p.id JOIN models m ON m.id=r.model_id
 LEFT JOIN provider_model_mappings x ON x.provider_id=r.provider_id AND x.model_id=r.model_id
 WHERE p.id=? AND m.id=?`, providerID, modelID).Scan(&snapshot.Provider.ID, &snapshot.Provider.Name, &snapshot.Provider.Enabled, &providerDoc, &snapshot.Model.ModelID, &snapshot.Model.ModelName, &snapshot.Model.Name, &snapshot.Model.Kind, &snapshot.Model.Enabled, &mappingDoc)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, errors.New("供应商或模型关联不存在，请刷新列表")
	}
	if err != nil {
		return snapshot, err
	}
	var doc providerDocument
	if err = s.decodeDocument([]byte(providerDoc), &doc); err != nil {
		return snapshot, err
	}
	snapshot.Provider = doc.provider(snapshot.Provider.ID, snapshot.Provider.Name, snapshot.Provider.Enabled)
	if err = json.Unmarshal([]byte(mappingDoc), &snapshot.Model.Mappings); err != nil {
		return snapshot, err
	}
	rows, err := s.keyRows(ctx, providerID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Keys = []ExecutionCandidate{}
	for _, row := range rows {
		if len(row.sealed) == 0 {
			continue
		}
		if !row.allModels {
			var allowed bool
			if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_key_models WHERE key_id=? AND model_id=? AND provider_id=?)", row.id, modelID, providerID).Scan(&allowed); err != nil {
				return snapshot, err
			}
			if !allowed {
				continue
			}
		}
		key := row.doc.key(row.id, providerID, row.enabled, row.allModels, "", true)
		snapshot.Keys = append(snapshot.Keys, ExecutionCandidate{Provider: snapshot.Provider, Key: key, Model: snapshot.Model.ModelName, Version: executionVersion(row.sealed)})
	}
	slices.SortStableFunc(snapshot.Keys, func(a, b ExecutionCandidate) int { return cmp.Compare(a.Key.Priority, b.Key.Priority) })
	return snapshot, nil
}

// DiagnosticSecret 在实际出站前复核归属、关联、权限及凭据版本，诊断放行不影响正式调用。
func (s *Store) DiagnosticSecret(ctx context.Context, providerID, modelID, keyID, version string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.diagnostic(ctx, providerID, modelID)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(snapshot.Keys, func(c ExecutionCandidate) bool { return c.Key.ID == keyID && c.Version == version }) {
		return "", errors.New("Key 权限或凭据已变化，请重新测试")
	}
	return s.Secret(ctx, keyID)
}
