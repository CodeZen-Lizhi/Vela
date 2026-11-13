package app

import (
	"context"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// Overview 返回调用的长期小时汇总，逻辑请求与 Key 实际尝试通过 keyId 区分。
func (s *VelaService) Overview(ctx context.Context) (records.OverviewSnapshot, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	return store.Overview(ctx)
}
