package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// recordStore 拒绝未初始化的真实记录操作，浏览器预览不伪造数据。
func (s *VelaService) recordStore() (*records.Store, error) {
	if s.records == nil {
		return nil, errors.New("请求记录尚未初始化，请使用桌面应用")
	}
	return s.records, nil
}

// RequestRecords 返回数据库内筛选、分页与整组摘要，不向前端加载全部历史。
func (s *VelaService) RequestRecords(ctx context.Context, q records.Query) (records.Page, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.Page{}, err
	}
	return store.Query(ctx, q)
}

// RequestRecord 返回按独立保留期限读取的上下游详情。
func (s *VelaService) RequestRecord(ctx context.Context, id string) (records.Record, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.Record{}, err
	}
	r, err := store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if s.providers != nil && r.ProviderID != "" {
		names, readErr := s.providers.KeyNames(ctx, r.ProviderID, r.KeyID)
		if readErr != nil {
			return r, readErr
		}
		if name, ok := names[r.KeyID]; ok {
			r.CurrentKeyName = name
		}
	}
	return r, nil
}

// ActiveRequestRecords 只更新页面已经展示的身份，不插入其他新请求。
func (s *VelaService) ActiveRequestRecords(ctx context.Context, ids []string) ([]records.Record, error) {
	store, err := s.recordStore()
	if err != nil {
		return nil, err
	}
	return store.Active(ctx, ids)
}

// PreviewRequestCleanup 预览已结束请求清理数量与固定截止时刻。
func (s *VelaService) PreviewRequestCleanup(ctx context.Context, days int) (records.CleanupPreview, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.CleanupPreview{}, err
	}
	return store.Preview(ctx, days)
}

// CleanupRequestRecords 仅在页面明确确认预览后调用，不影响长期累计和配置。
func (s *VelaService) CleanupRequestRecords(ctx context.Context, before int64) (int64, error) {
	store, err := s.recordStore()
	if err != nil {
		return 0, err
	}
	return store.Cleanup(ctx, before)
}

// RequestTotals 查询独立于明细清理的长期日维度统计。
func (s *VelaService) RequestTotals(ctx context.Context, q records.Query) ([]records.Aggregate, error) {
	store, err := s.recordStore()
	if err != nil {
		return nil, err
	}
	return store.Totals(ctx, q)
}

// RequestKeyTotals 读取清理后仍保留的 Key 消耗归属，并通过凭据模块解析当前名称。
func (s *VelaService) RequestKeyTotals(ctx context.Context, q records.Query) ([]records.KeyAggregate, error) {
	store, err := s.recordStore()
	if err != nil {
		return nil, err
	}
	result, err := store.KeyTotals(ctx, q)
	if err != nil {
		return nil, err
	}
	names := map[string]map[string]string{}
	for i := range result {
		row := &result[i]
		if _, ok := names[row.ProviderID]; !ok {
			names[row.ProviderID] = map[string]string{}
			if s.providers != nil {
				current, e := s.providers.KeyNames(ctx, row.ProviderID, "")
				if e != nil {
					return nil, e
				}
				names[row.ProviderID] = current
			}
		}
		if name, ok := names[row.ProviderID][row.KeyID]; ok {
			row.KeyName = name
		} else {
			row.KeyName += "（已删除）"
		}
	}
	return result, nil
}

// rememberDeletedKeyNames 删除配置前固定长期统计名称；读取失败时停止删除，便于安全重试。
func (s *VelaService) rememberDeletedKeyNames(ctx context.Context, providerID, keyID string) error {
	if s.records == nil || s.providers == nil {
		return nil
	}
	names, err := s.providers.KeyNames(ctx, providerID, keyID)
	if err != nil {
		return err
	}
	return s.records.RememberKeyNames(ctx, names)
}

// RequestRetention 返回使用记录或正文保留天数。
func (s *VelaService) RequestRetention(ctx context.Context, body bool) (int, error) {
	store, err := s.recordStore()
	if err != nil {
		return 0, err
	}
	return store.Retention(ctx, body)
}

// SaveRequestRetention 保存已确认的保留期限。
func (s *VelaService) SaveRequestRetention(ctx context.Context, body bool, days int, before int64) error {
	store, err := s.recordStore()
	if err != nil {
		return err
	}
	return store.SetRetention(ctx, body, days, before)
}

// PreviewRequestDetailsCleanup 预览独立正文清理。
func (s *VelaService) PreviewRequestDetailsCleanup(ctx context.Context, days int) (records.CleanupPreview, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.CleanupPreview{}, err
	}
	return store.PreviewDetails(ctx, days)
}

// CleanupRequestDetails 删除截止时间内的正文文件。
func (s *VelaService) CleanupRequestDetails(ctx context.Context, before int64) (int64, error) {
	store, err := s.recordStore()
	if err != nil {
		return 0, err
	}
	return store.CleanupDetails(ctx, before)
}
