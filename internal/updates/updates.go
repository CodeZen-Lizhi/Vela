// Package updates 管理用户主动触发的应用更新，下载校验与替换交给桌面更新器。
package updates

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Status 是界面可读取的更新状态；下载失败保留已检查的新版本，允许重试。
type Status struct {
	// Enabled 表示当前安装位置和构建支持自动更新。
	Enabled bool `json:"enabled"`
	// CurrentVersion 是正在运行的版本，Version 是已检查到的新版本。
	CurrentVersion string `json:"currentVersion"`
	Version        string `json:"version"`
	// Notes 是纯文本显示的发行说明，不作为 HTML 执行。
	Notes string `json:"notes"`
	// Phase 为 idle、checking、latest、available、downloading、ready 或 restarting。
	Phase string `json:"phase"`
	// Downloaded 和 Total 是更新器报告的字节数，Total 为零时进度未知。
	Downloaded int64 `json:"downloaded"`
	Total      int64 `json:"total"`
	// Error 保留本次失败原因，不能用“最新版本”冒充检查失败。
	Error string `json:"error"`
}

// Release 只携带已检查到的版本和说明；实际待下载目标由更新器持有。
type Release struct {
	// Version 和 Notes 来自更新清单。
	Version string
	Notes   string
}

// Backend 隔离系统更新器，所有安装目标、签名校验与进程替换均由它负责。
type Backend interface {
	// Enabled 判断当前构建和安装位置能否更新。
	Enabled() bool
	// Check 读取固定更新源并保存已检查目标，nil 表示没有更新。
	Check(context.Context) (*Release, error)
	// Download 下载已检查版本，回报字节进度并验证签名。
	Download(context.Context, func(int64, int64)) error
	// Restart 按正常退出链路重新启动已校验的版本。
	Restart()
}

// Manager 串行管理检查和下载，关闭时取消并等待任务，不因切换设置分类中断下载。
type Manager struct {
	// mu 保护状态和 closed；work/cancel 管理所有异步任务的退出。
	mu      sync.Mutex
	state   Status
	backend Backend
	ctx     context.Context
	cancel  context.CancelFunc
	work    sync.WaitGroup
	closed  bool
}

// New 创建只接受用户主动操作的更新管理器，不启动定时检查或静默下载。
func New(version string, backend Backend) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{state: Status{CurrentVersion: version, Phase: "idle"}, backend: backend, ctx: ctx, cancel: cancel}
}

// Status 返回当前一致快照，并核对系统是否允许更新此安装位置。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	state.Enabled = m.backend.Enabled()
	return state
}

// Check 在空闲状态检查新版本；重复请求不会开启并行检查或覆盖已下载版本。
func (m *Manager) Check() Status {
	m.mu.Lock()
	if m.closed || m.state.Phase == "checking" || m.state.Phase == "downloading" || m.state.Phase == "ready" || m.state.Phase == "restarting" {
		m.mu.Unlock()
		return m.Status()
	}
	if !m.backend.Enabled() {
		m.state.Error = "当前为开发版、只读位置或安装目录不可写，请使用 DMG 安装到应用程序后再更新。"
		m.mu.Unlock()
		return m.Status()
	}
	m.state.Phase = "checking"
	m.state.Error = ""
	m.work.Add(1)
	m.mu.Unlock()
	defer m.work.Done()
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	release, err := m.backend.Check(ctx)
	m.mu.Lock()
	m.state.Phase = "latest"
	m.state.Version = ""
	m.state.Notes = ""
	if err != nil {
		m.state.Phase = "idle"
		m.state.Error = err.Error()
	} else if release != nil {
		m.state.Phase = "available"
		m.state.Version = release.Version
		m.state.Notes = release.Notes
	}
	m.mu.Unlock()
	return m.Status()
}

// Download 启动已检查版本的下载；成功后仅进入 ready，必须再次明确确认才重启。
func (m *Manager) Download() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.state.Phase != "available" {
		return errors.New("请先检查可用更新，或等待当前下载完成")
	}
	m.state.Phase = "downloading"
	m.state.Error = ""
	m.state.Downloaded = 0
	m.state.Total = 0
	m.work.Add(1)
	go func() {
		defer m.work.Done()
		err := m.backend.Download(m.ctx, func(done, total int64) { m.mu.Lock(); m.state.Downloaded = done; m.state.Total = total; m.mu.Unlock() })
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			m.state.Phase = "available"
			m.state.Error = err.Error()
		} else {
			m.state.Phase = "ready"
		}
	}()
	return nil
}

// Restart 仅在下载校验成功后触发一次正常重启，沿用桌面退出时的网关及数据库收尾。
func (m *Manager) Restart() error {
	m.mu.Lock()
	if m.closed || m.state.Phase != "ready" {
		m.mu.Unlock()
		return errors.New("更新尚未下载并校验完成")
	}
	m.state.Phase = "restarting"
	m.mu.Unlock()
	m.backend.Restart()
	return nil
}

// Close 拒绝新任务，取消在途下载并等待退出，之后桌面宿主才关闭其他资源。
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.cancel(); m.mu.Unlock(); m.work.Wait() }
