package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// Configuration 是后端专用搬运数据，包含完整明文凭据；调用方必须加密文件且不得返回页面或记录日志。
// 不包含本机主密钥、余额结果、使用记录、统计和健康状态。
type Configuration struct {
	// Providers 按创建顺序包含供应商及其从属配置。
	Providers []ConfigurationProvider `json:"providers"`
}

// ConfigurationProvider 保存一个供应商的完整可编辑配置；身份用于跨机器匹配。
type ConfigurationProvider struct {
	// Provider 包含供应商身份、名称、启停、地址和请求策略。
	Provider Provider `json:"provider"`
	// Keys、Models 分别包含完整 Key 和模型关联；顺序保留来源创建顺序及映射顺序。
	Keys   []ConfigurationKey   `json:"keys"`
	Models []ConfigurationModel `json:"models"`
	// Account 为可选余额查询配置；nil 表示来源未配置，不删除目的机器已有配置。
	Account *ConfigurationAccount `json:"account,omitempty"`
}

// ConfigurationKey 保存 Key 的真实密钥与可编辑字段，不携带掩码和运行状态。
type ConfigurationKey struct {
	// ID 为来源稳定身份；匹配时保留目的机器身份。
	ID string `json:"id"`
	// Draft 包含完整 Secret；空表示来源确实未设置密钥。
	Draft ProviderKeyDraft `json:"draft"`
	// Enabled、AllModels、ModelIDs 分别是启停与独立权限模式，指定权限必须引用同供应商关联模型。
	Enabled   bool     `json:"enabled"`
	AllModels bool     `json:"allModels"`
	ModelIDs  []string `json:"modelIds"`
	// Query 保存未脱敏的独立查询连接、来源与路径，nil 表示来源未保存查询配置。
	Query *ProviderKeyQueryDraft `json:"query,omitempty"`
}

// ConfigurationModel 搬运关联启停及有序映射；ModelID 为来源统一模型身份。
type ConfigurationModel struct {
	// ModelID、Enabled、Mappings 分别是来源模型身份、关联启停及完整映射配置。
	ModelID  string                 `json:"modelId"`
	Enabled  bool                   `json:"enabled"`
	Mappings []ProviderModelMapping `json:"mappings"`
}

// ConfigurationAccount 仅携带余额查询认证及配置，不接受余额、签到结果或临时验证引用。
type ConfigurationAccount struct {
	// Config 是未脱敏连接，MonthlyQuota 为本地月配额美元金额，Checkin 为自动签到选择。
	Config       QueryConfig `json:"config"`
	MonthlyQuota float64     `json:"monthlyQuota"`
	Checkin      bool        `json:"checkin"`
}

// ConfigurationTransaction 串行执行整包配置事务；必须在开启事务前准备主密钥，避免单连接 SQLite 死锁。
// 回调内各模块必须只使用传入的 tx；返回错误或 panic 均回滚数据库，已生成的本机主密钥保留。
func (s *Store) ConfigurationTransaction(ctx context.Context, apply func(*sql.Tx) error) error {
	if apply == nil {
		return errors.New("配置事务回调不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sealingMasterKey(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = apply(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// 地址或密钥可能改变，提交成功后使旧模型发现结果失效。
	s.upstreamCache = nil
	return nil
}

// ExportConfiguration 从调用方事务读取一致快照并实际解密全部秘密；不取得 Store.mu，也不创建或提交事务。
func (s *Store) ExportConfiguration(ctx context.Context, tx *sql.Tx) (Configuration, error) {
	result := Configuration{Providers: []ConfigurationProvider{}}
	if tx == nil {
		return result, errors.New("配置导出需要事务")
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,enabled,document FROM providers ORDER BY seq")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, name, raw string
		var enabled bool
		if err = rows.Scan(&id, &name, &enabled, &raw); err != nil {
			break
		}
		var doc providerDocument
		if err = s.decodeDocument([]byte(raw), &doc); err != nil {
			break
		}
		result.Providers = append(result.Providers, ConfigurationProvider{Provider: doc.provider(id, name, enabled), Keys: []ConfigurationKey{}, Models: []ConfigurationModel{}})
	}
	err = finishConfigurationRows(rows, err)
	if err != nil {
		return Configuration{}, err
	}
	for i := range result.Providers {
		if err = s.exportProviderConfiguration(ctx, tx, &result.Providers[i]); err != nil {
			return Configuration{}, err
		}
	}
	return result, nil
}

// finishConfigurationRows 在进一步查询前关闭结果集，保留扫描、迭代及关闭错误。
func finishConfigurationRows(rows *sql.Rows, scanErr error) error {
	return errors.Join(scanErr, rows.Err(), rows.Close())
}

// configurationTable 在当前事务检查可选模块的表，不通过 db 取得第二条连接。
func configurationTable(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists)
	return exists, err
}

// exportProviderConfiguration 读取供应商从属配置；先关闭每个结果集，再发起后续查询。
func (s *Store) exportProviderConfiguration(ctx context.Context, tx *sql.Tx, p *ConfigurationProvider) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,enabled,all_models,secret,document FROM provider_keys WHERE provider_id=? ORDER BY seq", p.Provider.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k ConfigurationKey
		var sealed []byte
		var raw string
		if err = rows.Scan(&k.ID, &k.Enabled, &k.AllModels, &sealed, &raw); err != nil {
			break
		}
		var doc keyDocument
		if err = s.decodeDocument([]byte(raw), &doc); err != nil {
			break
		}
		k.Draft = ProviderKeyDraft{Name: doc.Name, Note: doc.Note, Multiplier: doc.Multiplier, Priority: doc.Priority, RPMLimit: doc.RPMLimit, ConcurrencyLimit: doc.ConcurrencyLimit, CacheMinutes: doc.CacheMinutes, ProbeMinutes: doc.ProbeMinutes, Proxy: doc.Proxy}
		k.ModelIDs = []string{}
		if sealed != nil {
			var master, plain []byte
			if master, err = s.existingMasterKey(); err != nil {
				break
			}
			if plain, err = open(master, secretBinding(k.ID), sealed); err != nil {
				break
			}
			k.Draft.Secret = string(plain)
		}
		p.Keys = append(p.Keys, k)
	}
	if err = finishConfigurationRows(rows, err); err != nil {
		return err
	}
	if exists, err := configurationTable(ctx, tx, "provider_models"); err != nil {
		return err
	} else if exists {
		if err = exportConfigurationModels(ctx, tx, p); err != nil {
			return err
		}
	}
	if exists, err := configurationTable(ctx, tx, "provider_accounts"); err != nil {
		return err
	} else if exists {
		var a ProviderAccount
		config, found, err := s.exportConfigurationQuery(ctx, tx, "provider_accounts", p.Provider.ID, &a)
		if err != nil {
			return err
		}
		if found {
			p.Account = &ConfigurationAccount{Config: config, MonthlyQuota: a.MonthlyQuota, Checkin: a.Checkin}
		}
	}
	if exists, err := configurationTable(ctx, tx, "provider_key_queries"); err != nil {
		return err
	} else if exists {
		for i := range p.Keys {
			k := &p.Keys[i]
			var q ProviderKeyQuery
			config, found, err := s.exportConfigurationQuery(ctx, tx, "provider_key_queries", k.ID, &q)
			if err != nil {
				return err
			}
			if found {
				k.Query = &ProviderKeyQueryDraft{ReuseProvider: q.ReuseProvider, Config: config, NameSource: q.NameSource, MultiplierSource: q.MultiplierSource, NamePath: q.NamePath, GroupPath: q.GroupPath, MultiplierPath: q.MultiplierPath, KeyPath: q.KeyPath}
			}
		}
	}
	return nil
}

// exportConfigurationModels 读取关联、映射和指定权限；不读取统一模型的运行数据。
func exportConfigurationModels(ctx context.Context, tx *sql.Tx, p *ConfigurationProvider) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.model_id,p.enabled,COALESCE(m.document,'[]') FROM provider_models p LEFT JOIN provider_model_mappings m ON m.provider_id=p.provider_id AND m.model_id=p.model_id WHERE p.provider_id=? ORDER BY p.model_id`, p.Provider.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m ConfigurationModel
		var raw string
		if err = rows.Scan(&m.ModelID, &m.Enabled, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &m.Mappings); err != nil {
			break
		}
		p.Models = append(p.Models, m)
	}
	if err = finishConfigurationRows(rows, err); err != nil {
		return err
	}
	for i := range p.Keys {
		rows, err = tx.QueryContext(ctx, "SELECT model_id FROM provider_key_models WHERE key_id=? ORDER BY model_id", p.Keys[i].ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			p.Keys[i].ModelIDs = append(p.Keys[i].ModelIDs, id)
		}
		if err = finishConfigurationRows(rows, err); err != nil {
			return err
		}
	}
	return nil
}

// exportConfigurationQuery 解密绑定行的完整认证；表名只接受内部固定的账户与 Key 查询表。
func (s *Store) exportConfigurationQuery(ctx context.Context, tx *sql.Tx, table, id string, value any) (QueryConfig, bool, error) {
	column := "provider_id"
	if table == "provider_key_queries" {
		column = "key_id"
	} else if table != "provider_accounts" {
		return QueryConfig{}, false, errors.New("无效账户表")
	}
	var sealed []byte
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT secret,document FROM "+table+" WHERE "+column+"=?", id).Scan(&sealed, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return QueryConfig{}, false, nil
	}
	if err != nil {
		return QueryConfig{}, false, err
	}
	if err = json.Unmarshal([]byte(raw), value); err != nil {
		return QueryConfig{}, false, errors.New("账户配置数据损坏")
	}
	master, err := s.existingMasterKey()
	if err != nil {
		return QueryConfig{}, false, err
	}
	plain, err := open(master, secretBinding(table+":"+id), sealed)
	if err != nil {
		return QueryConfig{}, false, err
	}
	var config QueryConfig
	if err = json.Unmarshal(plain, &config); err != nil {
		return config, false, errors.New("账户认证数据损坏")
	}
	return config, true, nil
}

// ValidateConfiguration 对整包来源配置做纯校验；modelIDs 是包中全部统一模型身份集合，不能用目的机器身份代替。
func ValidateConfiguration(value Configuration, modelIDs map[string]bool) error {
	_, err := normalizeConfiguration(value, modelIDs)
	return err
}

// configurationIdentity 拒绝空白或带首尾空格的身份，避免重映射时静默合并不同引用。
func configurationIdentity(id string) bool { return id != "" && id == strings.TrimSpace(id) }

// normalizeConfiguration 复用现有业务校验并复制所有嵌套配置；先校验整包，避免边写入边发现来源错误。
func normalizeConfiguration(value Configuration, modelIDs map[string]bool) (Configuration, error) {
	result := Configuration{Providers: make([]ConfigurationProvider, 0, len(value.Providers))}
	ids, names, keys := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range value.Providers {
		if !configurationIdentity(p.Provider.ID) || ids[p.Provider.ID] {
			return result, errors.New("供应商身份为空或重复")
		}
		ids[p.Provider.ID] = true
		name, doc, err := (ProviderDraft{Name: p.Provider.Name, Website: p.Provider.Website, URL: p.Provider.URL, Request: p.Provider.Request}).normalize()
		if err != nil {
			return result, err
		}
		if names[name] {
			return result, errors.New("供应商名称重复")
		}
		names[name] = true
		p.Provider = doc.provider(p.Provider.ID, name, p.Provider.Enabled)
		p.Models = append([]ConfigurationModel{}, p.Models...)
		associated := map[string]bool{}
		for i, m := range p.Models {
			if !configurationIdentity(m.ModelID) || !modelIDs[m.ModelID] || associated[m.ModelID] {
				return result, errors.New("供应商模型关联为空、重复或引用未知模型")
			}
			associated[m.ModelID] = true
			p.Models[i].Mappings, err = normalizeModelMappings(m.Mappings)
			if err != nil {
				return result, err
			}
		}
		if p.Account != nil {
			a := *p.Account
			if !finite(a.MonthlyQuota) || a.MonthlyQuota < 0 {
				return result, errors.New("月配额必须是有限非负金额")
			}
			if err = validateConfigurationSecrets(a.Config); err != nil {
				return result, err
			}
			a.Config, err = normalizeQuery(a.Config, QueryConfig{}, p.Provider.URL, false)
			if err != nil {
				return result, err
			}
			p.Account = &a
		}
		p.Keys = append([]ConfigurationKey{}, p.Keys...)
		secrets := map[string]bool{}
		for i, k := range p.Keys {
			if !configurationIdentity(k.ID) || keys[k.ID] {
				return result, errors.New("Key 身份为空或重复")
			}
			keys[k.ID] = true
			k, err = normalizeConfigurationKey(k, p.Provider.URL, associated)
			if err != nil {
				return result, err
			}
			if k.Draft.Secret != "" && secrets[k.Draft.Secret] {
				return result, errors.New("同一供应商的完整密钥重复")
			}
			secrets[k.Draft.Secret] = true
			p.Keys[i] = k
		}
		result.Providers = append(result.Providers, p)
	}
	return result, nil
}

// normalizeConfigurationKey 校验查询来源、真实倍率和模型权限，不使用页面的留空保留秘密语义。
func normalizeConfigurationKey(k ConfigurationKey, base string, associated map[string]bool) (ConfigurationKey, error) {
	if k.Query != nil {
		q := *k.Query
		if err := validateConfigurationSecrets(q.Config); err != nil {
			return k, err
		}
		if q.NameSource == "" {
			q.NameSource = "manual"
		}
		if q.MultiplierSource == "" {
			q.MultiplierSource = "manual"
		}
		if q.NameSource != "manual" && q.NameSource != "upstream_key" && q.NameSource != "upstream_group" {
			return k, errors.New("无效名称来源")
		}
		if q.MultiplierSource != "manual" && q.MultiplierSource != "upstream" {
			return k, errors.New("无效倍率来源")
		}
		var err error
		q.Config, err = normalizeQuery(q.Config, QueryConfig{}, base, true)
		if err != nil {
			return k, err
		}
		if q.ReuseProvider {
			q.Config = QueryConfig{}
		}
		k.Query = &q
	}
	upstream := k.Query != nil && k.Query.MultiplierSource == "upstream"
	draft := k.Draft
	if upstream {
		if draft.Multiplier != nil && (!finite(*draft.Multiplier) || *draft.Multiplier < 0) {
			return k, errors.New("上游倍率必须为有限非负数")
		}
		placeholder := 0.0
		draft.Multiplier = &placeholder
	}
	doc, secret, err := draft.normalize()
	if err != nil {
		return k, err
	}
	if secret == "***" {
		return k, errors.New("配置包必须包含完整密钥，不能使用掩码")
	}
	if upstream {
		doc.Multiplier = k.Draft.Multiplier
	}
	k.Draft = ProviderKeyDraft{Name: doc.Name, Secret: secret, Note: doc.Note, Multiplier: doc.Multiplier, Priority: doc.Priority, RPMLimit: doc.RPMLimit, ConcurrencyLimit: doc.ConcurrencyLimit, CacheMinutes: doc.CacheMinutes, ProbeMinutes: doc.ProbeMinutes, Proxy: doc.Proxy}
	if k.AllModels && len(k.ModelIDs) > 0 {
		return k, errors.New("全部模型模式不能同时指定模型")
	}
	k.ModelIDs = append([]string{}, k.ModelIDs...)
	seen := map[string]bool{}
	for _, id := range k.ModelIDs {
		if !associated[id] || seen[id] {
			return k, errors.New("Key 权限引用未关联模型或包含重复模型")
		}
		seen[id] = true
	}
	return k, nil
}

// validateConfigurationSecrets 拒绝页面脱敏占位符，导入不复用任何旧认证填补掩码。
func validateConfigurationSecrets(c QueryConfig) error {
	for _, v := range []string{c.RefreshToken, c.AccessToken, c.APIKey, c.Headers, c.Body} {
		if strings.TrimSpace(v) == "***" {
			return errors.New("配置包查询认证包含掩码，请重新导出完整配置")
		}
	}
	if u, err := url.Parse(c.URL); err == nil {
		for _, values := range u.Query() {
			for _, v := range values {
				if v == "***" {
					return errors.New("配置包查询地址包含认证掩码")
				}
			}
		}
	}
	return nil
}
