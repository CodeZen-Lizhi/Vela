package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
)

// configurationImportProvider 保存写入前已完成匹配的供应商和从属 Key，防止包内多个身份合并到同一目的对象。
type configurationImportProvider struct {
	// source 是归一化的来源配置，id 是目的机器身份，exists 表示供应商原已存在。
	source ConfigurationProvider
	id     string
	exists bool
	// keys 与 source.Keys 同序，包含已完成查重的目的身份。
	keys []configurationImportKey
}

// configurationImportKey 保存 Key 的目的身份及匹配状态；来源稳定身份或同供应商完整秘密可命中既有 Key。
type configurationImportKey struct {
	// id 是目的机器身份，exists 表示 Key 原已存在。
	id     string
	exists bool
}

// ImportConfiguration 在整包事务中导入配置，返回来源供应商身份到本机身份的映射。
// 调用方必须通过 ConfigurationTransaction 持有写锁并准备本机主密钥；模型先在同一 tx 中导入。
// overwrite 覆盖匹配配置；false 保留匹配配置并追加新 Key 和关联。两者均不删除本机独有主体或历史。
func (s *Store) ImportConfiguration(ctx context.Context, tx *sql.Tx, value Configuration, modelIDs map[string]string, overwrite bool) (map[string]string, error) {
	if tx == nil {
		return nil, errors.New("配置导入需要事务")
	}
	sourceModels := make(map[string]bool, len(modelIDs))
	for id := range modelIDs {
		sourceModels[id] = true
	}
	value, err := normalizeConfiguration(value, sourceModels)
	if err != nil {
		return nil, err
	}
	if err = validateConfigurationModelTargets(ctx, tx, value, modelIDs); err != nil {
		return nil, err
	}
	local, err := s.ExportConfiguration(ctx, tx)
	if err != nil {
		return nil, err
	}
	plan, err := planConfigurationImport(value, local)
	if err != nil {
		return nil, err
	}
	// 初始化从属表也属于整包事务，不在来源校验失败时留下半包配置。
	if _, err = tx.ExecContext(ctx, modelSchema+accountSchema); err != nil {
		return nil, err
	}
	master, err := s.existingMasterKey()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(plan))
	for _, p := range plan {
		if err = s.importConfigurationProvider(ctx, tx, p, modelIDs, overwrite, master); err != nil {
			return nil, err
		}
		result[p.source.Provider.ID] = p.id
	}
	return result, nil
}

// validateConfigurationModelTargets 确认所有来源关联映射到不同且已在当前事务落库的目的模型。
func validateConfigurationModelTargets(ctx context.Context, tx *sql.Tx, value Configuration, modelIDs map[string]string) error {
	seen := map[string]string{}
	for _, p := range value.Providers {
		for _, m := range p.Models {
			id := modelIDs[m.ModelID]
			if !configurationIdentity(id) {
				return errors.New("缺少模型身份映射")
			}
			if previous := seen[id]; previous != "" {
				if previous != m.ModelID {
					return errors.New("不同来源模型不能映射到同一目的模型")
				}
				continue
			}
			seen[id] = m.ModelID
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM models WHERE id=?)", id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errors.New("模型身份映射引用不存在的目的模型")
			}
		}
	}
	return nil
}

// planConfigurationImport 在任何配置写入前解析全部身份，拒绝 ID 与名称或完整密钥分别命中不同对象。
func planConfigurationImport(value, local Configuration) ([]configurationImportProvider, error) {
	byID, byName := map[string]ConfigurationProvider{}, map[string]ConfigurationProvider{}
	keyOwners := map[string]string{}
	for _, p := range local.Providers {
		byID[p.Provider.ID], byName[p.Provider.Name] = p, p
		for _, k := range p.Keys {
			keyOwners[k.ID] = p.Provider.ID
		}
	}
	result := make([]configurationImportProvider, 0, len(value.Providers))
	usedProviders, usedKeys := map[string]bool{}, map[string]bool{}
	for _, p := range value.Providers {
		matched, idMatch := byID[p.Provider.ID]
		named, nameMatch := byName[p.Provider.Name]
		if idMatch && nameMatch && matched.Provider.ID != named.Provider.ID {
			return nil, errors.New("供应商稳定身份与名称分别匹配不同本机供应商")
		}
		if !idMatch && nameMatch {
			matched = named
		}
		item := configurationImportProvider{source: p, id: p.Provider.ID, exists: idMatch || nameMatch}
		if item.exists {
			item.id = matched.Provider.ID
		}
		if usedProviders[item.id] {
			return nil, errors.New("多个来源供应商匹配同一本机供应商")
		}
		usedProviders[item.id] = true
		for _, k := range p.Keys {
			if owner := keyOwners[k.ID]; owner != "" && owner != item.id {
				return nil, errors.New("Key 稳定身份属于其他本机供应商")
			}
			key, err := matchConfigurationKey(k, matched.Keys)
			if err != nil {
				return nil, err
			}
			if usedKeys[key.id] {
				return nil, errors.New("多个来源 Key 匹配同一本机 Key")
			}
			usedKeys[key.id] = true
			item.keys = append(item.keys, key)
		}
		result = append(result, item)
	}
	return result, nil
}

// matchConfigurationKey 在同供应商下按稳定身份或完整秘密匹配，绝不按掩码或可重复名称匹配。
func matchConfigurationKey(source ConfigurationKey, local []ConfigurationKey) (configurationImportKey, error) {
	var idMatch, secretMatch string
	for _, k := range local {
		if k.ID == source.ID {
			idMatch = k.ID
		}
		if source.Draft.Secret != "" && k.Draft.Secret == source.Draft.Secret {
			if secretMatch != "" && secretMatch != k.ID {
				return configurationImportKey{}, errors.New("本机完整密钥重复，无法唯一匹配")
			}
			secretMatch = k.ID
		}
	}
	if idMatch != "" && secretMatch != "" && idMatch != secretMatch {
		return configurationImportKey{}, errors.New("Key 稳定身份与完整密钥分别匹配不同本机 Key")
	}
	if idMatch != "" {
		return configurationImportKey{id: idMatch, exists: true}, nil
	}
	if secretMatch != "" {
		return configurationImportKey{id: secretMatch, exists: true}, nil
	}
	return configurationImportKey{id: source.ID}, nil
}

// importConfigurationProvider 写入一项已校验供应商及其从属配置，所有查询都使用调用方事务。
func (s *Store) importConfigurationProvider(ctx context.Context, tx *sql.Tx, p configurationImportProvider, modelIDs map[string]string, overwrite bool, master []byte) error {
	if !p.exists || overwrite {
		v := p.source.Provider
		data, err := s.encodeDocument(providerDocument{Website: v.Website, URL: v.URL, Request: v.Request})
		if err != nil {
			return err
		}
		if !p.exists {
			_, err = tx.ExecContext(ctx, "INSERT INTO providers(id,name,enabled,document) VALUES(?,?,?,?)", p.id, v.Name, v.Enabled, string(data))
		} else {
			var previousName string
			if err = tx.QueryRowContext(ctx, "SELECT name FROM providers WHERE id=?", p.id).Scan(&previousName); err != nil {
				return err
			}
			// 同名匹配不写 name 列，避免无意义地触发历史汇总展示名称同步。
			if previousName == v.Name {
				_, err = tx.ExecContext(ctx, "UPDATE providers SET enabled=?,document=? WHERE id=?", v.Enabled, string(data), p.id)
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE providers SET name=?,enabled=?,document=? WHERE id=?", v.Name, v.Enabled, string(data), p.id)
			}
		}
		if err != nil {
			return err
		}
	}
	for _, m := range p.source.Models {
		if err := importConfigurationModel(ctx, tx, p.id, modelIDs[m.ModelID], m, overwrite); err != nil {
			return err
		}
	}
	if p.source.Account != nil {
		if err := s.importConfigurationAccount(ctx, tx, p.id, *p.source.Account, overwrite, master); err != nil {
			return err
		}
	}
	for i, k := range p.source.Keys {
		if p.keys[i].exists && !overwrite {
			if k.Query != nil {
				if err := s.importConfigurationKeyQuery(ctx, tx, p.keys[i].id, *k.Query, false, master); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.importConfigurationKey(ctx, tx, p.id, p.keys[i].id, k, modelIDs, master); err != nil {
			return err
		}
	}
	return nil
}

// importConfigurationModel 按模式保存关联启停，映射按请求名逐项合并，两种模式均保留本机独有请求名。
func importConfigurationModel(ctx context.Context, tx *sql.Tx, providerID, modelID string, m ConfigurationModel, overwrite bool) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_models WHERE provider_id=? AND model_id=?)", providerID, modelID).Scan(&exists); err != nil {
		return err
	}
	if !exists || overwrite {
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_models(provider_id,model_id,enabled) VALUES(?,?,?) ON CONFLICT(provider_id,model_id) DO UPDATE SET enabled=excluded.enabled`, providerID, modelID, m.Enabled); err != nil {
			return err
		}
	}
	return importConfigurationMappings(ctx, tx, providerID, modelID, m.Mappings, overwrite)
}

// importConfigurationMappings 保留本机顺序，按请求名覆盖或保留匹配项，再按来源顺序追加新项；空来源不删除映射。
func importConfigurationMappings(ctx context.Context, tx *sql.Tx, providerID, modelID string, incoming []ProviderModelMapping, overwrite bool) error {
	if len(incoming) == 0 {
		return nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT document FROM provider_model_mappings WHERE provider_id=? AND model_id=?", providerID, modelID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var current []ProviderModelMapping
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &current); err != nil {
			return errors.New("本机模型映射配置损坏")
		}
	}
	current, err = normalizeModelMappings(current)
	if err != nil {
		return err
	}
	indices := make(map[string]int, len(current))
	for i, item := range current {
		indices[item.RequestName] = i
	}
	for _, item := range incoming {
		if index, exists := indices[item.RequestName]; exists {
			if overwrite {
				current[index] = item
			}
		} else {
			indices[item.RequestName] = len(current)
			current = append(current, item)
		}
	}
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_model_mappings(provider_id,model_id,document) VALUES(?,?,?) ON CONFLICT(provider_id,model_id) DO UPDATE SET document=excluded.document`, providerID, modelID, string(data))
	return err
}

// importConfigurationKey 保留本机 Key 身份，使用本机主密钥重新加密来源秘密，并替换匹配 Key 的配置与权限。
func (s *Store) importConfigurationKey(ctx context.Context, tx *sql.Tx, providerID, id string, k ConfigurationKey, modelIDs map[string]string, master []byte) error {
	d := k.Draft
	doc := keyDocument{Name: d.Name, Note: d.Note, Multiplier: d.Multiplier, Priority: d.Priority, RPMLimit: d.RPMLimit, ConcurrencyLimit: d.ConcurrencyLimit, CacheMinutes: d.CacheMinutes, ProbeMinutes: d.ProbeMinutes, Proxy: d.Proxy}
	data, err := s.encodeDocument(doc)
	if err != nil {
		return err
	}
	var sealed []byte
	if d.Secret != "" {
		err = tx.QueryRowContext(ctx, "SELECT secret FROM provider_keys WHERE id=?", id).Scan(&sealed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var previous []byte
		if sealed != nil {
			previous, err = open(master, secretBinding(id), sealed)
			if err != nil {
				return err
			}
		}
		// 相同秘密保留本机密文，避免幂等导入改变执行版本、重置本机健康；新秘密仍按目的身份重新加密。
		if string(previous) != d.Secret {
			sealed, err = seal(master, secretBinding(id), []byte(d.Secret))
			if err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_keys(id,provider_id,enabled,all_models,secret,document) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,all_models=excluded.all_models,secret=excluded.secret,document=excluded.document`, id, providerID, k.Enabled, k.AllModels, secretColumn(sealed), string(data)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM provider_key_models WHERE key_id=?", id); err != nil {
		return err
	}
	for _, sourceID := range k.ModelIDs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO provider_key_models(key_id,provider_id,model_id) VALUES(?,?,?)", id, providerID, modelIDs[sourceID]); err != nil {
			return err
		}
	}
	if k.Query != nil {
		return s.importConfigurationKeyQuery(ctx, tx, id, *k.Query, true, master)
	}
	return nil
}

// importConfigurationAccount 更新连接和配额时保留本机余额结果；新账户的远端结果初始为未知。
func (s *Store) importConfigurationAccount(ctx context.Context, tx *sql.Tx, id string, a ConfigurationAccount, overwrite bool, master []byte) error {
	status := QueryStatus{State: "unknown"}
	account := ProviderAccount{ProviderID: id, Balance: AccountBalance{Status: status, SubscriptionStatus: status, Checkin: status}}
	_, exists, err := s.exportConfigurationQuery(ctx, tx, "provider_accounts", id, &account)
	if err != nil || (exists && !overwrite) {
		return err
	}
	account.ProviderID, account.Config, account.MonthlyQuota, account.Checkin = id, QueryConfig{}, a.MonthlyQuota, a.Checkin
	return writeConfigurationQuery(ctx, tx, "provider_accounts", id, a.Config, account, master)
}

// importConfigurationKeyQuery 按模式保留或替换已有查询，缺失时新增完整认证；保留本机分组和同步状态。
func (s *Store) importConfigurationKeyQuery(ctx context.Context, tx *sql.Tx, id string, q ProviderKeyQueryDraft, overwrite bool, master []byte) error {
	status := ProviderKeyQuery{NameStatus: QueryStatus{State: "unknown"}, MultiplierStatus: QueryStatus{State: "unknown"}}
	_, exists, err := s.exportConfigurationQuery(ctx, tx, "provider_key_queries", id, &status)
	if err != nil || (exists && !overwrite) {
		return err
	}
	status.KeyID, status.Config = id, QueryConfig{}
	status.ReuseProvider, status.NameSource, status.MultiplierSource = q.ReuseProvider, q.NameSource, q.MultiplierSource
	status.NamePath, status.GroupPath, status.MultiplierPath, status.KeyPath = q.NamePath, q.GroupPath, q.MultiplierPath, q.KeyPath
	return writeConfigurationQuery(ctx, tx, "provider_key_queries", id, q.Config, status, master)
}

// writeConfigurationQuery 在当前事务用目的身份绑定密文，并更新配置世代以拒绝导入前正在运行的查询写回。
func writeConfigurationQuery(ctx context.Context, tx *sql.Tx, table, id string, config QueryConfig, value any, master []byte) error {
	column := "provider_id"
	if table == "provider_key_queries" {
		column = "key_id"
	} else if table != "provider_accounts" {
		return errors.New("无效账户表")
	}
	plain, err := json.Marshal(config)
	if err != nil {
		return err
	}
	sealed, err := seal(master, secretBinding(table+":"+id), plain)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO "+table+"("+column+",secret,document,revision) VALUES(?,?,?,?) ON CONFLICT("+column+") DO UPDATE SET secret=excluded.secret,document=excluded.document,revision=excluded.revision", id, sealed, string(doc), rand.Text())
	return err
}
