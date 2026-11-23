package app

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
)

// localRefresh 保存最近成功的后台结果，供桌面窗口读取。
type localRefresh struct {
	today  atomic.Pointer[records.OverviewSnapshot]
	recent atomic.Pointer[records.OverviewSnapshot]
}

// RunLocalRefresh 按设置刷新本地快照，整个桌面进程共用一份结果。
func RunLocalRefresh(ctx context.Context, s *VelaService, notify func(string)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	overviewDue := time.Now()
	var previous settings.GeneralSettings
	for {
		config, err := s.settings.General(ctx)
		if err == nil {
			now := time.Now()
			if previous.DashboardSeconds != 0 && previous.DashboardSeconds != config.DashboardSeconds {
				overviewDue = now
			}
			previous = config
			if !now.Before(overviewDue) {
				overviewDue = now.Add(time.Duration(config.DashboardSeconds) * time.Second)
				today, err := s.readOverview(ctx, "today", (*records.Store).OverviewToday)
				if err == nil {
					s.refresh.today.Store(&today)
					recent, err := s.readOverview(ctx, "recent", (*records.Store).OverviewRecent)
					if err == nil {
						s.refresh.recent.Store(&recent)
					} else {
						log.Printf("后台刷新近期统计失败: %v", err)
					}
					notify("overview")
				} else {
					log.Printf("后台刷新总览失败: %v", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
