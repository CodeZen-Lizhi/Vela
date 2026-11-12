package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/modeltest"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// NewModelTests 复用真实出站代理连接池，主动诊断不经过正式调度的启停、健康与准入。
func NewModelTests(catalog *models.Store, vendors *providers.Store, execution *SchedulingExecution) (*modeltest.Manager, error) {
	if catalog == nil || execution == nil {
		return nil, errors.New("模型测试执行依赖尚未初始化")
	}
	return modeltest.New(catalog.DB(), vendors, func(ctx context.Context, c providers.ExecutionCandidate) (*http.Client, error) {
		return execution.clientFor(ctx, scheduling.Candidate{Provider: c.Provider, Key: c.Key})
	})
}

// modelTestManager 返回宿主创建的真实联通测试入口，浏览器预览不伪造执行。
func (s *VelaService) modelTestManager() (*modeltest.Manager, error) {
	if s.modelTests == nil {
		return nil, errors.New("模型联通测试尚未初始化")
	}
	return s.modelTests, nil
}

// ProviderModelTestOptions 读取当前关联模型的诊断 Key 和按优先级排列的映射，不访问上游。
func (s *VelaService) ProviderModelTestOptions(ctx context.Context, providerID, modelID string) (modeltest.Options, error) {
	manager, err := s.modelTestManager()
	if err != nil {
		return modeltest.Options{}, err
	}
	return manager.Options(ctx, providerID, modelID)
}

// RunProviderModelTest 按用户选择执行真实联通诊断并保存脱敏结果，真实上游可能产生消耗。
func (s *VelaService) RunProviderModelTest(ctx context.Context, request modeltest.Request) (modeltest.Result, error) {
	manager, err := s.modelTestManager()
	if err != nil {
		return modeltest.Result{}, err
	}
	return manager.Run(ctx, request)
}

// CancelProviderModelTest 取消指定请求及后续候选，已发出的上游消耗不可撤回。
func (s *VelaService) CancelProviderModelTest(id string) error {
	manager, err := s.modelTestManager()
	if err != nil {
		return err
	}
	manager.Cancel(id)
	return nil
}

// ProviderModelTestResults 重读当前供应商每个已关联模型的最近真实诊断结果。
func (s *VelaService) ProviderModelTestResults(ctx context.Context, providerID string) ([]modeltest.Result, error) {
	manager, err := s.modelTestManager()
	if err != nil {
		return nil, err
	}
	return manager.Results(ctx, providerID)
}
