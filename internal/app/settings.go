package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// SettingsSnapshot 是设置页一次读取的已保存设置；访问密钥内容不随快照下发。
type SettingsSnapshot struct {
	// General 是常规设置。
	General settings.GeneralSettings `json:"general"`
	// OpenAtLogin 是系统当前是否在登录时启动应用，以系统登录项为准。
	OpenAtLogin bool `json:"openAtLogin"`
	// Gateway 是网关监听设置。
	Gateway settings.GatewaySettings `json:"gateway"`
	// Account 是全局账户刷新设置。
	Account settings.AccountRefresh `json:"account"`
	// Attention 是总览提醒偏好。
	Attention settings.AttentionSettings `json:"attention"`
}

// settingsStore 返回应用生命周期管理的设置模块，启动失败不伪造默认设置。
func (s *VelaService) settingsStore() (*settings.Store, error) {
	if s.settings == nil {
		return nil, errors.New("设置数据库尚未初始化")
	}
	return s.settings, nil
}

// Settings 一次读取设置页需要的已保存设置。
func (s *VelaService) Settings(ctx context.Context) (SettingsSnapshot, error) {
	var snapshot SettingsSnapshot
	store, err := s.settingsStore()
	if err != nil {
		return snapshot, err
	}
	if snapshot.General, err = store.General(ctx); err != nil {
		return snapshot, err
	}
	if snapshot.Gateway, err = store.Gateway(ctx); err != nil {
		return snapshot, err
	}
	if snapshot.Account, err = store.AccountRefresh(ctx); err != nil {
		return snapshot, err
	}
	if snapshot.Attention, err = store.Attention(ctx); err != nil {
		return snapshot, err
	}
	snapshot.OpenAtLogin = mygo.App.OpenAtLogin()
	return snapshot, nil
}

// SaveGeneralSettings 保存常规设置，并让外观、关闭行为与后台入口立即生效。
func (s *VelaService) SaveGeneralSettings(ctx context.Context, value settings.GeneralSettings) error {
	store, err := s.settingsStore()
	if err != nil {
		return err
	}
	if err = store.SaveGeneral(ctx, value); err != nil {
		return err
	}
	if s.applyGeneral != nil {
		s.applyGeneral(value)
	}
	return nil
}

// SetOpenAtLogin 向系统登记或取消开机启动，返回系统的实际状态；失败时系统设置保持原样。
func (s *VelaService) SetOpenAtLogin(enabled bool) (bool, error) {
	if err := mygo.App.SetOpenAtLogin(enabled); err != nil {
		return mygo.App.OpenAtLogin(), fmt.Errorf("开机启动设置未生效: %w", err)
	}
	return mygo.App.OpenAtLogin(), nil
}

// SaveGatewaySettings 保存网关监听端口；网关接入后按此端口监听。
func (s *VelaService) SaveGatewaySettings(ctx context.Context, value settings.GatewaySettings) error {
	store, err := s.settingsStore()
	if err != nil {
		return err
	}
	return store.SaveGateway(ctx, value)
}

// SaveAccountRefresh 保存全局账户刷新设置。
func (s *VelaService) SaveAccountRefresh(ctx context.Context, value settings.AccountRefresh) error {
	store, err := s.settingsStore()
	if err != nil {
		return err
	}
	return store.SaveAccountRefresh(ctx, value)
}

// SaveAttentionSettings 保存总览提醒偏好。
func (s *VelaService) SaveAttentionSettings(ctx context.Context, value settings.AttentionSettings) error {
	store, err := s.settingsStore()
	if err != nil {
		return err
	}
	return store.SaveAttention(ctx, value)
}

// accessKey 从系统凭据存储读取网关访问密钥；尚未设置时返回空字符串。
func (s *VelaService) accessKey() (string, error) {
	if s.accessKeyAccount == "" {
		return "", errors.New("访问密钥存储尚未初始化")
	}
	value, err := mygo.Secrets.Get(s.accessKeyAccount)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("访问密钥读取失败，已保存的密钥未被改动: %w", err)
	}
	return string(value), nil
}

// HasAccessKey 报告是否已设置网关访问密钥，不返回密钥内容。
func (s *VelaService) HasAccessKey() (bool, error) {
	value, err := s.accessKey()
	return value != "", err
}

// RevealAccessKey 在用户主动查看时返回访问密钥。
func (s *VelaService) RevealAccessKey() (string, error) {
	value, err := s.accessKey()
	if err == nil && value == "" {
		return "", errors.New("尚未设置访问密钥")
	}
	return value, err
}

// CopyAccessKey 将访问密钥直接写入系统剪贴板，密钥不经过页面。
func (s *VelaService) CopyAccessKey() error {
	value, err := s.RevealAccessKey()
	if err != nil {
		return err
	}
	return mygo.Clipboard.Write(transfer.TextData(value))
}

// SaveAccessKey 校验后把访问密钥写入系统凭据存储；失败时原密钥保持不变。
func (s *VelaService) SaveAccessKey(value string) error {
	if s.accessKeyAccount == "" {
		return errors.New("访问密钥存储尚未初始化")
	}
	if err := settings.ValidateAccessKey(value); err != nil {
		return err
	}
	if err := mygo.Secrets.Set(s.accessKeyAccount, []byte(value)); err != nil {
		return fmt.Errorf("访问密钥保存失败，原密钥保持不变: %w", err)
	}
	return nil
}
