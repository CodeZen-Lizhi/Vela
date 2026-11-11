package providers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// proxyCipherPrefix 标识仅存于数据库的代理 URL 密文，不接受客户端提交此表示。
const proxyCipherPrefix = "vela-proxy:v1:"

// proxyBinding 将代理密文绑定到资源类型及稳定身份，防止跨供应商或 Key 挪用。
func proxyBinding(kind, id string) string { return "provider-proxy:" + kind + ":" + id }

// maskProxy 对含认证或已加密代理隐藏整个 URL；无认证地址保持原样，不泄露用户名。
func maskProxy(p ProxyChoice) ProxyChoice {
	if strings.HasPrefix(p.URL, proxyCipherPrefix) {
		p.URL = "***"
		return p
	}
	if u, err := url.Parse(p.URL); err == nil && u.User != nil {
		p.URL = "***"
	}
	return p
}

// proxyForNetwork 只在后端解密代理；错误不含 URL 或密文，调用者不得把明文放回数据库或页面。
func (s *Store) proxyForNetwork(binding string, p ProxyChoice) (ProxyChoice, error) {
	if !strings.HasPrefix(p.URL, proxyCipherPrefix) {
		return p, nil
	}
	master, err := s.existingMasterKey()
	if err != nil {
		return ProxyChoice{}, err
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(p.URL, proxyCipherPrefix))
	if err != nil {
		return ProxyChoice{}, errors.New("代理认证数据已损坏，已保存的数据未被改动")
	}
	plain, err := open(master, binding, data)
	if err != nil {
		return ProxyChoice{}, errors.New("代理认证解密失败，已保存的数据未被改动")
	}
	p.URL = string(plain)
	return p, nil
}

// protectProxy 保留掩码或空输入对应的原代理，其余认证 URL 用 vault 加密；调用方持有 Store.mu。
// 已有密文须先验证主密钥可读，避免在主密钥丢失时重写或覆盖旧配置。
func (s *Store) protectProxy(ctx context.Context, binding string, p, old ProxyChoice) (ProxyChoice, error) {
	if strings.HasPrefix(old.URL, proxyCipherPrefix) {
		if _, err := s.proxyForNetwork(binding, old); err != nil {
			return ProxyChoice{}, err
		}
	}
	if p.Mode != ProxyCustom {
		return p, nil
	}
	if p.URL == "" || p.URL == "***" {
		if old.Mode != ProxyCustom || old.URL == "" {
			return ProxyChoice{}, errors.New("请填写自定义代理地址")
		}
		p = old
	}
	if strings.HasPrefix(p.URL, proxyCipherPrefix) {
		return p, nil
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return ProxyChoice{}, errors.New("代理地址无效")
	}
	if u.User == nil {
		return p, nil
	}
	master, err := s.sealingMasterKey(ctx)
	if err != nil {
		return ProxyChoice{}, err
	}
	encrypted, err := seal(master, binding, []byte(p.URL))
	if err != nil {
		return ProxyChoice{}, err
	}
	p.URL = proxyCipherPrefix + base64.RawStdEncoding.EncodeToString(encrypted)
	return p, nil
}

// migrateProxyCredentials 只迁移已有明文认证代理；先完成全部加密，再用事务一起更新，失败不改变旧文档。
// 删除旧单元内容时启用 SQLite 安全删除，避免认证残留于本次更新释放的数据库页。
func (s *Store) migrateProxyCredentials(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// updates 保存待迁移表、稳定身份和完整文档，避免单连接结果集未关闭就嵌套查询。
	type update struct {
		// table、id 指定固定资源表与已有身份；data 是保持其他字段不变的 JSON 文档。
		table, id string
		data      map[string]json.RawMessage
		// proxy 为迁移前的代理地址，用于识别并加密完整认证 URL。
		proxy ProxyChoice
	}
	updates := []update{}
	for _, table := range []string{"providers", "provider_keys"} {
		rows, err := s.db.QueryContext(ctx, "SELECT id,document FROM "+table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var data map[string]json.RawMessage
			if err = s.decodeDocument([]byte(raw), &data); err != nil {
				rows.Close()
				return err
			}
			proxyData := data["proxy"]
			if table == "providers" {
				var request map[string]json.RawMessage
				if err = json.Unmarshal(data["request"], &request); err != nil {
					rows.Close()
					return err
				}
				proxyData = request["proxy"]
			}
			var p ProxyChoice
			if len(proxyData) > 0 {
				if err = json.Unmarshal(proxyData, &p); err != nil {
					rows.Close()
					return err
				}
			}
			if p.Mode == ProxyCustom && p.URL != "***" && maskProxy(p).URL == "***" && !strings.HasPrefix(p.URL, proxyCipherPrefix) {
				updates = append(updates, update{table: table, id: id, data: data, proxy: p})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if len(updates) == 0 {
		return nil
	}
	// 新密钥生成前仍会检查全部旧代理密文，包含没有 Key 密钥或账户连接的独立供应商。
	documents := make([]string, 0, len(updates))
	for _, item := range updates {
		kind := "provider"
		if item.table == "provider_keys" {
			kind = "key"
		}
		p, err := s.protectProxy(ctx, proxyBinding(kind, item.id), item.proxy, ProxyChoice{})
		if err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if item.table == "providers" {
			var request map[string]json.RawMessage
			if err = json.Unmarshal(item.data["request"], &request); err != nil {
				return err
			}
			request["proxy"] = raw
			item.data["request"], err = json.Marshal(request)
			if err != nil {
				return err
			}
		} else {
			item.data["proxy"] = raw
		}
		data, err := s.encodeDocument(item.data)
		if err != nil {
			return err
		}
		documents = append(documents, string(data))
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, item := range updates {
		if _, err = tx.ExecContext(ctx, "UPDATE "+item.table+" SET document=? WHERE id=?", documents[i], item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// storedKeyProxy 读取 Key 原代理供更新时保留秘密；不存在时给出统一业务错误。
func (s *Store) storedKeyProxy(ctx context.Context, id string) (ProxyChoice, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM provider_keys WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyChoice{}, errKeyMissing
	}
	if err != nil {
		return ProxyChoice{}, err
	}
	var doc keyDocument
	if err = s.decodeDocument([]byte(raw), &doc); err != nil {
		return ProxyChoice{}, err
	}
	return doc.Proxy, nil
}

// hasProxyCredentials 在完整文档解密后检查代理密文，不能对已加密 document 使用 SQLite JSON 函数。
// 调用方持有 Store.mu；解密失败直接拒绝生成主密钥，保护已有密文。
func (s *Store) hasProxyCredentials(ctx context.Context) (bool, error) {
	for _, table := range []string{"providers", "provider_keys"} {
		rows, err := s.db.QueryContext(ctx, "SELECT document FROM "+table)
		if err != nil {
			return false, err
		}
		found := false
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return false, err
			}
			var doc map[string]json.RawMessage
			if err = s.decodeDocument([]byte(raw), &doc); err != nil {
				rows.Close()
				return false, err
			}
			var p ProxyChoice
			if table == "providers" {
				var request RequestPolicy
				if err = json.Unmarshal(doc["request"], &request); err != nil {
					rows.Close()
					return false, err
				}
				p = request.Proxy
			} else {
				if err = json.Unmarshal(doc["proxy"], &p); err != nil {
					rows.Close()
					return false, err
				}
			}
			found = found || strings.HasPrefix(p.URL, proxyCipherPrefix)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}
