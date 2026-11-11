package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/outbound"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// schedulingSource 将供应商显式模型关系和权限接入调度，明文凭据不进入MyGo绑定。
type schedulingSource struct {
	// providers 管理加密凭据和实际出站前的配置复核。
	providers *providers.Store
	// mu与order使配置快照按读取顺序获得单调版本，迟到旧候选不得回滚新凭据。
	mu    sync.Mutex
	order uint64
}

// Candidates 返回当前可提供模型的配置，不把空关联列表当成所有模型均可调用。
func (s *schedulingSource) Candidates(ctx context.Context, req scheduling.ExecutionRequest) ([]scheduling.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	routes, err := s.providers.ExecutionCandidates(ctx, req.Request.Model, requestOperation(req))
	s.order++
	if err != nil {
		return nil, err
	}
	result := make([]scheduling.Candidate, 0, len(routes))
	for _, route := range routes {
		result = append(result, scheduling.Candidate{Provider: route.Provider, Key: route.Key, Model: route.Model, CredentialVersion: route.Version, ConfigVersion: s.order})
	}
	return result, nil
}

// Credential 在每次真实出站前复核模型、启停、Key权限和凭据版本。
func (s *schedulingSource) Credential(ctx context.Context, c scheduling.Candidate, req scheduling.ExecutionRequest) (string, error) {
	return s.providers.ExecutionSecret(ctx, req.Request.Model, c.Key.ID, c.CredentialVersion, c.Model, requestOperation(req))
}

// SchedulingExecution 是宿主或未来HTTP网关持有的调度生命周期，不暴露到前端。
type SchedulingExecution struct {
	// Executor 是已连接持久化、真实供应商和HTTP执行的调度入口。
	Executor *scheduling.Executor
	// mu 保护按出口地址复用的客户端集合；关闭应在宿主请求结束后执行。
	mu      sync.Mutex
	clients map[string]*http.Client
	// models 提供唯一全局代理，不从环境变量读取代理。
	models *models.Store
}

// NewSchedulingExecution 组装调度并恢复健康；异常时拒绝启动执行，不伪造满分。
func NewSchedulingExecution(ctx context.Context, policy *scheduling.Store, vendors *providers.Store, catalog *models.Store) (*SchedulingExecution, error) {
	if policy == nil || vendors == nil || catalog == nil {
		return nil, errors.New("调度模块依赖尚未初始化")
	}
	runtime := scheduling.NewRuntime()
	if err := policy.RestoreRuntime(ctx, runtime); err != nil {
		return nil, err
	}
	vendors.SetHealthLookup(func(id, version string) (float64, bool) {
		health := runtime.Health(id, version)
		return health.Score, health.Score > 0 && !health.CircuitOpen && !health.CooldownUntil.After(time.Now())
	})
	value := &SchedulingExecution{models: catalog, clients: map[string]*http.Client{}}
	runner := scheduling.NewHTTPRunner()
	runner.ClientFor = value.clientFor
	value.Executor = &scheduling.Executor{Policies: policy, Runtime: runtime, Source: &schedulingSource{providers: vendors}, Runner: runner, Persist: func(ctx context.Context) error { return policy.SaveRuntime(ctx, runtime) }}
	return value, nil
}

// clientFor 按Key→供应商→全局代理解析出口，客户端按地址复用，重定向不携带上游凭据。
func (s *SchedulingExecution) clientFor(ctx context.Context, c scheduling.Candidate) (*http.Client, error) {
	choice := c.Key.Proxy
	if choice.Mode == providers.ProxyProvider || choice.Mode == "" {
		choice = c.Provider.Request.Proxy
	}
	var proxy *url.URL
	if choice.Mode == providers.ProxyGlobal {
		global, err := s.models.GetProxy(ctx)
		if err != nil {
			return nil, err
		}
		if global.Enabled {
			proxy, err = outbound.ParseProxy(global.URL)
			if err != nil {
				return nil, err
			}
		}
	} else if choice.Mode != providers.ProxyDirect {
		return nil, errors.New("无效网络代理方式")
	}

	key := "direct"
	if proxy != nil {
		key = proxy.String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if client := s.clients[key]; client != nil {
		return client, nil
	}
	client := &http.Client{Transport: outbound.Transport(proxy), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.clients[key] = client
	return client, nil
}

// Close 关闭该调度执行器持有的连接池；宿主应先停止接收并等待在途请求结束。
func (s *SchedulingExecution) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, client := range s.clients {
		client.CloseIdleConnections()
	}
	if runner, ok := s.Executor.Runner.(*scheduling.HTTPRunner); ok {
		runner.Close()
	}
}

// requestOperation 使用协议入口提供的操作范围，空值保留聊天默认语义。
func requestOperation(req scheduling.ExecutionRequest) string {
	if req.Operation != "" {
		return req.Operation
	}
	return "chat"
}
