package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
)

// SaveKey 原子保存 Key 基本信息、调用密钥、查询连接与来源；keyID 为空表示创建。
// 新建上游倍率为未知，编辑上游倍率保留已知值；手动倍率按输入校验。
// 仅在新建或变更上游查询所依赖的配置时同步当前 Key；查询失败不撤销本地保存。
func (a *Accounts) SaveKey(ctx context.Context, providerID, keyID string, draft ProviderKeyDraft, query ProviderKeyQueryDraft) (ProviderKeyQuery, error) {
	id, refresh, err := a.saveKeyLocal(ctx, providerID, keyID, draft, query)
	if err != nil {
		return ProviderKeyQuery{}, err
	}
	if refresh {
		return a.RefreshProviderKey(ctx, id)
	}
	return a.ProviderKeyQuery(ctx, id)
}

// saveKeyLocal 在 Store 写锁内校验并原子保存，同时判断本次修改是否需要同步上游。
// 解密和查重在开启事务前执行，兼容单连接 SQLite。
func (a *Accounts) saveKeyLocal(ctx context.Context, providerID, keyID string, draft ProviderKeyDraft, query ProviderKeyQueryDraft) (string, bool, error) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	provider, err := a.provider(ctx, providerID)
	if err != nil {
		return "", false, err
	}
	creating := keyID == ""
	id := keyID
	var saved keyDocument
	var sealed []byte
	if creating {
		id = rand.Text()
	} else {
		var raw string
		err = a.store.db.QueryRowContext(ctx, "SELECT document,secret FROM provider_keys WHERE id=? AND provider_id=?", id, providerID).Scan(&raw, &sealed)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, errKeyMissing
		}
		if err != nil {
			return "", false, err
		}
		if err = a.store.decodeDocument([]byte(raw), &saved); err != nil {
			return "", false, err
		}
	}
	config, status, queryChanged, err := a.normalizeCombinedQuery(ctx, id, provider.URL, query)
	if err != nil {
		return "", false, err
	}
	// 上游倍率不属于手动输入范围，使用占位值只校验其他基本字段，随后恢复真实值或未知。
	if status.MultiplierSource == "upstream" {
		placeholder := 0.0
		draft.Multiplier = &placeholder
	}
	doc, secret, err := draft.normalize()
	if err != nil {
		return "", false, err
	}

	if status.MultiplierSource == "upstream" {
		doc.Multiplier = saved.Multiplier
	}
	if !creating && status.NameSource != "manual" {
		doc.Name = saved.Name
	}
	if secret != "" {
		sealed, err = a.store.sealUnique(ctx, providerID, id, keyID, secret)
		if err != nil {
			return "", false, err
		}
	} else if sealed != nil {
		master, e := a.store.existingMasterKey()
		if e != nil {
			return "", false, e
		}
		if _, e = open(master, secretBinding(id), sealed); e != nil {
			return "", false, e
		}
	}
	keyData, err := a.store.encodeDocument(doc)
	if err != nil {
		return "", false, err
	}
	queryData, err := json.Marshal(status)
	if err != nil {
		return "", false, err
	}
	configData, err := json.Marshal(config)
	if err != nil {
		return "", false, err
	}
	master, err := a.store.sealingMasterKey(ctx)
	if err != nil {
		return "", false, err
	}
	querySealed, err := seal(master, secretBinding("provider_key_queries:"+id), configData)
	if err != nil {
		return "", false, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() // 提交成功后无作用；任何写入失败都同时回滚 Key 与查询配置。
	if creating {
		_, err = tx.ExecContext(ctx, "INSERT INTO provider_keys(id,provider_id,enabled,all_models,secret,document) VALUES(?,?,1,1,?,?)", id, providerID, secretColumn(sealed), string(keyData))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, "UPDATE provider_keys SET secret=?,document=? WHERE id=? AND provider_id=?", secretColumn(sealed), string(keyData), id, providerID)
		if err == nil {
			err = affected(result, errKeyMissing)
		}
	}
	if err != nil {
		return "", false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_key_queries(key_id,secret,document,revision) VALUES(?,?,?,?)
 ON CONFLICT(key_id) DO UPDATE SET secret=excluded.secret,document=excluded.document,revision=excluded.revision`, id, querySealed, string(queryData), rand.Text())
	if err != nil {
		return "", false, err
	}
	if err = tx.Commit(); err != nil {
		return "", false, err
	}
	usesUpstream := status.NameSource != "manual" || status.MultiplierSource != "manual"
	refresh := usesUpstream && (creating || queryChanged || secret != "" || doc.Proxy.Mode != saved.Proxy.Mode)
	return id, refresh, nil
}

// normalizeCombinedQuery 校验并比较归一化后的连接、来源和路径，保留旧查询结果；调用方持有 Store.mu。
func (a *Accounts) normalizeCombinedQuery(ctx context.Context, id, base string, d ProviderKeyQueryDraft) (QueryConfig, ProviderKeyQuery, bool, error) {
	old, raw, _, err := a.accountConfig(ctx, "provider_key_queries", id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return QueryConfig{}, ProviderKeyQuery{}, false, err
	}
	if d.NameSource == "" {
		d.NameSource = "manual"
	}
	if d.MultiplierSource == "" {
		d.MultiplierSource = "manual"
	}
	if d.NameSource != "manual" && d.NameSource != "upstream_key" && d.NameSource != "upstream_group" {
		return QueryConfig{}, ProviderKeyQuery{}, false, errors.New("无效名称来源")
	}
	if d.MultiplierSource != "manual" && d.MultiplierSource != "upstream" {
		return QueryConfig{}, ProviderKeyQuery{}, false, errors.New("无效倍率来源")
	}
	config := QueryConfig{}
	if !d.ReuseProvider {
		config, err = normalizeQuery(d.Config, old, base, true)
		if err != nil {
			return QueryConfig{}, ProviderKeyQuery{}, false, err
		}
	}
	status := ProviderKeyQuery{NameStatus: QueryStatus{State: "unknown"}, MultiplierStatus: QueryStatus{State: "unknown"}}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &status); err != nil {
			return QueryConfig{}, ProviderKeyQuery{}, false, err
		}
	}
	previous := status
	status.KeyID = id
	status.ReuseProvider = d.ReuseProvider
	status.Config = QueryConfig{}
	status.NameSource = d.NameSource
	status.MultiplierSource = d.MultiplierSource
	status.NamePath = d.NamePath
	status.GroupPath = d.GroupPath
	status.MultiplierPath = d.MultiplierPath
	status.KeyPath = d.KeyPath
	return config, status, config != old || status != previous, nil
}
