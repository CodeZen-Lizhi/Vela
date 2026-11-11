package app

import (
	"context"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// ProviderModels 读取供应商已关联模型及有序映射，不暴露价格和凭据。
func (s *VelaService) ProviderModels(ctx context.Context, providerID string) ([]providers.ProviderModel, error) {
	store, err := s.providerStore()
	if err != nil {
		return nil, err
	}
	return store.Models(ctx, providerID)
}

// SaveProviderModels 一次替换供应商全部模型关联，并按草稿为新增模型授权。
func (s *VelaService) SaveProviderModels(ctx context.Context, providerID string, draft providers.ProviderModelsDraft) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SaveModels(ctx, providerID, draft)
}

// SetProviderModelEnabled 即时启停一个供应商模型，保留映射及权限。
func (s *VelaService) SetProviderModelEnabled(ctx context.Context, providerID, modelID string, enabled bool) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SetModelEnabled(ctx, providerID, modelID, enabled)
}

// ProviderKeyModelPermissions 读取 Key 的全部或指定权限，明确区分指定零个。
func (s *VelaService) ProviderKeyModelPermissions(ctx context.Context, keyID string) (providers.ProviderModelPermissions, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.ProviderModelPermissions{}, err
	}
	return store.ModelPermissions(ctx, keyID)
}

// SaveProviderKeyModelPermissions 保存当前 Key 权限；界面主动取消全选需显式使用全部模式。
func (s *VelaService) SaveProviderKeyModelPermissions(ctx context.Context, keyID string, p providers.ProviderModelPermissions) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SaveModelPermissions(ctx, keyID, p)
}

// SaveProviderModelMappings 原子保存工作台提交的所有模型映射；空列表清除当前模型映射。
func (s *VelaService) SaveProviderModelMappings(ctx context.Context, providerID string, drafts []providers.ProviderModelMappingsDraft) error {
	store, err := s.providerStore()
	if err != nil {
		return err
	}
	return store.SaveMappings(ctx, providerID, drafts)
}
