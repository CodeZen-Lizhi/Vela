package app

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// Overview 返回长期小时汇总，通过供应商模块解密解析当前 Key 名称，不在统计 SQL 中读取配置密文。
func (s *VelaService) Overview(ctx context.Context) (records.OverviewSnapshot, error) {
	return s.readOverview(ctx, "all", (*records.Store).Overview)
}

// OverviewToday 返回当天统计，供总览首屏独立加载。
func (s *VelaService) OverviewToday(ctx context.Context) (records.OverviewSnapshot, error) {
	return s.readOverview(ctx, "today", (*records.Store).OverviewToday)
}

// OverviewRecent 返回最近三十个统计日供快捷范围共享。
func (s *VelaService) OverviewRecent(ctx context.Context) (records.OverviewSnapshot, error) {
	return s.readOverview(ctx, "recent", (*records.Store).OverviewRecent)
}

// OverviewRange 返回自定义起止日内的统计。
func (s *VelaService) OverviewRange(ctx context.Context, start, end string) (records.OverviewSnapshot, error) {
	return s.readOverview(ctx, "range", func(store *records.Store, ctx context.Context) (records.OverviewSnapshot, error) {
		return store.OverviewRange(ctx, start, end)
	})
}

// OverviewHeatmap 读取指定年份的每日热力图汇总。
func (s *VelaService) OverviewHeatmap(ctx context.Context, year int) (records.OverviewSnapshot, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	return store.OverviewHeatmap(ctx, year)
}

// readOverview 读取指定范围并解析当前 Key 名称。
func (s *VelaService) readOverview(ctx context.Context, scope string, read func(*records.Store, context.Context) (records.OverviewSnapshot, error)) (records.OverviewSnapshot, error) {
	store, err := s.recordStore()
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	snapshot, err := read(store, ctx)
	if err != nil {
		return records.OverviewSnapshot{}, err
	}
	if s.providers == nil {
		return snapshot, nil
	}
	started := time.Now()
	defer func() {
		log.Printf("[PERF][overview] scope=%s stage=key_names ms=%.2f", scope, float64(time.Since(started).Microseconds())/1000)
	}()
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

// LogOverviewFrame 将 WebView 的帧耗时写入桌面诊断日志，限制事件名称和数值。
func (s *VelaService) LogOverviewFrame(scope string, millis float64) {
	switch scope {
	case "today", "recent", "all", "range", "heatmap":
		if !math.IsNaN(millis) && millis >= 0 && millis <= 3600000 {
			log.Printf("[PERF][overview] scope=%s stage=frame ms=%.2f", scope, millis)
		}
	}
}
