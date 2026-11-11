package providers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// refreshBalance 查询余额并条件写回；保存/删除或供应商出口改变后丢弃在途结果。
func (a *Accounts) refreshBalance(ctx context.Context, id string) error {
	a.store.mu.Lock()
	c, raw, rev, err := a.accountConfig(ctx, "provider_accounts", id)
	p, pe := a.provider(ctx, id)
	a.store.mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		return pe
	}
	if err != nil {
		return err
	}
	if pe != nil {
		return pe
	}
	if c.Mode == "" {
		return nil
	}
	var account ProviderAccount
	if err = json.Unmarshal([]byte(raw), &account); err != nil {
		return err
	}
	qctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	proxy, queryErr := a.store.proxyForNetwork(proxyBinding("provider", id), p.Request.Proxy)
	var h *accountHTTP
	if queryErr == nil {
		h, queryErr = a.connection(qctx, c, proxy, proxy)
	}
	if queryErr == nil {
		defer h.transport.CloseIdleConnections()
		queryErr = a.authenticate(qctx, h)
	}
	balance := account.Balance
	if queryErr == nil {
		balance, queryErr = h.balance(qctx, balance, account.Checkin)
	}
	if queryErr != nil {
		balance.Status = failedStatus(balance.Status, queryErr)
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	_, _, current, err := a.accountConfig(ctx, "provider_accounts", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now, err := a.provider(ctx, id)
	if err != nil {
		return err
	}
	if current != rev || now != p {
		return nil
	}
	account.Balance = balance
	account.Config = QueryConfig{}
	if h != nil && h.rotated != "" {
		c.RefreshToken = h.rotated
	}
	return a.writeAccount(ctx, "provider_accounts", id, c, account, rev)
}

// authenticate 在同一查询批次复用 Sub2API 认证，避免余额和复用 Key 反复续期；批次结束清空内存缓存。
func (a *Accounts) authenticate(ctx context.Context, h *accountHTTP) error {
	if h.config.Mode != "sub2api" {
		return h.authenticate(ctx)
	}
	cacheKey := h.config.BaseURL + "\x00" + h.config.RefreshToken
	if token := a.tokens[cacheKey]; token != "" {
		h.headers["Authorization"] = token
		return nil
	}
	if err := h.authenticate(ctx); err != nil {
		return err
	}
	if a.tokens == nil {
		a.tokens = map[string]string{}
	}
	token := h.headers["Authorization"]
	a.tokens[cacheKey] = token
	if h.rotated != "" {
		a.tokens[h.config.BaseURL+"\x00"+h.rotated] = token
	}
	return nil
}

// refreshKey 对选定来源按字段更新，写回前确认 Key 密文、连接世代、来源和供应商快照仍一致。
func (a *Accounts) refreshKey(ctx context.Context, id string) error {
	q, err := a.ProviderKeyQuery(ctx, id)
	if err != nil {
		return err
	}
	if q.NameSource == "manual" && q.MultiplierSource == "manual" {
		return nil
	}
	a.store.mu.Lock()
	c, raw, rev, err := a.accountConfig(ctx, "provider_key_queries", id)
	if err != nil {
		a.store.mu.Unlock()
		return err
	}
	if err = json.Unmarshal([]byte(raw), &q); err != nil {
		a.store.mu.Unlock()
		return err
	}
	var pid, keyRaw string
	var sealed []byte
	err = a.store.db.QueryRowContext(ctx, "SELECT provider_id,secret,document FROM provider_keys WHERE id=?", id).Scan(&pid, &sealed, &keyRaw)
	if err != nil {
		a.store.mu.Unlock()
		return err
	}
	p, err := a.provider(ctx, pid)
	if err != nil {
		a.store.mu.Unlock()
		return err
	}
	var kd keyDocument
	if err = a.store.decodeDocument([]byte(keyRaw), &kd); err != nil {
		a.store.mu.Unlock()
		return err
	}
	parentRevision := ""
	configErr := error(nil)
	if q.ReuseProvider {
		c, _, parentRevision, configErr = a.accountConfig(ctx, "provider_accounts", pid)
		if errors.Is(configErr, sql.ErrNoRows) {
			configErr = errors.New("供应商未配置余额连接，请单独配置 Key 查询")
		}
	}
	var secret string
	if configErr == nil {
		if sealed == nil {
			configErr = errors.New("当前 Key 尚未设置调用密钥")
		} else {
			master, e := a.store.existingMasterKey()
			if e != nil {
				a.store.mu.Unlock()
				return e
			}
			plain, e := open(master, secretBinding(id), sealed)
			if e != nil {
				a.store.mu.Unlock()
				return e
			}
			secret = string(plain)
		}
	}
	a.store.mu.Unlock()
	qctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	queryErr := configErr
	var info keyInformation
	var h *accountHTTP
	if queryErr == nil && c.Mode == "" {
		queryErr = errors.New("尚未配置信息查询连接")
	}
	if queryErr == nil {
		// 只解密实际生效的出口；Key 直连或独立代理不依赖未使用的供应商认证。
		proxy, binding := kd.Proxy, proxyBinding("key", id)
		if proxy.Mode == ProxyProvider || proxy.Mode == "" {
			proxy, binding = p.Request.Proxy, proxyBinding("provider", pid)
		}
		proxy, queryErr = a.store.proxyForNetwork(binding, proxy)
		if queryErr == nil {
			h, queryErr = a.connection(qctx, c, proxy, proxy)
		}
	}
	if queryErr == nil {
		defer h.transport.CloseIdleConnections()
		queryErr = a.authenticate(qctx, h)
	}
	if queryErr == nil {
		info, queryErr = h.keyInfo(qctx, secret, q)
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	currentConfig, _, currentRev, err := a.accountConfig(ctx, "provider_key_queries", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if currentRev != rev {
		return nil
	}
	var nowKey string
	var nowSealed []byte
	err = a.store.db.QueryRowContext(ctx, "SELECT secret,document FROM provider_keys WHERE id=?", id).Scan(&nowSealed, &nowKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	nowProvider, err := a.provider(ctx, pid)
	if err != nil {
		return err
	}
	if !bytes.Equal(nowSealed, sealed) || nowKey != keyRaw || nowProvider != p {
		return nil
	}
	if q.ReuseProvider {
		_, _, nowRev, e := a.accountConfig(ctx, "provider_accounts", pid)
		if errors.Is(e, sql.ErrNoRows) && parentRevision == "" {
		} else if e != nil {
			return e
		} else if nowRev != parentRevision {
			return nil
		}
	}
	if q.NameSource != "manual" {
		name := info.name
		if q.NameSource == "upstream_group" {
			name = info.group
		}
		e := queryErr
		if e == nil && name == "" {
			e = errors.New("所选上游名称字段缺失或不支持")
		}
		if e != nil {
			q.NameStatus = failedStatus(q.NameStatus, e)
		} else {
			kd.Name = name
			q.NameStatus = successStatus()
		}
	}
	if q.MultiplierSource == "upstream" {
		e := queryErr
		if e == nil {
			e = info.rateError
		}
		if e == nil && (info.rate == nil || *info.rate < 0) {
			e = errors.New("上游倍率字段缺失或不是有限非负数值")
		}
		if e != nil {
			q.MultiplierStatus = failedStatus(q.MultiplierStatus, e)
		} else {
			kd.Multiplier = info.rate
			q.MultiplierStatus = successStatus()
		}
	}
	if queryErr == nil && info.group != "" {
		q.GroupName = info.group
	}
	if h != nil && h.rotated != "" {
		if q.ReuseProvider {
			pc, pr, prev, e := a.accountConfig(ctx, "provider_accounts", pid)
			if e != nil {
				return e
			}
			var account ProviderAccount
			if e = json.Unmarshal([]byte(pr), &account); e != nil {
				return e
			}
			pc.RefreshToken = h.rotated
			if e = a.writeAccount(ctx, "provider_accounts", pid, pc, account, prev); e != nil {
				return e
			}
		} else {
			currentConfig.RefreshToken = h.rotated
		}
	}
	doc, err := a.store.encodeDocument(kd)
	if err != nil {
		return err
	}
	return a.writeKeyQuery(ctx, id, currentConfig, q, rev, doc)
}

// writeKeyQuery 把 Key 文档和查询来源/状态作为一次事务保存；主密钥错误时不改变任何字段。
// 调用方持有 Store.mu，配置已经过校验。
func (a *Accounts) writeKeyQuery(ctx context.Context, id string, c QueryConfig, q ProviderKeyQuery, rev string, keyData []byte) error {
	q.Config = QueryConfig{}
	qdoc, err := json.Marshal(q)
	if err != nil {
		return err
	}
	configRaw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	master, err := a.store.sealingMasterKey(ctx)
	if err != nil {
		return err
	}
	sealed, err := seal(master, secretBinding("provider_key_queries:"+id), configRaw)
	if err != nil {
		return err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 成功后回滚无作用，其他分支保持原值。
	if _, err = tx.ExecContext(ctx, "UPDATE provider_keys SET document=? WHERE id=?", string(keyData), id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_key_queries(key_id,secret,document,revision) VALUES(?,?,?,?)
        ON CONFLICT(key_id) DO UPDATE SET secret=excluded.secret,document=excluded.document,revision=excluded.revision`, id, sealed, string(qdoc), rev)
	if err != nil {
		return err
	}
	return tx.Commit()
}
