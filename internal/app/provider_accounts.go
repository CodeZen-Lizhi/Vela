package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// accountManager 取得已初始化账户管理器，初始化失败不返回伪造状态。
func (s *VelaService) accountManager() (*providers.Accounts, error) {
	if s.accounts == nil {
		return nil, errors.New("账户查询尚未初始化")
	}
	return s.accounts, nil
}

// ProviderAccount 只读取本地账户配置与结果，不访问上游。
func (s *VelaService) ProviderAccount(ctx context.Context, providerID string) (providers.ProviderAccount, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderAccount{}, e
	}
	return a.ProviderAccount(ctx, providerID)
}

// SaveProviderAccount 保存账户并独立同步余额及复用它的 Key；同步失败在结果状态中表达。
func (s *VelaService) SaveProviderAccount(ctx context.Context, providerID string, draft providers.ProviderAccountDraft) (providers.ProviderAccount, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderAccount{}, e
	}
	return a.SaveProviderAccount(ctx, providerID, draft)
}

// ProviderKeyQuery 只读取本地 Key 查询配置与状态。
func (s *VelaService) ProviderKeyQuery(ctx context.Context, keyID string) (providers.ProviderKeyQuery, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderKeyQuery{}, e
	}
	return a.ProviderKeyQuery(ctx, keyID)
}

// SaveProviderKeyQuery 保存来源和查询配置，只同步当前 Key。
func (s *VelaService) SaveProviderKeyQuery(ctx context.Context, keyID string, draft providers.ProviderKeyQueryDraft) (providers.ProviderKeyQuery, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderKeyQuery{}, e
	}
	return a.SaveProviderKeyQuery(ctx, keyID, draft)
}

// RefreshProviderAccounts 主动查询指定供应商，空 ID 查询全部（包括只有 Key 独立连接的供应商）。
func (s *VelaService) RefreshProviderAccounts(ctx context.Context, providerID string) (providers.ProviderAccountRefresh, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderAccountRefresh{}, e
	}
	return a.RefreshProviderAccounts(ctx, providerID)
}

// RefreshProviderKey 用于创建或替换调用密钥后只同步受影响 Key。
func (s *VelaService) RefreshProviderKey(ctx context.Context, keyID string) (providers.ProviderKeyQuery, error) {
	a, e := s.accountManager()
	if e != nil {
		return providers.ProviderKeyQuery{}, e
	}
	return a.RefreshProviderKey(ctx, keyID)
}

// SaveProviderKey 原子保存 Key 与查询配置；keyID 为空创建，提交后的上游失败通过返回状态提示。
func (s *VelaService) SaveProviderKey(ctx context.Context, providerID, keyID string, draft providers.ProviderKeyDraft, query providers.ProviderKeyQueryDraft) (providers.ProviderKeyQuery, error) {
	a, err := s.accountManager()
	if err != nil {
		return providers.ProviderKeyQuery{}, err
	}
	return a.SaveKey(ctx, providerID, keyID, draft, query)
}
