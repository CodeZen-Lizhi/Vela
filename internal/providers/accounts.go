package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
)

// accountSchema 在既有供应商数据库中新增从属表；secret 为绑定行身份的完整连接密文，document 只含来源与结果。
const accountSchema = `
CREATE TABLE IF NOT EXISTS provider_accounts (
 provider_id TEXT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE, -- 余额所属供应商
 secret BLOB NOT NULL, -- 完整 QueryConfig 密文，含请求头和请求体
 document TEXT NOT NULL, -- 配额、签到与结果，不含认证
 revision TEXT NOT NULL -- 保存配置的世代，拒绝旧请求写回
);
CREATE TABLE IF NOT EXISTS provider_key_queries (
 key_id TEXT PRIMARY KEY REFERENCES provider_keys(id) ON DELETE CASCADE, -- Key 查询归属
 secret BLOB NOT NULL, -- 独立 QueryConfig 密文；复用模式不复制供应商认证
 document TEXT NOT NULL, -- 来源、路径与同步结果
 revision TEXT NOT NULL -- 保存配置的世代
);`

// Accounts 管理余额与 Key 信息查询；网络在 Store 锁外，串行查询防止令牌轮换与重复触发相互覆盖。
type Accounts struct {
	// validations 在 store.mu 保护下为每个供应商保留一份临时结果及已生效的凭据轮换；关闭只使余额结果失效。
	validations map[string]accountValidation

	// store 复用数据库、写锁和凭据库；globalProxy 每次请求读取当前全局出口。
	store       *Store
	globalProxy func(context.Context) (bool, string, error)
	// queryMu 串行化本管理器发起的查询；不阻塞本地配置保存与读取。
	queryMu chan struct{}
	// tokens 在 queryMu 保护的单批查询内缓存访问认证和轮换结果，不持久化 Access Token。
	tokens map[string]accountAuthentication
}

// NewAccounts 建立外键级联账户表，不改变 Store 结构；调用方负责 Store 生命周期。
func NewAccounts(store *Store, globalProxy func(context.Context) (bool, string, error)) (*Accounts, error) {
	if store == nil {
		return nil, errors.New("供应商数据库尚未初始化")
	}
	if _, err := store.db.Exec(accountSchema); err != nil {
		return nil, fmt.Errorf("初始化账户表: %w", err)
	}
	return &Accounts{store: store, globalProxy: globalProxy, queryMu: make(chan struct{}, 1)}, nil
}

// beginQuery 串行化查询批次，排队期间也响应调用方取消，不阻塞应用退出。
func (a *Accounts) beginQuery(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a.queryMu <- struct{}{}:
		a.tokens = nil
		return nil
	}
}

// endQuery 清除批次认证并释放查询席位；与成功的 beginQuery 配对调用。
func (a *Accounts) endQuery() {
	a.tokens = nil
	<-a.queryMu
}

// provider 读取当前供应商文档，余额配置缺省站点与出口均来自它。
func (a *Accounts) provider(ctx context.Context, id string) (providerDocument, error) {
	var raw string
	err := a.store.db.QueryRowContext(ctx, "SELECT document FROM providers WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return providerDocument{}, errProviderMissing
	}
	if err != nil {
		return providerDocument{}, err
	}
	var p providerDocument
	err = a.store.decodeDocument([]byte(raw), &p)
	return p, err
}

// accountConfig 解密指定行的连接；表名只接受模块内部两个固定值。
func (a *Accounts) accountConfig(ctx context.Context, table, id string) (QueryConfig, string, string, error) {
	column := "provider_id"
	if table == "provider_key_queries" {
		column = "key_id"
	} else if table != "provider_accounts" {
		return QueryConfig{}, "", "", errors.New("无效账户表")
	}
	var sealed []byte
	var doc, rev string
	err := a.store.db.QueryRowContext(ctx, "SELECT secret,document,revision FROM "+table+" WHERE "+column+"=?", id).Scan(&sealed, &doc, &rev)
	if err != nil {
		return QueryConfig{}, "", "", err
	}
	master, err := a.store.existingMasterKey()
	if err != nil {
		return QueryConfig{}, "", "", err
	}
	raw, err := open(master, secretBinding(table+":"+id), sealed)
	if err != nil {
		return QueryConfig{}, "", "", err
	}
	var c QueryConfig
	err = json.Unmarshal(raw, &c)
	return c, doc, rev, err
}

// masked 隐藏所有可携带认证的字段，不向页面回传任何密钥片段。
func (c QueryConfig) masked() QueryConfig {
	for _, v := range []*string{&c.RefreshToken, &c.AccessToken, &c.APIKey, &c.Headers, &c.Body} {
		if *v != "" {
			*v = "***"
		}
	}
	if u, err := url.Parse(c.URL); err == nil && u.RawQuery != "" {
		query := u.Query()
		for key, values := range query {
			for i := range values {
				values[i] = "***"
			}
			query[key] = values
		}
		u.RawQuery = query.Encode()
		c.URL = u.String()
	}
	return c
}

// normalizeQuery 合并留空秘密并校验请求边界；方式改变不复用旧认证。
func normalizeQuery(c, old QueryConfig, base string, key bool) (QueryConfig, error) {
	if c.Mode == old.Mode {
		// URL 查询参数可能携带凭据，页面掩码回传时按字段保留原值。
		if c.URL == "" || c.URL == old.masked().URL {
			c.URL = old.URL
		}
		if current, e := url.Parse(c.URL); e == nil {
			if previous, e := url.Parse(old.URL); e == nil {
				values := current.Query()
				for key, entries := range values {
					for i, value := range entries {
						if (value == "***" || value == "") && i < len(previous.Query()[key]) {
							entries[i] = previous.Query()[key][i]
						}
					}
					values[key] = entries
				}
				current.RawQuery = values.Encode()
				c.URL = current.String()
			}
		}
		n := []*string{&c.RefreshToken, &c.AccessToken, &c.APIKey, &c.Headers, &c.Body}
		o := []string{old.RefreshToken, old.AccessToken, old.APIKey, old.Headers, old.Body}
		for i, p := range n {
			if *p == "" || *p == "***" {
				*p = o[i]
			}
		}
	}
	if c.Mode == "" {
		return QueryConfig{}, nil
	}
	if c.BaseURL == "" {
		c.BaseURL = base
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	if c.QuotaDivisor == 0 {
		c.QuotaDivisor = 500000
	}
	if !finite(c.QuotaDivisor) || c.QuotaDivisor <= 0 {
		return c, errors.New("额度除数必须为有限正数")
	}
	switch c.Mode {
	case "sub2api":
		if c.RefreshToken == "" || c.RefreshToken == "***" {
			return c, errors.New("请填写 Refresh Token")
		}
	case "newapi":
		if c.UserID == "" || c.AccessToken == "" || c.AccessToken == "***" {
			return c, errors.New("请填写用户 ID 与系统访问令牌")
		}
	case "usage_api":
		if !key && (c.APIKey == "" || c.APIKey == "***") {
			return c, errors.New("请填写 API Key")
		}
	case "custom":
		if c.Method == "" {
			c.Method = "GET"
		}
		if c.Method != "GET" && c.Method != "POST" {
			return c, errors.New("仅支持 GET 或 POST")
		}
		if err := customAccountURL(c.URL); err != nil {
			return c, err
		}
		if !key && strings.TrimSpace(c.BalancePath) == "" {
			return c, errors.New("请填写余额字段路径")
		}
		if c.Headers != "" {
			var h map[string]string
			if json.Unmarshal([]byte(c.Headers), &h) != nil || h == nil {
				return c, errors.New("请求头必须是 JSON 字符串对象")
			}
			for k, v := range h {
				if strings.ContainsAny(k+v, "\r\n") {
					return c, errors.New("请求头不能包含换行")
				}
			}
		}
		if c.Body != "" && !json.Valid([]byte(c.Body)) {
			return c, errors.New("POST 请求体必须是合法 JSON")
		}
		return c, nil
	default:
		return c, errors.New("未知账户接入方式")
	}
	if err := accountURL(c.BaseURL); err != nil {
		return c, err
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c, nil
}

// accountURL 限制查询地址为不含 URL 认证和查询秘密的 HTTP(S) 地址。
func accountURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("查询地址必须是有效 HTTP(S) 地址，不含账号密码、查询参数或片段；认证请放请求头或 JSON 体")
	}
	return nil
}

// customAccountURL 支持自定义 GET/POST 查询参数；参数随完整连接加密，读取时全部掩码。
func customAccountURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("查询地址格式无效")
	}
	if _, err = url.ParseQuery(u.RawQuery); err != nil {
		return errors.New("URL 查询参数格式无效")
	}
	u.RawQuery = ""
	return accountURL(u.String())
}

// finite 区分可用数值与 NaN、无穷值。
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// writeAccount 加密配置并原子保存记录；调用方持有 Store.mu。
func (a *Accounts) writeAccount(ctx context.Context, table, id string, c QueryConfig, doc any, revision string) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	master, err := a.store.sealingMasterKey(ctx)
	if err != nil {
		return err
	}
	sealed, err := seal(master, secretBinding(table+":"+id), raw)
	if err != nil {
		return err
	}
	col := "provider_id"
	if table == "provider_key_queries" {
		col = "key_id"
	} else if table != "provider_accounts" {
		return errors.New("无效账户表")
	}
	_, err = a.store.db.ExecContext(ctx, "INSERT INTO "+table+"("+col+",secret,document,revision) VALUES(?,?,?,?) ON CONFLICT("+col+") DO UPDATE SET secret=excluded.secret,document=excluded.document,revision=excluded.revision", id, sealed, string(data), revision)
	return err
}

// ProviderAccount 读取本地余额连接与状态；缺省未配置，不进行网络查询。
func (a *Accounts) ProviderAccount(ctx context.Context, id string) (ProviderAccount, error) {
	if _, err := a.provider(ctx, id); err != nil {
		return ProviderAccount{}, err
	}
	c, raw, _, err := a.accountConfig(ctx, "provider_accounts", id)
	r := ProviderAccount{ProviderID: id, Balance: AccountBalance{Status: QueryStatus{State: "unknown"}, SubscriptionStatus: QueryStatus{State: "unknown"}, Checkin: QueryStatus{State: "unknown"}}}
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	r.Config = c.masked()
	return r, nil
}

// ProviderKeyQuery 读取本地来源；尚未保存时默认复用供应商、名称和倍率手动。
func (a *Accounts) ProviderKeyQuery(ctx context.Context, id string) (ProviderKeyQuery, error) {
	var exists bool
	if err := a.store.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_keys WHERE id=?)", id).Scan(&exists); err != nil {
		return ProviderKeyQuery{}, err
	}
	if !exists {
		return ProviderKeyQuery{}, errKeyMissing
	}
	c, raw, _, err := a.accountConfig(ctx, "provider_key_queries", id)
	r := ProviderKeyQuery{KeyID: id, ReuseProvider: true, NameSource: "manual", MultiplierSource: "manual", NameStatus: QueryStatus{State: "unknown"}, MultiplierStatus: QueryStatus{State: "unknown"}}
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	r.Config = c.masked()
	return r, nil
}

// SaveProviderKeyQuery 保存独立来源与连接，不更改现有值；新建上游来源 Key 应由创建方以 nil 倍率保存。
func (a *Accounts) SaveProviderKeyQuery(ctx context.Context, id string, d ProviderKeyQueryDraft) (ProviderKeyQuery, error) {
	err := func() error {
		a.store.mu.Lock()
		defer a.store.mu.Unlock()
		var pid, doc string
		if e := a.store.db.QueryRowContext(ctx, "SELECT provider_id,document FROM provider_keys WHERE id=?", id).Scan(&pid, &doc); e != nil {
			return e
		}
		p, e := a.provider(ctx, pid)
		if e != nil {
			return e
		}
		old, raw, _, e := a.accountConfig(ctx, "provider_key_queries", id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if d.NameSource == "" {
			d.NameSource = "manual"
		}
		if d.MultiplierSource == "" {
			d.MultiplierSource = "manual"
		}
		if d.NameSource != "manual" && d.NameSource != "upstream_key" && d.NameSource != "upstream_group" {
			return errors.New("无效名称来源")
		}
		if d.MultiplierSource != "manual" && d.MultiplierSource != "upstream" {
			return errors.New("无效倍率来源")
		}
		c := QueryConfig{}
		if !d.ReuseProvider {
			c, e = normalizeQuery(d.Config, old, p.URL, true)
			if e != nil {
				return e
			}
		}
		q := ProviderKeyQuery{NameStatus: QueryStatus{State: "unknown"}, MultiplierStatus: QueryStatus{State: "unknown"}}
		if raw != "" {
			if e = json.Unmarshal([]byte(raw), &q); e != nil {
				return e
			}
		}
		q.KeyID = id
		q.ReuseProvider = d.ReuseProvider
		q.Config = QueryConfig{}
		q.NameSource = d.NameSource
		q.MultiplierSource = d.MultiplierSource
		q.NamePath = d.NamePath
		q.GroupPath = d.GroupPath
		q.MultiplierPath = d.MultiplierPath
		q.KeyPath = d.KeyPath
		keyData := []byte(doc)

		return a.writeKeyQuery(ctx, id, c, q, rand.Text(), keyData)
	}()
	if err != nil {
		return ProviderKeyQuery{}, err
	}
	return a.RefreshProviderKey(ctx, id)
}

// RefreshProviderKey 只查询指定 Key，手动来源不访问网络。
func (a *Accounts) RefreshProviderKey(ctx context.Context, id string) (ProviderKeyQuery, error) {
	if err := a.beginQuery(ctx); err != nil {
		return ProviderKeyQuery{}, err
	}
	defer a.endQuery()
	if err := a.refreshKey(ctx, id); err != nil {
		return ProviderKeyQuery{}, err
	}
	return a.ProviderKeyQuery(ctx, id)
}

// RefreshProviderAccounts 查询已配置账户和上游来源 Key；空 ID 遍历全部供应商，包括停用项；跳过批次执行期间已删除的对象。
func (a *Accounts) RefreshProviderAccounts(ctx context.Context, id string) (ProviderAccountRefresh, error) {
	return a.refreshAccounts(ctx, id, true, true)
}

// refreshAccounts 按查询范围处理对象；静默后台批次单项失败继续，主动调用仍反馈错误。
func (a *Accounts) refreshAccounts(ctx context.Context, id string, balance, keyInfo bool) (ProviderAccountRefresh, error) {
	if err := a.beginQuery(ctx); err != nil {
		return ProviderAccountRefresh{}, err
	}
	defer a.endQuery()
	result := ProviderAccountRefresh{Accounts: []ProviderAccount{}, Keys: []ProviderKeyQuery{}}
	ids := []string{id}
	if id == "" {
		ids = nil
		rows, e := a.store.db.QueryContext(ctx, "SELECT id FROM providers ORDER BY seq")
		if e != nil {
			return result, e
		}
		for rows.Next() {
			var v string
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return result, e
			}
			ids = append(ids, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return result, e
		}
	}
	for _, pid := range ids {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if balance {
			if err := a.refreshBalance(ctx, pid); err != nil {
				if !accountObjectMissing(err) && !isSilentRefresh(ctx) {
					return result, err
				}
				if accountObjectMissing(err) {
					continue
				}
			}
		}
		keys, err := a.store.keyRows(ctx, pid)
		if err != nil {
			if isSilentRefresh(ctx) {
				continue
			}
			return result, err
		}
		providerKeys := []ProviderKeyQuery{}
		for _, key := range keys {
			if !keyInfo {
				break
			}
			if err = a.refreshKey(ctx, key.id); err != nil {
				if accountObjectMissing(err) || isSilentRefresh(ctx) {
					continue
				}
				return result, err
			}
			q, err := a.ProviderKeyQuery(ctx, key.id)
			if err != nil {
				if accountObjectMissing(err) || isSilentRefresh(ctx) {
					continue
				}
				return result, err
			}
			providerKeys = append(providerKeys, q)
		}
		// Key 网络请求期间供应商也可能被删除，确认主体仍在才交付整组结果。
		account, err := a.ProviderAccount(ctx, pid)
		if err != nil {
			if accountObjectMissing(err) || isSilentRefresh(ctx) {
				continue
			}
			return result, err
		}
		result.Accounts = append(result.Accounts, account)
		result.Keys = append(result.Keys, providerKeys...)
	}
	return result, nil
}

// accountObjectMissing 识别批次快照在正常删除后失效的错误；不把数据库或凭据故障当成删除。
func accountObjectMissing(err error) bool {
	return errors.Is(err, errKeyMissing) || errors.Is(err, errProviderMissing) || errors.Is(err, sql.ErrNoRows)
}
