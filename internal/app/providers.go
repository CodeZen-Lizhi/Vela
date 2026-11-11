package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// providerStore 返回应用生命周期管理的供应商模块，启动失败不伪造空列表。
func (s *VelaService) providerStore() (*providers.Store, error) {
	if s.providers == nil {
		return nil, errors.New("供应商数据库尚未初始化")
	}
	return s.providers, nil
}

// Providers 一次读取供应商页需要的内容：供应商概览与表单默认值。
func (s *VelaService) Providers(ctx context.Context) (providers.ProviderSnapshot, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.ProviderSnapshot{}, err
	}
	return store.Snapshot(ctx)
}

// CreateProvider 新增供应商，默认启用；名称重复或字段不合法时不写入。
func (s *VelaService) CreateProvider(ctx context.Context, draft providers.ProviderDraft) (providers.Provider, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.Provider{}, err
	}
	return store.Create(ctx, draft)
}

// UpdateProvider 保存供应商的名称与配置，不改变启停状态。
func (s *VelaService) UpdateProvider(ctx context.Context, id string, draft providers.ProviderDraft) (providers.Provider, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.Provider{}, err
	}
	return store.Update(ctx, id, draft)
}

// SetProviderEnabled 启用或停用供应商，Key 与其他配置保留。
func (s *VelaService) SetProviderEnabled(ctx context.Context, id string, enabled bool) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SetEnabled(ctx, id, enabled)
}

// DeleteProvider 删除供应商及其全部 Key，调用方先展示确认。
func (s *VelaService) DeleteProvider(ctx context.Context, id string) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.Delete(ctx, id)
}

// ProviderKeys 读取一个供应商的上游 Key，只包含密钥掩码。
func (s *VelaService) ProviderKeys(ctx context.Context, providerID string) ([]providers.ProviderKey, error) {
	store, err := s.providerStore()
	if err != nil {
		return nil, err
	}
	return store.Keys(ctx, providerID)
}

// CreateProviderKey 为供应商新增一把上游 Key，密钥加密保存。
func (s *VelaService) CreateProviderKey(ctx context.Context, providerID string, draft providers.ProviderKeyDraft) (providers.ProviderKey, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.ProviderKey{}, err
	}
	return store.CreateKey(ctx, providerID, draft)
}

// UpdateProviderKey 保存一把上游 Key 的配置；草稿中的密钥为空时保留原密钥。
func (s *VelaService) UpdateProviderKey(ctx context.Context, keyID string, draft providers.ProviderKeyDraft) (providers.ProviderKey, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.ProviderKey{}, err
	}
	return store.UpdateKey(ctx, keyID, draft)
}

// SetProviderKeyEnabled 启用或停用一把上游 Key。
func (s *VelaService) SetProviderKeyEnabled(ctx context.Context, keyID string, enabled bool) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SetKeyEnabled(ctx, keyID, enabled)
}

// DeleteProviderKey 删除一把上游 Key 及其密钥，调用方先展示确认。
func (s *VelaService) DeleteProviderKey(ctx context.Context, keyID string) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.DeleteKey(ctx, keyID)
}

// CopyProviderKeySecret 把上游 Key 的完整密钥直接写入系统剪贴板，明文不经过页面。
func (s *VelaService) CopyProviderKeySecret(ctx context.Context, keyID string) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	secret, err := store.Secret(ctx, keyID)
	if err != nil {
		return err
	}
	return mygo.Clipboard.Write(transfer.TextData(secret))
}
