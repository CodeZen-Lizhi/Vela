package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// schedulingStore 返回应用生命周期管理的调度模块，启动失败不伪造默认策略。
func (s *VelaService) schedulingStore() (*scheduling.Store, error) {
	if s.scheduling == nil {
		return nil, errors.New("调度数据库尚未初始化")
	}
	return s.scheduling, nil
}

// SchedulingPolicy 读取调度页需要的已保存调度配置；尚未保存时为默认的缓存亲和。
func (s *VelaService) SchedulingPolicy(ctx context.Context) (scheduling.SchedulingPolicy, error) {
	store, err := s.schedulingStore()
	if err != nil {
		return scheduling.SchedulingPolicy{}, err
	}
	return store.Policy(ctx)
}

// SaveSchedulingStrategy 保存全局调度策略，取值为 cache_affinity、fixed_order 或 cost_based；失败时原策略保持不变。
func (s *VelaService) SaveSchedulingStrategy(ctx context.Context, strategy scheduling.SchedulingStrategy) error {
	store, err := s.schedulingStore()
	if err != nil {
		return err
	}
	return store.SaveStrategy(ctx, strategy)
}
