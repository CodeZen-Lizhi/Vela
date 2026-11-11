package app

import (
	"context"
	"errors"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"sort"

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

// SchedulingSnapshot 是调度页的同一次读取结果，供应商包含停用项，按已保存顺序排列。
type SchedulingSnapshot struct {
	// Policy 包含策略、顺序及修订号。
	Policy scheduling.SchedulingPolicy `json:"policy"`
	// Providers 只提供名称、健康与启停等已有展示字段，不包含明文密钥。
	Providers []providers.ProviderOverview `json:"providers"`
}

// SchedulingSnapshot 读取真实供应商并按全局调度顺序排列，不以空数组掩盖加载失败。
func (s *VelaService) SchedulingSnapshot(ctx context.Context) (SchedulingSnapshot, error) {
	policy, err := s.SchedulingPolicy(ctx)
	if err != nil {
		return SchedulingSnapshot{}, err
	}
	snapshot, err := s.Providers(ctx)
	if err != nil {
		return SchedulingSnapshot{}, err
	}
	positions := map[string]int{}
	for i, id := range policy.ProviderOrder {
		positions[id] = i
	}
	sort.SliceStable(snapshot.Providers, func(i, j int) bool {
		left,ok:=positions[snapshot.Providers[i].Provider.ID];if !ok{left=len(positions)}
 right,ok:=positions[snapshot.Providers[j].Provider.ID];if !ok{right=len(positions)}
 return left<right
	})
	return SchedulingSnapshot{Policy: policy, Providers: snapshot.Providers}, nil
}

// SaveSchedulingProviderOrder 原子保存完整供应商顺序，失败不改变已保存配置。
func (s *VelaService) SaveSchedulingProviderOrder(ctx context.Context, ids []string) error {
	store, err := s.schedulingStore()
	if err != nil {
		return err
	}
	return store.SaveProviderOrder(ctx, ids)
}
