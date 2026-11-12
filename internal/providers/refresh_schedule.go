package providers

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/settings"
)

// silentRefreshKey 将后台查询的失败反馈与主动查询隔离，只在当前批次上下文生效。
type silentRefreshKey struct{}

// isSilentRefresh 判断是否保留旧的可见状态而不生成失败提示。
func isSilentRefresh(ctx context.Context) bool {
	value, _ := ctx.Value(silentRefreshKey{}).(bool)
	return value
}

// refreshFailure 后台失败保留原状态；主动查询记录可展示的本次失败原因。
func refreshFailure(ctx context.Context, previous QueryStatus, err error) QueryStatus {
	if isSilentRefresh(ctx) {
		return previous
	}
	return failedStatus(previous, err)
}

// RefreshBackground 静默查询所选范围，复用批次串行和配置世代校验；单项失败不阻断其他对象。
func (a *Accounts) RefreshBackground(ctx context.Context, balance, keys bool) error {
	_, err := a.refreshAccounts(context.WithValue(ctx, silentRefreshKey{}, true), "", balance, keys)
	return err
}

// Run 分别按余额和 Key 的分钟周期查询；设置每秒重读，查询失败不停止后续周期。
func (a *Accounts) Run(ctx context.Context, readSettings func(context.Context) (settings.AccountRefresh, error)) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	return a.run(ctx, readSettings, ticker.C)
}

// run 使用独立到期时间，修改一组间隔不会重置另一组；同拍到期合为一次批次但范围保持独立。
func (a *Accounts) run(ctx context.Context, readSettings func(context.Context) (settings.AccountRefresh, error), ticks <-chan time.Time) error {
	var balanceNext, keyNext time.Time
	var previous settings.AccountRefresh
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			config, err := readSettings(ctx)
			if err != nil || config.Validate() != nil {
				continue
			}
			balance := scheduleDue(now, config.Enabled, config.Minutes, previous.Minutes, &balanceNext)
			keys := scheduleDue(now, config.KeyEnabled, config.KeyMinutes, previous.KeyMinutes, &keyNext)
			previous = config
			if balance || keys {
				_ = a.RefreshBackground(ctx, balance, keys)
			}
		}
	}
}

// scheduleDue 关闭时清空期限，开启或间隔改变后从当前拍开始计算完整周期。
func scheduleDue(now time.Time, enabled bool, minutes, previous int, next *time.Time) bool {
	if !enabled {
		*next = time.Time{}
		return false
	}
	if next.IsZero() || minutes != previous {
		*next = now.Add(time.Duration(minutes) * time.Minute)
	}
	if now.Before(*next) {
		return false
	}
	*next = now.Add(time.Duration(minutes) * time.Minute)
	return true
}
