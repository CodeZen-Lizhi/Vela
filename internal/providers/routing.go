package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

// ExecutionCandidate 是已通过模型关联和Key权限的配置；CredentialVersion仅是密文摘要。
type ExecutionCandidate struct {
	// Provider、Key 是当前启用配置，不含明文密钥。
	Provider Provider
	Key      ProviderKey
	// Model 是实际请求名，Version 随密文替换变化。
	Model   string
	Version string
}

// executionVersion 以加密密文计算版本，不用掩码或明文缓存健康身份。
func executionVersion(sealed []byte) string {
	sum := sha256.Sum256(sealed)
	return hex.EncodeToString(sum[:])
}

// ExecutionCandidates 查询显式关联模型和Key权限，不把未关联供应商当成可服务所有模型。
func (s *Store) ExecutionCandidates(ctx context.Context, modelName string) ([]ExecutionCandidate, error) {
	return s.ExecutionCandidatesFor(ctx, modelName, "chat")
}

// ExecutionCandidatesFor 复用模型关联、权限和映射规则，按真实业务类型选取候选。
func (s *Store) ExecutionCandidatesFor(ctx context.Context, modelName, kind string) ([]ExecutionCandidate, error) {
	files := kind == "files"
	if kind == "embedding" || kind == "rerank" {
		kind = "vector"
	}
	if kind == "realtime" || kind == "search" || kind == "files" || kind == "interactions" {
		kind = "chat"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.enabled,p.document,k.id,k.enabled,k.all_models,k.secret,k.document,COALESCE(x.document,'[]')
 FROM provider_models r JOIN providers p ON p.id=r.provider_id JOIN models m ON m.id=r.model_id
 JOIN provider_keys k ON k.provider_id=p.id LEFT JOIN provider_model_mappings x ON x.provider_id=r.provider_id AND x.model_id=r.model_id
 WHERE (? OR m.model_id=?) AND json_extract(m.document,'$.kind')=? AND p.enabled=1 AND r.enabled=1 AND k.enabled=1 AND k.secret IS NOT NULL
 AND(k.all_models=1 OR EXISTS(SELECT 1 FROM provider_key_models a WHERE a.key_id=k.id AND a.model_id=r.model_id))
 ORDER BY p.seq,k.seq`, files, modelName, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ExecutionCandidate{}
	seen := make(map[string]bool)
	for rows.Next() {
		var c ExecutionCandidate
		var pdoc, kdoc, names string
		var secret []byte
		if err = rows.Scan(&c.Provider.ID, &c.Provider.Name, &c.Provider.Enabled, &pdoc, &c.Key.ID, &c.Key.Enabled, &c.Key.AllModels, &secret, &kdoc, &names); err != nil {
			return nil, err
		}
		var pd providerDocument
		var kd keyDocument
		var mapping []ProviderModelMapping
		if err = s.decodeDocument([]byte(pdoc), &pd); err != nil {
			return nil, err
		}
		if err = s.decodeDocument([]byte(kdoc), &kd); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(names), &mapping); err != nil {
			return nil, err
		}
		c.Provider = pd.provider(c.Provider.ID, c.Provider.Name, c.Provider.Enabled)
		c.Key = kd.key(c.Key.ID, c.Provider.ID, c.Key.Enabled, c.Key.AllModels, "", true)
		c.Model = ResolveModelRequestName(modelName, mapping)
		if files {
			// Files 不携带模型；按旧版无请求模型选路，同 Key 的多个模型关系只产生一个候选。
			if seen[c.Key.ID] {
				continue
			}
			seen[c.Key.ID] = true
			c.Model = modelName
		}

		c.Version = executionVersion(secret)
		result = append(result, c)
	}
	return result, rows.Err()
}

// ExecutionSecret 在实际出站前复核模型、启停、权限及凭据版本，再解密；不返回给前端。
func (s *Store) ExecutionSecret(ctx context.Context, modelName, keyID, version, upstreamModel string) (string, error) {
	return s.ExecutionSecretFor(ctx, modelName, "chat", keyID, version, upstreamModel)
}

// ExecutionSecretFor 在实际出站处复核相应类型的模型关联、权限、映射及凭据版本。
func (s *Store) ExecutionSecretFor(ctx context.Context, modelName, kind, keyID, version, upstreamModel string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates, err := s.ExecutionCandidatesFor(ctx, modelName, kind)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(candidates, func(c ExecutionCandidate) bool {
		return c.Key.ID == keyID && c.Version == version && c.Model == upstreamModel
	})
	if i < 0 {
		return "", errors.New("供应商、模型权限或凭据已变化，请重新调用")
	}
	return s.Secret(ctx, keyID)
}
