package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
)

// SetKeyMultiplier 只更新手动来源的倍率，范围为 0～100；写锁内重查来源，保留凭据与其余配置。
func (s *Store) SetKeyMultiplier(ctx context.Context, id string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return errors.New("手动倍率需为 0～100 之间的数值")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT document FROM provider_keys WHERE id=?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errKeyMissing
		}
		return err
	}
	var source string
	err := s.db.QueryRowContext(ctx, "SELECT json_extract(document,'$.multiplierSource') FROM provider_key_queries WHERE key_id=?", id).Scan(&source)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if source == "upstream" {
		return errors.New("自动模式下不能手动修改倍率，请先切换为手动")
	}
	var document map[string]json.RawMessage
	if err := s.decodeDocument([]byte(raw), &document); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document["multiplier"] = encoded
	data, err := s.encodeDocument(document)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE provider_keys SET document=? WHERE id=?", string(data), id)
	if err != nil {
		return err
	}
	return affected(result, errKeyMissing)
}
