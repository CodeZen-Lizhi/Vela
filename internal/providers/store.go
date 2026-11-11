package providers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/outbound"
)

// schema 是本模块的表结构。删除供应商或 Key 时，外键级联在同一条语句内清掉从属数据，不留下指向已删除记录的行。
const schema = `
CREATE TABLE IF NOT EXISTS providers (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, -- 创建顺序；调度模块提供顺位前，列表按它排序
  id TEXT NOT NULL UNIQUE,               -- 稳定身份
  name TEXT NOT NULL UNIQUE,             -- 名称，全局唯一且区分大小写
  enabled INTEGER NOT NULL,              -- 1 启用，0 停用
  document TEXT NOT NULL                 -- 主站链接、服务地址、备注与请求配置（JSON）
);
CREATE TABLE IF NOT EXISTS provider_keys (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, -- 创建顺序；Key 列表按它升序排列
  id TEXT NOT NULL UNIQUE,               -- 稳定身份
  provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE, -- 所属供应商，随供应商删除
  enabled INTEGER NOT NULL,              -- 1 启用，0 停用
  all_models INTEGER NOT NULL DEFAULT 1, -- 1 允许该供应商的全部模型，0 只允许指定模型
  secret BLOB,                           -- 加密后的密钥；NULL 表示未设置
  document TEXT NOT NULL                 -- 名称、备注、倍率、优先级、限制与出口选择（JSON）
);
CREATE INDEX IF NOT EXISTS provider_keys_provider ON provider_keys(provider_id);
`

// Store 维护供应商与上游 Key 的本地配置。它与模型模块共用同一个 SQLite 数据库，数据库的打开与关闭由入口负责。
type Store struct {
	// db 是共用的数据库句柄；mu 串行化写操作，保证“先检查再写入”的步骤不被并发调用打断。
	db *sql.DB
	mu sync.Mutex
	// vault 加解密上游密钥。
	vault *vault
	// documents 在桌面环境加密包含代理认证信息的配置文档。
	documents *credentials.Store
}

// New 复用已打开的数据库并建表；keys 保存上游主密钥，documents 在桌面环境迁移并加密含代理凭据的文档。
// 本模块依赖外键级联清理从属数据，数据库连接必须已启用外键约束，否则拒绝启动。
func New(db *sql.DB, keys KeyStore, documents ...*credentials.Store) (*Store, error) {
	var enforced int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enforced); err != nil {
		return nil, fmt.Errorf("检查数据库外键约束: %w", err)
	}
	if enforced != 1 {
		return nil, errors.New("数据库连接未启用外键约束，无法保证删除供应商时清理其 Key 与关联")
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("初始化供应商数据: %w", err)
	}
	s := &Store{db: db, vault: &vault{store: keys}}
	if len(documents) > 0 && documents[0] != nil {
		if err := s.protectDocuments(documents[0]); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// providerDocument 是 providers.document 列保存的内容；身份、名称与启停在独立列中。
type providerDocument struct {
	// Website、URL、Note、Request 与 Provider 的同名字段含义相同。
	Website string        `json:"website"`
	URL     string        `json:"url"`
	Note    string        `json:"note"`
	Request RequestPolicy `json:"request"`
}

// errProviderMissing 是操作的供应商已不存在时的提示。
var errProviderMissing = errors.New("供应商不存在，请刷新列表")

// normalizeWebsite 归一化主站链接：允许为空；未写协议时按 https 处理；只接受不带账号密码的 http/https 地址，并去掉末尾斜杠。
func normalizeWebsite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("主站链接需要有效的 http 或 https 地址")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// normalizeServiceURL 把服务地址归一化为站点根地址。四种 API 格式共用一个站点，
// 因此只接受根地址或带 /v1、/v1beta 后缀的写法，保存时去掉后缀。
func normalizeServiceURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("请填写服务地址")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("服务地址需要以 http:// 或 https:// 开头，且不包含账号密码")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("服务地址不能包含查询参数或 # 片段")
	}
	if path := strings.TrimRight(u.Path, "/"); path != "" && path != "/v1" && path != "/v1beta" {
		return "", errors.New("服务地址只需填写站点根地址，例如 https://api.example.com")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// normalizeProxy 校验出口选择并清理无关字段；只有上游 Key 可以选择跟随所属供应商。
func normalizeProxy(choice ProxyChoice, forKey bool) (ProxyChoice, error) {
	modes := []ProxyMode{ProxyGlobal, ProxyDirect, ProxyCustom}
	if forKey {
		modes = append(modes, ProxyProvider)
	}
	if !slices.Contains(modes, choice.Mode) {
		return ProxyChoice{}, errors.New("请选择网络代理方式")
	}
	if choice.Mode != ProxyCustom {
		return ProxyChoice{Mode: choice.Mode}, nil
	}
	choice.URL = strings.TrimSpace(choice.URL)
	if _, err := outbound.ParseProxy(choice.URL); err != nil {
		return ProxyChoice{}, err
	}
	return choice, nil
}

// normalizeRequest 校验请求配置；数值为 0 表示使用默认值或不限制，其余必须落在各自范围内。
func normalizeRequest(p RequestPolicy) (RequestPolicy, error) {
	switch {
	case p.Attempts != 0 && (p.Attempts < 1 || p.Attempts > 99):
		return p, errors.New("总尝试次数需为 1～99 的整数")
	case p.StreamTotalMillis != 0 && (p.StreamTotalMillis < 1000 || p.StreamTotalMillis > 1_200_000):
		return p, errors.New("流式请求总超时需在 1～1200 秒之间")
	case p.FirstByteSeconds != 0 && (p.FirstByteSeconds < 1 || p.FirstByteSeconds > 300):
		return p, errors.New("首次响应超时需为 1～300 秒的整数")
	case p.RequestSeconds != 0 && (p.RequestSeconds < 1 || p.RequestSeconds > 1200):
		return p, errors.New("非流式请求超时需为 1～1200 秒的整数")
	case p.MaxTransfers < 0 || p.MaxTransferSeconds < 0:
		return p, errors.New("最大转移次数和最大转移超时不能为负数")
	}
	proxy, err := normalizeProxy(p.Proxy, false)
	if err != nil {
		return p, err
	}
	p.Proxy = proxy
	return p, nil
}

// normalize 校验草稿并返回归一化后的名称与文档；任何一项不合法都不写入。
func (d ProviderDraft) normalize() (string, providerDocument, error) {
	name := strings.TrimSpace(d.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return "", providerDocument{}, errors.New("供应商名称需为 1～80 个字符")
	}
	website, err := normalizeWebsite(d.Website)
	if err != nil {
		return "", providerDocument{}, err
	}
	serviceURL, err := normalizeServiceURL(d.URL)
	if err != nil {
		return "", providerDocument{}, err
	}
	request, err := normalizeRequest(d.Request)
	if err != nil {
		return "", providerDocument{}, err
	}
	return name, providerDocument{Website: website, URL: serviceURL, Note: strings.TrimSpace(d.Note), Request: request}, nil
}

// provider 由独立列和文档组装对外的供应商配置。
func (doc providerDocument) provider(id, name string, enabled bool) Provider {
	return Provider{ID: id, Name: name, Website: doc.Website, URL: doc.URL, Note: doc.Note, Enabled: enabled, Request: doc.Request}
}

// saveError 把写入失败转成用户可读的原因：名称被其他供应商占用时指出重名，其余保留原始错误。
func (s *Store) saveError(ctx context.Context, name, id string, cause error) error {
	var taken bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE name=? AND id<>?)", name, id).Scan(&taken); err == nil && taken {
		return fmt.Errorf("供应商名称「%s」已存在", name)
	}
	return fmt.Errorf("保存供应商: %w", cause)
}

// Create 新增供应商，默认启用；桌面环境加密配置文档，名称重复或字段不合法时不写入。
func (s *Store) Create(ctx context.Context, draft ProviderDraft) (Provider, error) {
	name, doc, err := draft.normalize()
	if err != nil {
		return Provider{}, err
	}
	data, err := s.encodeDocument(doc)
	if err != nil {
		return Provider{}, err
	}
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.db.ExecContext(ctx, "INSERT INTO providers(id,name,enabled,document) VALUES(?,?,1,?)", id, name, string(data)); err != nil {
		return Provider{}, s.saveError(ctx, name, "", err)
	}
	return doc.provider(id, name, true), nil
}

// Update 加密保存桌面供应商配置，不改变启停状态；供应商不存在、名称重复或字段不合法时不写入。
func (s *Store) Update(ctx context.Context, id string, draft ProviderDraft) (Provider, error) {
	name, doc, err := draft.normalize()
	if err != nil {
		return Provider{}, err
	}
	data, err := s.encodeDocument(doc)
	if err != nil {
		return Provider{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var enabled bool
	err = s.db.QueryRowContext(ctx, "UPDATE providers SET name=?, document=? WHERE id=? RETURNING enabled", name, string(data), id).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, errProviderMissing
	}
	if err != nil {
		return Provider{}, s.saveError(ctx, name, id, err)
	}
	return doc.provider(id, name, enabled), nil
}

// affected 确认写操作命中了记录；没有命中时返回 missing。
func affected(result sql.Result, missing error) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return missing
	}
	return nil
}

// SetEnabled 启用或停用供应商。停用只让它退出调用候选，Key 与其他配置保留。
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "UPDATE providers SET enabled=? WHERE id=?", enabled, id)
	if err != nil {
		return fmt.Errorf("保存供应商状态: %w", err)
	}
	return affected(result, errProviderMissing)
}

// Delete 删除供应商；它的全部 Key 与凭据由外键级联在同一条语句内删除。
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM providers WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("删除供应商: %w", err)
	}
	return affected(result, errProviderMissing)
}

// keyBrief 是汇总供应商概览时用到的 Key 信息。
type keyBrief struct {
	// enabled 是否启用；health 是健康分（0～10）；doc 提供名称、倍率与优先级。
	enabled bool
	health  float64
	doc     keyDocument
}

// summarize 由供应商启停与它的全部 Key（按创建顺序）得出首选 Key 与健康汇总。
// 首选 Key 取已启用的 Key 中优先级数值最小的一把，优先级相同时取先创建的；健康汇总是已启用 Key 健康分的等权平均。
// 网关执行接入后，首选 Key 还需要排除冷却或熔断中的 Key。
func summarize(enabled bool, keys []keyBrief) (*PreferredKey, ProviderHealth) {
	if !enabled {
		return nil, ProviderHealth{State: ProviderHealthUnavailable, Reason: "供应商已停用"}
	}
	var preferred *keyDocument
	var total float64
	active := 0
	for i := range keys {
		if !keys[i].enabled {
			continue
		}
		active++
		total += keys[i].health
		if preferred == nil || keys[i].doc.Priority < preferred.Priority {
			preferred = &keys[i].doc
		}
	}
	if preferred == nil {
		return nil, ProviderHealth{State: ProviderHealthUnavailable, Reason: "没有已启用的 Key"}
	}
	// 健康分满分 10，换算为百分比后分档。
	percent := int(math.Round(total / float64(active) * 10))
	state := ProviderHealthAbnormal
	switch {
	case percent >= 80:
		state = ProviderHealthNormal
	case percent >= 50:
		state = ProviderHealthDegraded
	}
	return &PreferredKey{Name: preferred.Name, Multiplier: preferred.Multiplier}, ProviderHealth{State: state, Percent: &percent}
}

// Snapshot 读取供应商页需要的全部内容：供应商概览与表单默认值。
func (s *Store) Snapshot(ctx context.Context) (ProviderSnapshot, error) {
	snapshot := ProviderSnapshot{
		Providers: []ProviderOverview{},
		Defaults: ProviderDefaults{
			Attempts:          defaultAttempts,
			StreamTotalMillis: defaultStreamTotalMillis,
			FirstByteSeconds:  defaultFirstByteSeconds,
			RequestSeconds:    defaultRequestSeconds,
			KeyMultiplier:     defaultKeyMultiplier,
			KeyCacheMinutes:   defaultKeyCacheMinutes,
			KeyProbeMinutes:   defaultKeyProbeMinutes,
		},
	}
	// 数据库只有一个连接：先读完并关闭一个结果集，再发起下一条查询。
	keys, err := s.keyBriefs(ctx)
	if err != nil {
		return snapshot, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, enabled, document FROM providers ORDER BY enabled DESC, seq")
	if err != nil {
		return snapshot, fmt.Errorf("读取供应商: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, data string
		var enabled bool
		if err = rows.Scan(&id, &name, &enabled, &data); err != nil {
			return snapshot, err
		}
		var doc providerDocument
		if err = s.decodeDocument([]byte(data), &doc); err != nil {
			return snapshot, fmt.Errorf("供应商「%s」的数据已损坏: %w", name, err)
		}
		preferred, health := summarize(enabled, keys[id])
		snapshot.Providers = append(snapshot.Providers, ProviderOverview{
			Provider:     doc.provider(id, name, enabled),
			KeyCount:     len(keys[id]),
			PreferredKey: preferred,
			Health:       health,
		})
	}
	return snapshot, rows.Err()
}

// keyBriefs 按供应商分组读取全部 Key 的启停与文档，组内保持创建顺序；不读取密钥。
func (s *Store) keyBriefs(ctx context.Context) (map[string][]keyBrief, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT provider_id, enabled, document FROM provider_keys ORDER BY seq")
	if err != nil {
		return nil, fmt.Errorf("读取上游 Key: %w", err)
	}
	defer rows.Close()
	result := map[string][]keyBrief{}
	for rows.Next() {
		var providerID, data string
		// 没有调用结果可结算前，每把 Key 都是初始健康分。
		brief := keyBrief{health: initialKeyHealth}
		if err = rows.Scan(&providerID, &brief.enabled, &data); err != nil {
			return nil, err
		}
		if err = s.decodeDocument([]byte(data), &brief.doc); err != nil {
			return nil, fmt.Errorf("上游 Key 的数据已损坏: %w", err)
		}
		result[providerID] = append(result[providerID], brief)
	}
	return result, rows.Err()
}
