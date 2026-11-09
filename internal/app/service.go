// Package app 提供暴露给 Vela 前端的类型化应用服务。
package app

import (
	"runtime"
	"time"
)

// VelaService 是当前最小应用服务，由 MyGo 生成 TypeScript 客户端。
type VelaService struct {
	startedAt time.Time
	version   string
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

// NewVelaService 创建最小应用服务。
func NewVelaService(version string) *VelaService {
	return &VelaService{
		startedAt: time.Now(),
		version:   version,
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
