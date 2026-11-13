package app

import (
	"context"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// Overview 返回长期小时汇总，通过供应商模块解密解析当前 Key 名称，不在统计 SQL 中读取配置密文。
func (s *VelaService) Overview(ctx context.Context) (records.OverviewSnapshot, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	snapshot, err := store.Overview(ctx)
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	if s.providers == nil {
		return snapshot, nil
	}
	names := map[string]map[string]string{}
	for i := range snapshot.Rows {
		row := &snapshot.Rows[i]
		if row.KeyID == "" {
			continue
		}
		if _, loaded := names[row.ProviderID]; !loaded {
			current, readErr := s.providers.KeyNames(ctx, row.ProviderID, "")
			if readErr != nil {
				return records.OverviewSnapshot{}, readErr
			}
			names[row.ProviderID] = current
		}
		if name, found := names[row.ProviderID][row.KeyID]; found {
			row.KeyName = name
		}
	}
	return snapshot, nil
}
