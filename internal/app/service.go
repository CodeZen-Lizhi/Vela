// Package app 提供暴露给 Vela 前端的类型化应用服务。
package app

import (
	"runtime"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// VelaService 提供运行信息、模型管理、供应商管理、调度策略和全局设置操作，由 MyGo 生成 TypeScript 客户端。
type VelaService struct {
	// startedAt、version 是运行信息；models、providers、settings、scheduling 由桌面入口创建，共用的数据库在退出时关闭。
	startedAt  time.Time
	version    string
	models     *models.Store
	providers  *providers.Store
	settings   *settings.Store
	scheduling *scheduling.Store
	// credentials 保存本机加密凭据；applyGeneral 让常规设置在桌面宿主生效。
	credentials  *credentials.Store
	applyGeneral func(settings.GeneralSettings)
}

// Options 汇集桌面入口交给应用服务的依赖；生成绑定时只需要版本。
type Options struct {
	// Version 是应用版本。
	Version string
	// Models 是已打开的模型模块。
	Models *models.Store
	// Providers 是与模型模块共用数据库的供应商模块。
	Providers *providers.Store
	// Settings 是与模型模块共用数据库的全局设置模块。
	Settings *settings.Store
	// Scheduling 是与模型模块共用数据库的调度策略模块。
	Scheduling *scheduling.Store
	// Credentials 是本机加密凭据存储，由桌面入口负责初始化和旧凭据迁移。
	Credentials *credentials.Store
	// ApplyGeneral 让保存后的常规设置在窗口、菜单栏与 Dock 立即生效。
	ApplyGeneral func(settings.GeneralSettings)
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
		startedAt:    time.Now(),
		version:      o.Version,
		models:       o.Models,
		providers:    o.Providers,
		settings:     o.Settings,
		scheduling:   o.Scheduling,
		credentials:  o.Credentials,
		applyGeneral: o.ApplyGeneral,
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
