package providers

import "context"

// saveAndRefreshProviderAccount 保存配置后显式刷新余额及复用连接的 Key，再读取结果，保留既有适配器测试的验收语义。
func saveAndRefreshProviderAccount(a *Accounts, ctx context.Context, id string, draft ProviderAccountDraft) (ProviderAccount, error) {
	if _, err := a.SaveProviderAccount(ctx, id, draft); err != nil {
		return ProviderAccount{}, err
	}
	if _, err := a.refreshAccounts(ctx, id, true, false); err != nil {
		return ProviderAccount{}, err
	}
	if err := a.RefreshReusedProviderKeys(ctx, id); err != nil {
		return ProviderAccount{}, err
	}
	return a.ProviderAccount(ctx, id)
}
