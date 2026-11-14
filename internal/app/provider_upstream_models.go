package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// ProviderUpstreamModels 查询供应商真实上游候选；只读配置，强刷失败保留此前成功缓存。
func (s *VelaService) ProviderUpstreamModels(ctx context.Context, providerID string, forceRefresh bool) (providers.UpstreamModelsResult, error) {
	store, err := s.providerStore()
	if err != nil {
		return providers.UpstreamModelsResult{}, err
	}
	if s.execution == nil {
		return providers.UpstreamModelsResult{}, errors.New("上游模型查询连接尚未初始化")
	}
	return store.UpstreamModels(ctx, providerID, forceRefresh, func(ctx context.Context, p providers.Provider, k providers.ProviderKey) (*http.Client, error) {
		return s.execution.clientFor(ctx, scheduling.Candidate{Provider: p, Key: k})
	})
}
