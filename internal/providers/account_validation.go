package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// accountValidation 绑定供应商、配置世代和完整草稿；轮换凭据仅留在后端，取消不应用余额。
type accountValidation struct {
	// id 是当前结果的随机引用；provider、revision 防止旧草稿覆盖已变更的账户。
	id       string
	provider providerDocument
	revision string
	// config 是验证时归一化配置，rotated 是认证成功后上游返回的新 Refresh Token。
	config  QueryConfig
	rotated string
	// balance 是尚未应用的查询结果，只有成功结果可写入正式余额。
	balance AccountBalance
}

// accountDraftSnapshot 在持有 store.mu 时读取旧账户并做本地格式检查；不访问上游。
func (a *Accounts) accountDraftSnapshot(ctx context.Context, id string, d ProviderAccountDraft) (providerDocument, QueryConfig, ProviderAccount, string, error) {
	p, err := a.provider(ctx, id)
	if err != nil {
		return p, QueryConfig{}, ProviderAccount{}, "", err
	}
	old, raw, revision, err := a.accountConfig(ctx, "provider_accounts", id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, old, ProviderAccount{}, revision, err
	}
	c, err := normalizeQuery(d.Config, old, p.URL, false)
	if err != nil {
		return p, c, ProviderAccount{}, revision, err
	}
	if !finite(d.MonthlyQuota) || d.MonthlyQuota < 0 {
		return p, c, ProviderAccount{}, revision, errors.New("月配额必须是有限非负金额")
	}
	account := ProviderAccount{ProviderID: id, Balance: AccountBalance{Status: QueryStatus{State: "unknown"}, SubscriptionStatus: QueryStatus{State: "unknown"}, Checkin: QueryStatus{State: "unknown"}}}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &account); err != nil {
			return p, c, account, revision, err
		}
	}
	return p, c, account, revision, nil
}

// ValidateProviderAccount 只用当前草稿查询余额，临时结果不写正式余额，也不查询 Key、签到或订阅。
// 60 秒时限包含等待其他查询批次，避免前台排队期间没有超时。
// 已保存连接的 Refresh Token 如被上游轮换，仍需独立保留新凭据，防止取消弹窗使原连接失效。
func (a *Accounts) ValidateProviderAccount(ctx context.Context, id string, d ProviderAccountDraft) (ProviderAccountValidation, error) {
	qctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := a.beginQuery(qctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ProviderAccountValidation{}, fmt.Errorf("等待账户查询超时，请稍后重试: %w", err)
		}
		return ProviderAccountValidation{}, err
	}
	defer a.endQuery()
	a.store.mu.Lock()
	p, c, _, revision, err := a.accountDraftSnapshot(qctx, id, d)
	inputConfig := c
	c = a.rotatedAccountDraft(id, c)
	delete(a.validations, id)
	a.store.mu.Unlock()
	if err != nil {
		return ProviderAccountValidation{}, err
	}
	result := ProviderAccountValidation{Balance: AccountBalance{Status: QueryStatus{State: "unknown"}}}
	h, queryErr := a.connection(qctx, c, p.Request.Proxy)
	if queryErr == nil {
		defer h.transport.CloseIdleConnections()
		queryErr = a.authenticate(qctx, h)
		if queryErr == nil {
			if err = a.saveRotation(qctx, h, "provider_accounts", id, revision); err != nil {
				return result, err
			}
			result.Balance, queryErr = h.balance(qctx, result.Balance, false, false)
		}
	}
	if queryErr != nil {
		result.Balance.Status = failedStatus(result.Balance.Status, queryErr)
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	now, err := a.provider(ctx, id)
	if err != nil {
		return result, err
	}
	_, _, current, err := a.accountConfig(ctx, "provider_accounts", id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if now != p || current != revision {
		return result, errors.New("账户配置已变更，请重新验证")
	}
	result.ID = rand.Text()
	if a.validations == nil {
		a.validations = map[string]accountValidation{}
	}
	rotated := ""
	if c.RefreshToken != inputConfig.RefreshToken {
		rotated = c.RefreshToken
	}
	if h != nil && h.rotated != "" {
		rotated = h.rotated
	}
	a.validations[id] = accountValidation{id: result.ID, provider: p, revision: revision, config: inputConfig, rotated: rotated, balance: result.Balance}
	return result, nil
}

// DiscardProviderAccountValidation 只使对应余额结果失效；已生效的认证轮换仍供同一身份再次验证或保存。
func (a *Accounts) DiscardProviderAccountValidation(id, validationID string) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if v, ok := a.validations[id]; ok && v.id == validationID {
		if v.rotated == "" {
			delete(a.validations, id)
		} else {
			v.id = ""
			v.balance = AccountBalance{}
			a.validations[id] = v
		}
	}
}

// rotatedAccountDraft 在持有 store.mu 时复用同一站点和认证身份已生效的轮换，不恢复已失效的余额结果。
func (a *Accounts) rotatedAccountDraft(id string, c QueryConfig) QueryConfig {
	if v, ok := a.validations[id]; ok && v.rotated != "" && c.Mode == "sub2api" && v.config.Mode == c.Mode && v.config.BaseURL == c.BaseURL && (c.RefreshToken == v.config.RefreshToken || c.RefreshToken == v.rotated) {
		c.RefreshToken = v.rotated
	}
	return c
}

// SaveProviderAccount 只保存配置；携带匹配的验证引用时采用其余额与轮换凭据，不发起网络请求。
func (a *Accounts) SaveProviderAccount(ctx context.Context, id string, d ProviderAccountDraft) (ProviderAccount, error) {
	err := func() error {
		a.store.mu.Lock()
		defer a.store.mu.Unlock()
		p, c, account, revision, err := a.accountDraftSnapshot(ctx, id, d)
		if err != nil {
			return err
		}
		if d.ValidationID != "" {
			v, ok := a.validations[id]
			rotated := v.config
			if v.rotated != "" {
				rotated.RefreshToken = v.rotated
			}
			if !ok || v.id != d.ValidationID || v.provider != p || v.revision != revision || (c != v.config && c != rotated) {
				return errors.New("验证结果已失效，请重新验证或直接保存")
			}
			c = rotated
			if v.balance.Status.State == "success" && !v.balance.Status.LastSuccess.Before(account.Balance.Status.LastSuccess) {
				v.balance.Subscription = account.Balance.Subscription
				v.balance.SubscriptionStatus = account.Balance.SubscriptionStatus
				v.balance.Checkin = account.Balance.Checkin
				account.Balance = v.balance
			}
		}
		c = a.rotatedAccountDraft(id, c)
		account.Config = QueryConfig{}
		account.MonthlyQuota = d.MonthlyQuota
		account.Checkin = d.Checkin
		if err = a.writeAccount(ctx, "provider_accounts", id, c, account, rand.Text()); err != nil {
			return err
		}
		delete(a.validations, id)
		return nil
	}()
	if err != nil {
		return ProviderAccount{}, err
	}
	return a.ProviderAccount(ctx, id)
}

// RefreshReusedProviderKeys 供验证成功并保存后后台调用，只同步复用该账户且选择上游来源的 Key。
// 余额不重复查询，单把 Key 的业务失败保留其状态并继续处理其余 Key。
func (a *Accounts) RefreshReusedProviderKeys(ctx context.Context, id string) error {
	if err := a.beginQuery(ctx); err != nil {
		return err
	}
	defer a.endQuery()
	rows, err := a.store.keyRows(ctx, id)
	if err != nil {
		return err
	}
	for _, row := range rows {
		q, err := a.ProviderKeyQuery(ctx, row.id)
		if err != nil {
			return err
		}
		if !q.ReuseProvider || (q.NameSource == "manual" && q.MultiplierSource == "manual") {
			continue
		}
		if err = a.refreshKey(ctx, row.id); err != nil && !accountObjectMissing(err) {
			return err
		}
	}
	return nil
}
