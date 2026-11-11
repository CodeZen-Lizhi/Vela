package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// keyDocument 是 provider_keys.document 列保存的内容；身份、所属供应商、启停、模型范围与密钥在独立列中。
type keyDocument struct {
	// 各字段与 ProviderKey 的同名字段含义相同。
	Name             string      `json:"name"`
	Note             string      `json:"note"`
	Multiplier       *float64    `json:"multiplier"`
	Priority         int         `json:"priority"`
	RPMLimit         int         `json:"rpmLimit"`
	ConcurrencyLimit int         `json:"concurrencyLimit"`
	CacheMinutes     int         `json:"cacheMinutes"`
	ProbeMinutes     int         `json:"probeMinutes"`
	Proxy            ProxyChoice `json:"proxy"`
}

// errKeyMissing 是操作的上游 Key 已不存在时的提示。
var errKeyMissing = errors.New("上游 Key 不存在，请刷新列表")

// normalize 校验草稿并返回归一化后的文档与去掉首尾空白的密钥；任何一项不合法都不写入。
func (d ProviderKeyDraft) normalize() (keyDocument, string, error) {
	name := strings.TrimSpace(d.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return keyDocument{}, "", errors.New("Key 名称需为 1～100 个字符")
	}
	switch {
	case d.Multiplier == nil:
		return keyDocument{}, "", errors.New("请填写倍率")
	case math.IsNaN(*d.Multiplier) || *d.Multiplier < 0 || *d.Multiplier > 100:
		return keyDocument{}, "", errors.New("倍率需在 0～100 之间")
	case d.Priority < 0:
		return keyDocument{}, "", errors.New("优先级需为非负整数")
	case d.RPMLimit < 0 || d.RPMLimit > 10000:
		return keyDocument{}, "", errors.New("RPM 限制需为 1～10000 的整数，留空表示不限制")
	case d.ConcurrencyLimit < 0:
		return keyDocument{}, "", errors.New("并发上限需为非负整数")
	case d.CacheMinutes < 0 || d.CacheMinutes > 60:
		return keyDocument{}, "", errors.New("缓存 TTL 需为 0～60 分钟")
	case d.ProbeMinutes < 0 || d.ProbeMinutes > 32:
		return keyDocument{}, "", errors.New("熔断探测间隔需为 0～32 分钟")
	}
	proxy, err := normalizeProxy(d.Proxy, true)
	if err != nil {
		return keyDocument{}, "", err
	}
	multiplier := *d.Multiplier
	return keyDocument{
		Name:             name,
		Note:             strings.TrimSpace(d.Note),
		Multiplier:       &multiplier,
		Priority:         d.Priority,
		RPMLimit:         d.RPMLimit,
		ConcurrencyLimit: d.ConcurrencyLimit,
		CacheMinutes:     d.CacheMinutes,
		ProbeMinutes:     d.ProbeMinutes,
		Proxy:            proxy,
	}, strings.TrimSpace(d.Secret), nil
}

// maskSecret 返回密钥掩码：超过 12 个字符时保留前 8 位与后 4 位，否则全部隐藏。
func maskSecret(secret string) string {
	runes := []rune(secret)
	if len(runes) > 12 {
		return string(runes[:8]) + "***" + string(runes[len(runes)-4:])
	}
	return "***"
}

// secretBinding 把密文绑定到所属的 Key，密文被挪到其他记录时无法解密。
func secretBinding(keyID string) string { return "provider-key:" + keyID }

// secretColumn 把密文转成写入 secret 列的参数：未设置密钥时写入 NULL，而不是长度为零的内容。
func secretColumn(sealed []byte) any {
	if sealed == nil {
		return nil
	}
	return sealed
}

// key 由独立列、文档和掩码组装对外的 Key 信息。
func (doc keyDocument) key(id, providerID string, enabled, allModels bool, mask string, hasSecret bool) ProviderKey {
	return ProviderKey{
		ID:               id,
		ProviderID:       providerID,
		Name:             doc.Name,
		Note:             doc.Note,
		Enabled:          enabled,
		HasSecret:        hasSecret,
		SecretMask:       mask,
		Multiplier:       doc.Multiplier,
		Priority:         doc.Priority,
		RPMLimit:         doc.RPMLimit,
		ConcurrencyLimit: doc.ConcurrencyLimit,
		CacheMinutes:     doc.CacheMinutes,
		ProbeMinutes:     doc.ProbeMinutes,
		Proxy:            doc.Proxy,
		AllModels:        allModels,
		HealthScore:      initialKeyHealth,
	}
}

// keyRow 是从数据库读出的一把 Key，密钥仍是密文。
type keyRow struct {
	// id、providerID、enabled、allModels 来自独立列；sealed 为 nil 表示未设置密钥；doc 是解码后的文档。
	id, providerID     string
	enabled, allModels bool
	sealed             []byte
	doc                keyDocument
}

// keyRows 读取一个供应商的全部 Key，按创建顺序排列；读完即关闭结果集，调用方可以继续使用数据库。
func (s *Store) keyRows(ctx context.Context, providerID string) ([]keyRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, enabled, all_models, secret, document FROM provider_keys WHERE provider_id=? ORDER BY seq", providerID)
	if err != nil {
		return nil, fmt.Errorf("读取上游 Key: %w", err)
	}
	defer rows.Close()
	var result []keyRow
	for rows.Next() {
		row := keyRow{providerID: providerID}
		var data string
		if err = rows.Scan(&row.id, &row.enabled, &row.allModels, &row.sealed, &data); err != nil {
			return nil, err
		}
		if err = s.decodeDocument([]byte(data), &row.doc); err != nil {
			return nil, fmt.Errorf("上游 Key 的数据已损坏: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// existingMasterKey 返回解密用的主密钥，从不生成新密钥：密文已经存在，找不到主密钥只能报错。
func (s *Store) existingMasterKey() ([]byte, error) {
	key, err := s.vault.load()
	if errors.Is(err, ErrNoMasterKey) {
		return nil, errMasterKeyMissing
	}
	return key, err
}

// sealingMasterKey 返回加密用的主密钥。本地存储中没有主密钥时，只有数据库里也没有任何密文才生成新的；
// 否则报错并保持原样，避免新密钥让已保存的凭据再也无法解密。调用方须持有 s.mu。
func (s *Store) sealingMasterKey(ctx context.Context) ([]byte, error) {
	key, err := s.vault.load()
	if !errors.Is(err, ErrNoMasterKey) {
		return key, err
	}
	var sealed bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM provider_keys WHERE secret IS NOT NULL)").Scan(&sealed); err != nil {
		return nil, fmt.Errorf("检查已保存的凭据: %w", err)
	}
	if sealed {
		return nil, errMasterKeyMissing
	}
	return s.vault.generate()
}

// revealed 解密一把 Key 的密钥；未设置密钥时返回空字符串。
func (row keyRow) revealed(master []byte) (string, error) {
	if row.sealed == nil {
		return "", nil
	}
	plaintext, err := open(master, secretBinding(row.id), row.sealed)
	return string(plaintext), err
}

// Keys 读取一个供应商的全部上游 Key，按创建顺序排列，没有 Key 时返回空数组。
// 只返回密钥掩码；任意一把 Key 设置了密钥而主密钥不可用时返回错误，不返回残缺的列表。
func (s *Store) Keys(ctx context.Context, providerID string) ([]ProviderKey, error) {
	rows, err := s.keyRows(ctx, providerID)
	if err != nil {
		return nil, err
	}
	result := []ProviderKey{}
	var master []byte
	for _, row := range rows {
		mask := ""
		if row.sealed != nil {
			if master == nil {
				if master, err = s.existingMasterKey(); err != nil {
					return nil, err
				}
			}
			secret, err := row.revealed(master)
			if err != nil {
				return nil, err
			}
			mask = maskSecret(secret)
		}
		result = append(result, row.doc.key(row.id, providerID, row.enabled, row.allModels, mask, row.sealed != nil))
	}
	return result, nil
}

// sealUnique 加密新密钥，并确认同一供应商的其他 Key 没有使用相同的密钥。
// exceptID 是正在编辑的 Key，新增时为空；调用方须持有 s.mu。
func (s *Store) sealUnique(ctx context.Context, providerID, keyID, exceptID, secret string) ([]byte, error) {
	master, err := s.sealingMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.keyRows(ctx, providerID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.id == exceptID {
			continue
		}
		existing, err := row.revealed(master)
		if err != nil {
			return nil, err
		}
		if row.sealed != nil && existing == secret {
			return nil, fmt.Errorf("该密钥已存在于当前供应商（名称：%s）", row.doc.Name)
		}
	}
	return seal(master, secretBinding(keyID), []byte(secret))
}

// CreateKey 为供应商新增一把上游 Key：默认启用、允许全部模型。密钥可以留空，填写时加密保存。
func (s *Store) CreateKey(ctx context.Context, providerID string, draft ProviderKeyDraft) (ProviderKey, error) {
	doc, secret, err := draft.normalize()
	if err != nil {
		return ProviderKey{}, err
	}
	data, err := s.encodeDocument(doc)
	if err != nil {
		return ProviderKey{}, err
	}
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE id=?)", providerID).Scan(&exists); err != nil {
		return ProviderKey{}, fmt.Errorf("读取供应商: %w", err)
	}
	if !exists {
		return ProviderKey{}, errProviderMissing
	}
	var sealed []byte
	mask := ""
	if secret != "" {
		if sealed, err = s.sealUnique(ctx, providerID, id, "", secret); err != nil {
			return ProviderKey{}, err
		}
		mask = maskSecret(secret)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO provider_keys(id,provider_id,enabled,all_models,secret,document) VALUES(?,?,1,1,?,?)", id, providerID, secretColumn(sealed), string(data)); err != nil {
		return ProviderKey{}, fmt.Errorf("保存上游 Key: %w", err)
	}
	return doc.key(id, providerID, true, true, mask, sealed != nil), nil
}

// UpdateKey 保存一把上游 Key 的配置，不改变启停与模型范围。草稿中的密钥为空时保留原密钥，否则替换。
func (s *Store) UpdateKey(ctx context.Context, keyID string, draft ProviderKeyDraft) (ProviderKey, error) {
	doc, secret, err := draft.normalize()
	if err != nil {
		return ProviderKey{}, err
	}
	data, err := s.encodeDocument(doc)
	if err != nil {
		return ProviderKey{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := keyRow{id: keyID}
	err = s.db.QueryRowContext(ctx, "SELECT provider_id, enabled, all_models, secret FROM provider_keys WHERE id=?", keyID).Scan(&row.providerID, &row.enabled, &row.allModels, &row.sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderKey{}, errKeyMissing
	}
	if err != nil {
		return ProviderKey{}, fmt.Errorf("读取上游 Key: %w", err)
	}
	mask := ""
	if secret != "" {
		if row.sealed, err = s.sealUnique(ctx, row.providerID, keyID, keyID, secret); err != nil {
			return ProviderKey{}, err
		}
		mask = maskSecret(secret)
	} else if row.sealed != nil {
		master, err := s.existingMasterKey()
		if err != nil {
			return ProviderKey{}, err
		}
		existing, err := row.revealed(master)
		if err != nil {
			return ProviderKey{}, err
		}
		mask = maskSecret(existing)
	}
	result, err := s.db.ExecContext(ctx, "UPDATE provider_keys SET secret=?, document=? WHERE id=?", secretColumn(row.sealed), string(data), keyID)
	if err != nil {
		return ProviderKey{}, fmt.Errorf("保存上游 Key: %w", err)
	}
	if err = affected(result, errKeyMissing); err != nil {
		return ProviderKey{}, err
	}
	return doc.key(keyID, row.providerID, row.enabled, row.allModels, mask, row.sealed != nil), nil
}

// SetKeyEnabled 启用或停用一把上游 Key。
func (s *Store) SetKeyEnabled(ctx context.Context, keyID string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "UPDATE provider_keys SET enabled=? WHERE id=?", enabled, keyID)
	if err != nil {
		return fmt.Errorf("保存上游 Key 状态: %w", err)
	}
	return affected(result, errKeyMissing)
}

// DeleteKey 删除一把上游 Key 及其密钥。
func (s *Store) DeleteKey(ctx context.Context, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM provider_keys WHERE id=?", keyID)
	if err != nil {
		return fmt.Errorf("删除上游 Key: %w", err)
	}
	return affected(result, errKeyMissing)
}

// Secret 返回一把上游 Key 的密钥明文，供用户主动复制；调用方不得把它交给页面或写入日志。
func (s *Store) Secret(ctx context.Context, keyID string) (string, error) {
	row := keyRow{id: keyID}
	err := s.db.QueryRowContext(ctx, "SELECT secret FROM provider_keys WHERE id=?", keyID).Scan(&row.sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errKeyMissing
	}
	if err != nil {
		return "", fmt.Errorf("读取上游 Key: %w", err)
	}
	if row.sealed == nil {
		return "", errors.New("这把 Key 尚未设置密钥")
	}
	master, err := s.existingMasterKey()
	if err != nil {
		return "", err
	}
	return row.revealed(master)
}
