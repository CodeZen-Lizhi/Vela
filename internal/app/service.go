// Package app 提供暴露给 Vela 前端的类型化应用服务。
package app

import (
	"context"
	"runtime"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/modeltest"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/CodeZen-Lizhi/vela/internal/updates"
	"github.com/CodeZen-Lizhi/vela/internal/verification"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// VelaService 提供运行信息、模型管理、供应商管理、调度策略和全局设置操作，由 MyGo 生成 TypeScript 客户端。
type VelaService struct {
	// refresh 保存桌面后台本地查询结果。
	refresh localRefresh
	// updates 管理主动检查、下载及用户确认重启，生命周期由宿主持有。
	updates *updates.Manager
	// records 管理独立请求历史与清理，生命周期由桌面宿主持有。
	records *records.Store
	// startedAt、version 是运行信息；models、providers、settings、scheduling 由桌面入口创建，共用的数据库在退出时关闭。
	startedAt time.Time
	version   string
	models    *models.Store
	providers *providers.Store
	// accounts 管理供应商余额与逐 Key 上游信息查询。
	accounts   *providers.Accounts
	settings   *settings.Store
	scheduling *scheduling.Store
	// execution 管理运行中的 Key 健康状态与持久化，不向模板代码暴露。
	execution *SchedulingExecution
	// modelTests 管理供应商模型主动联通诊断及最近结果。
	modelTests *modeltest.Manager
	// verification 管理独立固定目标的指纹检测，不参与调度健康。
	verification *verification.Manager
	// credentials 是本机加密凭据存储；applyGeneral、applyGateway 让设置在桌面宿主立即生效。
	credentials  *credentials.Store
	applyGeneral func(settings.GeneralSettings)
	applyGateway func(context.Context, settings.GatewaySettings) error
}

// Options 汇集桌面入口交给应用服务的依赖；生成绑定时只需要版本。
type Options struct {
	// Updates 是宿主创建的应用更新管理器，生成绑定时可为空。
	Updates *updates.Manager
	// Records 是实际执行入口共用的请求记录模块。
	Records *records.Store
	// Version 是应用版本。
	Version string
	// Models 是已打开的模型模块。
	Models *models.Store
	// Providers 是与模型模块共用数据库的供应商模块。
	Providers *providers.Store
	// Accounts 是与供应商共用数据库与凭据库的查询管理器。
	Accounts *providers.Accounts
	// Settings 是与模型模块共用数据库的全局设置模块。
	Settings *settings.Store
	// Scheduling 是与模型模块共用数据库的调度策略模块。
	Scheduling *scheduling.Store
	// Execution 是已恢复健康状态的调度执行器，手动恢复 Key 健康必须复用同一运行核心。
	Execution *SchedulingExecution
	// ModelTests 是复用真实请求出口的模型主动诊断模块。
	ModelTests *modeltest.Manager
	// Verification 是与供应商共用数据库的模型验证管理器。
	Verification *verification.Manager
	// Credentials 是桌面入口初始化的本机加密凭据存储。
	Credentials *credentials.Store
	// ApplyGeneral 让保存后的常规设置在窗口、菜单栏与 Dock 立即生效。
	ApplyGeneral func(settings.GeneralSettings)
	// ApplyGateway 让保存后的监听端口与访问密钥立即作用于本机网关。
	ApplyGateway func(context.Context, settings.GatewaySettings) error
}

// RuntimeInfo 是前端可读取的运行信息投影。
type RuntimeInfo struct {
	// Ready 表示桌面应用服务已经可以响应前端调用。
	Ready bool `json:"ready"`
	// Version 是应用版本。
	Version string `json:"version"`
	// Platform 是 Go 运行平台。
	Platform string `json:"platform"`
	// Architecture 是 Go 运行架构。
	Architecture string `json:"architecture"`
	// StartedAt 是应用服务创建时间，使用 RFC 3339 格式。
	StartedAt string `json:"startedAt"`
}

// NewVelaService 创建应用服务并接收入口已打开的模块。
func NewVelaService(o Options) *VelaService {
	return &VelaService{
		updates:      o.Updates,
		records:      o.Records,
		startedAt:    time.Now(),
		version:      o.Version,
		models:       o.Models,
		providers:    o.Providers,
		accounts:     o.Accounts,
		settings:     o.Settings,
		scheduling:   o.Scheduling,
		execution:    o.Execution,
		verification: o.Verification,
		modelTests:   o.ModelTests,
		credentials:  o.Credentials,
		applyGeneral: o.ApplyGeneral,
		applyGateway: o.ApplyGateway,
	}
}

// RuntimeInfo 返回当前应用的基础运行信息。
func (s *VelaService) RuntimeInfo() RuntimeInfo {
	return RuntimeInfo{
		Ready:        true,
		Version:      s.version,
		Platform:     runtime.GOOS,
		Architecture: runtime.GOARCH,
		StartedAt:    s.startedAt.Format(time.RFC3339),
	}
}

// CopyText 将前端选中的文本写入系统剪贴板，写入失败返回错误。
func (s *VelaService) CopyText(text string) error {
	return mygo.Clipboard.Write(transfer.TextData(text))
}
