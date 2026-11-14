package updates

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo"
)

// desktopBackend 持有 MyGo 检查结果，前端不能自行指定 URL、版本或跳过签名。
type desktopBackend struct{ release *mygo.Update }

// NewDesktop 接入 MyGo 更新器；测试版跟随后续测试版和正式版，正式版只检查正式发布。
func NewDesktop(version string) *Manager {
	mygo.Updater.SetPrereleases(strings.Contains(version, "-"))
	return New(version, &desktopBackend{})
}

// Enabled 按 MyGo 的构建信息和安装位置判断能否安全自更新。
func (b *desktopBackend) Enabled() bool { return mygo.Updater.Enabled() }

// Check 由官方更新源读取清单，MyGo 按语义版本拒绝降级。
func (b *desktopBackend) Check(ctx context.Context) (*Release, error) {
	release, err := mygo.Updater.Check(ctx)
	if err != nil {
		return nil, err
	}
	b.release = release
	if release == nil {
		return nil, nil
	}
	return &Release{Version: release.Version, Notes: release.Notes}, nil
}

// Download 复用 MyGo 的下载、签名校验、暂存与退出后替换，不直接写业务数据。
func (b *desktopBackend) Download(ctx context.Context, progress func(int64, int64)) error {
	if b.release == nil {
		return errors.New("没有已检查的更新")
	}
	return b.release.Install(ctx, progress)
}

// Restart 使用正常退出链路，MyGo 在进程退出后完成替换并启动新版。
func (b *desktopBackend) Restart() { mygo.App.Relaunch() }
