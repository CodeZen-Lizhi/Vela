package providers

import (
	"context"
	"fmt"
)

// normalizeKeyUpdate 校验普通 Key 草稿；上游倍率始终保留数据库当前值，不能被页面的手动占位值覆盖。调用方持有 Store.mu。
func (s *Store) normalizeKeyUpdate(ctx context.Context, keyID string, draft ProviderKeyDraft) (keyDocument, string, error) {
	var configured bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='provider_key_queries')").Scan(&configured); err != nil {
		return keyDocument{}, "", err
	}
	var preserve bool
	if configured {
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_key_queries WHERE key_id=? AND json_extract(document,'$.multiplierSource')='upstream')", keyID).Scan(&preserve); err != nil {
			return keyDocument{}, "", err
		}
	}
	if !preserve {
		return draft.normalize()
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT document FROM provider_keys WHERE id=?", keyID).Scan(&raw); err != nil {
		return keyDocument{}, "", fmt.Errorf("读取上游 Key: %w", err)
	}
	var saved keyDocument
	if err := s.decodeDocument([]byte(raw), &saved); err != nil {
		return keyDocument{}, "", err
	}
	placeholder := 0.0
	draft.Multiplier = &placeholder
	doc, secret, err := draft.normalize()
	doc.Multiplier = saved.Multiplier
	return doc, secret, err
}
