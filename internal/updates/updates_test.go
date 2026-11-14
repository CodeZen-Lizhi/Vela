package updates

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBackend 隔离不可控网络与系统重启；状态机仍执行真实管理器。
type fakeBackend struct {
	release       *Release
	checkError    error
	downloadError error
	started       chan struct{}
	finish        chan struct{}
	checks        atomic.Int32
	downloads     atomic.Int32
	restarts      atomic.Int32
}

// Enabled 测试宿主具有可写安装位置。
func (b *fakeBackend) Enabled() bool { return true }

// Check 返回受控发布结果，统计是否重复查询。
func (b *fakeBackend) Check(context.Context) (*Release, error) {
	b.checks.Add(1)
	return b.release, b.checkError
}

// Download 阻塞到完成或取消，报告真实状态机可见的进度。
func (b *fakeBackend) Download(ctx context.Context, progress func(int64, int64)) error {
	b.downloads.Add(1)
	progress(50, 100)
	close(b.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.finish:
		return b.downloadError
	}
}

// Restart 记录实际进入重启契约的次数，不结束测试进程。
func (b *fakeBackend) Restart() { b.restarts.Add(1) }

// waitPhase 在有界时间内观察异步状态，不复制业务逻辑。
func waitPhase(t *testing.T, m *Manager, phase string) Status {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s := m.Status()
		if s.Phase == phase {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未进入 %s: %+v", phase, m.Status())
	return Status{}
}

// TestDownloadRequiresExplicitRestart 验证重复操作不并发、进度保留、下载后不自动重启。
func TestDownloadRequiresExplicitRestart(t *testing.T) {
	b := &fakeBackend{release: &Release{Version: "2.0.0", Notes: "notes"}, started: make(chan struct{}), finish: make(chan struct{})}
	m := New("1.0.0", b)
	defer m.Close()
	if m.Restart() == nil || m.Download() == nil {
		t.Fatal("未检查更新不能下载或重启")
	}
	if s := m.Check(); s.Phase != "available" {
		t.Fatal(s)
	}
	if err := m.Download(); err != nil {
		t.Fatal(err)
	}
	<-b.started
	if m.Download() == nil {
		t.Fatal("不能重复下载")
	}
	m.Check()
	if s := m.Status(); s.Downloaded != 50 || s.Total != 100 {
		t.Fatal(s)
	}
	close(b.finish)
	waitPhase(t, m, "ready")
	m.Check()
	if b.restarts.Load() != 0 || b.checks.Load() != 1 {
		t.Fatal("完成前自动重启或检查覆盖")
	}
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	if m.Restart() == nil || b.restarts.Load() != 1 {
		t.Fatal("重复重启")
	}
}

// TestErrorsAndShutdown 验证检查失败不冒充最新、下载失败可重试、关闭取消在途任务。
func TestErrorsAndShutdown(t *testing.T) {
	b := &fakeBackend{checkError: errors.New("offline"), started: make(chan struct{}), finish: make(chan struct{})}
	m := New("1.0.0", b)
	if s := m.Check(); s.Phase == "latest" || s.Error != "offline" {
		t.Fatal(s)
	}
	b.checkError = nil
	b.release = &Release{Version: "2.0.0"}
	b.downloadError = errors.New("invalid signature")
	m.Check()
	if err := m.Download(); err != nil {
		t.Fatal(err)
	}
	<-b.started
	close(b.finish)
	if s := waitPhase(t, m, "available"); s.Error != "invalid signature" || s.Version != "2.0.0" {
		t.Fatal(s)
	}
	b.started = make(chan struct{})
	b.finish = make(chan struct{})
	b.downloadError = nil
	if err := m.Download(); err != nil {
		t.Fatal(err)
	}
	<-b.started
	m.Close()
	if m.Download() == nil || m.Restart() == nil {
		t.Fatal("关闭后仍可操作")
	}
}

// TestNoNewRelease 只有成功检查且没有更新时才展示 latest。
func TestNoNewRelease(t *testing.T) {
	m := New("1.0.0", &fakeBackend{})
	defer m.Close()
	if state := m.Check(); state.Phase != "latest" || state.Error != "" || state.Version != "" {
		t.Fatal(state)
	}
}
