// Package settings 维护不依赖桌面宿主的全局设置：默认值、校验与 SQLite 持久化。
package settings

import (
	"errors"
	"math"
	"regexp"
)

// GeneralSettings 是常规设置：外观、关闭窗口后的行为、后台入口与页面刷新间隔。
type GeneralSettings struct {
	// Appearance 界面外观，取 light 或 dark。
	Appearance string `json:"appearance"`
	// CloseAction 关闭主窗口后的行为：background 继续运行，quit 退出应用。
	CloseAction string `json:"closeAction"`
	// BackgroundEntry 后台运行时保留的系统入口：menu、dock 或 both。
	BackgroundEntry string `json:"backgroundEntry"`
	// DashboardSeconds 总览刷新间隔，单位秒。
	DashboardSeconds int `json:"dashboardSeconds"`
	// ProviderSeconds 供应商列表刷新间隔，单位秒。
	ProviderSeconds int `json:"providerSeconds"`
}

// GatewaySettings 是本机网关的监听设置；监听地址固定为 127.0.0.1。
type GatewaySettings struct {
	// Port 监听的 TCP 端口，范围 1024～65535。
	Port int `json:"port"`
}

// AccountRefresh 是所有供应商账户共用的定时刷新设置。
type AccountRefresh struct {
	// Enabled 是否定时刷新账户信息，默认关闭。
	Enabled bool `json:"enabled"`
	// Minutes 刷新间隔，单位分钟，至少为 1。
	Minutes int `json:"minutes"`
}

// AttentionSettings 是总览提醒的展示偏好，只控制展示，不启动采集或验证任务。
type AttentionSettings struct {
	// Balance 是否展示供应商余额不足。
	Balance bool `json:"balance"`
	// Difference 是否展示模型验证差异。
	Difference bool `json:"difference"`
	// Health 是否展示供应商健康异常。
	Health bool `json:"health"`
	// Rate 是否展示 Key 倍率变化。
	Rate bool `json:"rate"`
	// BalanceThreshold 余额低于该美元金额时提醒，零是有效阈值。
	BalanceThreshold float64 `json:"balanceThreshold"`
}

// DefaultGeneral 返回常规设置默认值：浅色、关闭窗口继续运行、仅保留菜单栏入口。
func DefaultGeneral() GeneralSettings {
	return GeneralSettings{Appearance: "light", CloseAction: "background", BackgroundEntry: "menu", DashboardSeconds: 10, ProviderSeconds: 60}
}

// DefaultGateway 返回默认监听端口。
func DefaultGateway() GatewaySettings { return GatewaySettings{Port: 18080} }

// DefaultAccountRefresh 返回默认关闭、间隔 5 分钟的账户刷新设置。
func DefaultAccountRefresh() AccountRefresh { return AccountRefresh{Enabled: false, Minutes: 5} }

// DefaultAttention 返回四项提醒全部开启、余额阈值 10 美元的默认偏好。
func DefaultAttention() AttentionSettings {
	return AttentionSettings{Balance: true, Difference: true, Health: true, Rate: true, BalanceThreshold: 10}
}

// Validate 只接受界面提供的取值，拒绝后保留原设置。
func (g GeneralSettings) Validate() error {
	if g.Appearance != "light" && g.Appearance != "dark" {
		return errors.New("外观只能是浅色或深色")
	}
	if g.CloseAction != "background" && g.CloseAction != "quit" {
		return errors.New("请选择关闭窗口后继续运行或退出应用")
	}
	if g.BackgroundEntry != "menu" && g.BackgroundEntry != "dock" && g.BackgroundEntry != "both" {
		return errors.New("请选择后台运行时显示的入口")
	}
	if g.DashboardSeconds != 10 && g.DashboardSeconds != 30 && g.DashboardSeconds != 60 {
		return errors.New("总览刷新间隔只能是 10、30 或 60 秒")
	}
	if g.ProviderSeconds != 60 && g.ProviderSeconds != 120 {
		return errors.New("供应商列表刷新间隔只能是 60 或 120 秒")
	}
	return nil
}

// Validate 限制端口在非特权范围内。
func (g GatewaySettings) Validate() error {
	if g.Port < 1024 || g.Port > 65535 {
		return errors.New("请输入 1024–65535 之间的整数端口")
	}
	return nil
}

// Validate 要求间隔至少 1 分钟；关闭时仍保留间隔。
func (a AccountRefresh) Validate() error {
	if a.Minutes < 1 {
		return errors.New("请输入至少 1 分钟的整数刷新间隔")
	}
	return nil
}

// Validate 拒绝负数与非有限阈值。
func (a AttentionSettings) Validate() error {
	if math.IsNaN(a.BalanceThreshold) || math.IsInf(a.BalanceThreshold, 0) || a.BalanceThreshold < 0 {
		return errors.New("请填写不小于 0 的余额阈值")
	}
	return nil
}

// accessKeyPattern 限定访问密钥为 1～256 个可见 ASCII 字符。
var accessKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)

// ValidateAccessKey 校验网关访问密钥的字符与长度；密钥本身由系统凭据存储保存。
func ValidateAccessKey(value string) error {
	if !accessKeyPattern.MatchString(value) {
		return errors.New("密钥须为 1–256 个英文字符、数字或符号，不能包含空格")
	}
	return nil
}
