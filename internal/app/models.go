package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// modelStore 返回应用生命周期管理的模型模块，启动失败不伪造空列表。
func (s *VelaService) modelStore() (*models.Store, error) {
	if s.models == nil {
		return nil, errors.New("模型数据库尚未初始化")
	}
	return s.models, nil
}

// ListModels 读取本地模型完整配置。
func (s *VelaService) ListModels(ctx context.Context) ([]models.Model, error) {
	store, err := s.modelStore()
	if err != nil {
		return nil, err
	}
	return store.List(ctx)
}

// ModelCatalog 加载真实目录，价格缺失保持未知。
func (s *VelaService) ModelCatalog(ctx context.Context) ([]models.CatalogEntry, error) {
	store, err := s.modelStore()
	if err != nil {
		return nil, err
	}
	return store.Catalog(ctx)
}

// AddModel 根据来源身份添加模型，前端不能注入来源价格。
func (s *VelaService) AddModel(ctx context.Context, providerID, modelID string) (models.Model, error) {
	store, err := s.modelStore()
	if err != nil {
		return models.Model{}, err
	}
	return store.Add(ctx, providerID, modelID)
}

// UpdateModel 保存名称与完整价格草稿，并保持来源修改保护。
func (s *VelaService) UpdateModel(ctx context.Context, edit models.Edit) (models.Model, error) {
	store, err := s.modelStore()
	if err != nil {
		return models.Model{}, err
	}
	return store.Update(ctx, edit)
}

// DeleteModel 删除统一模型及其供应商关联、映射和 Key 模型权限，调用方先展示确认。
func (s *VelaService) DeleteModel(ctx context.Context, id string) error {
	store, err := s.modelStore()
	if err != nil {
		return err
	}
	return store.Delete(ctx, id)
}

// SyncModelPrices 更新全部已添加模型并返回各模型失败原因。
func (s *VelaService) SyncModelPrices(ctx context.Context) (models.SyncResult, error) {
	store, err := s.modelStore()
	if err != nil {
		return models.SyncResult{}, err
	}
	return store.Sync(ctx)
}

// GlobalProxy 读取唯一的全局网络设置。
func (s *VelaService) GlobalProxy(ctx context.Context) (models.Proxy, error) {
	store, err := s.modelStore()
	if err != nil {
		return models.Proxy{}, err
	}
	return store.GetProxy(ctx)
}

// SaveGlobalProxy 保存全局代理，目录读取立即遵循此配置。
func (s *VelaService) SaveGlobalProxy(ctx context.Context, p models.Proxy) error {
	store, err := s.modelStore()
	if err != nil {
		return err
	}
	return store.SetProxy(ctx, p)
}

// CreateModel 原子保存目录确认草稿或无来源的手动模型。
func (s *VelaService) CreateModel(ctx context.Context, draft models.Create) (models.Model, error) {
	store, err := s.modelStore()
	if err != nil {
		return models.Model{}, err
	}
	return store.Create(ctx, draft)
}
