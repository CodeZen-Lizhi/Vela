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
-- 顺序由调度维护；供应商创建、删除时在同一事务中维护集合完整性。
CREATE TABLE IF NOT EXISTS scheduling_meta(id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
INSERT OR IGNORE INTO scheduling_meta VALUES(1,0);
CREATE TABLE IF NOT EXISTS scheduling_provider_order(provider_id TEXT PRIMARY KEY, position INTEGER NOT NULL UNIQUE);
INSERT INTO scheduling_provider_order(provider_id,position)
 SELECT id,(SELECT COALESCE(MAX(position),0) FROM scheduling_provider_order)+ROW_NUMBER() OVER(ORDER BY seq)
 FROM providers WHERE id NOT IN (SELECT provider_id FROM scheduling_provider_order);
CREATE TRIGGER IF NOT EXISTS scheduling_provider_added AFTER INSERT ON providers BEGIN
 INSERT INTO scheduling_provider_order VALUES(NEW.id,(SELECT COALESCE(MAX(position),0)+1 FROM scheduling_provider_order));
 UPDATE scheduling_meta SET revision=revision+1 WHERE id=1;
END;
CREATE TRIGGER IF NOT EXISTS scheduling_provider_deleted AFTER DELETE ON providers BEGIN
 DELETE FROM scheduling_provider_order WHERE provider_id=OLD.id;
 UPDATE scheduling_meta SET revision=revision+1 WHERE id=1;
END;

`

// Store 维护供应商与上游 Key 的本地配置。它与模型模块共用同一个 SQLite 数据库，数据库的打开与关闭由入口负责。
type Store struct {
	// db 是共用的数据库句柄；mu 串行化写操作，保证“先检查再写入”的步骤不被并发调用打断。
	db *sql.DB
	mu sync.Mutex
	// vault 加解密上游密钥。
	vault *vault
	// healthLookup 在接收请求前注入整Key健康投影。
	healthLookup func(string, string) ProviderKeyHealthSnapshot
	// documents 加密供应商和Key的完整配置文档。
	documents *credentials.Store
	// upstreamCache 保存各供应商当前配置下五分钟内的成功模型列表，由 mu 保护。
	upstreamCache map[string]upstreamModelsCache
}

// New 复用已打开的数据库并建表；keys 是凭据主密钥的保存位置。
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
	if err := s.removeLocalProxies(context.Background()); err != nil {
		return nil, fmt.Errorf("迁移局部代理: %w", err)
	}
	if err := s.removeRetiredRequestFields(context.Background()); err != nil {
		return nil, fmt.Errorf("迁移请求重试策略: %w", err)
	}
	if err := s.removeProviderNotes(context.Background()); err != nil {
		return nil, fmt.Errorf("迁移供应商备注: %w", err)
	}
	return s, nil
}

// providerDocument 是 providers.document 列保存的内容；身份、名称与启停在独立列中。
type providerDocument struct {
	// Website、URL、Request 与 Provider 的同名字段含义相同。
	Website string        `json:"website"`
	URL     string        `json:"url"`
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

// normalizeProxy 校验出口选择并清理无关字段；供应商和 Key 均直接跟随全局，兼容 Key 的旧 provider 输入。
func normalizeProxy(choice ProxyChoice, forKey bool) (ProxyChoice, error) {
	modes := []ProxyMode{ProxyGlobal, ProxyDirect}
	if forKey && choice.Mode == ProxyProvider {
		choice.Mode = ProxyGlobal
	}
	if !slices.Contains(modes, choice.Mode) {
		return ProxyChoice{}, errors.New("请选择网络代理方式")
	}
	return ProxyChoice{Mode: choice.Mode}, nil
}

// normalizeRequest 校验请求配置；数值为 0 表示使用默认值，其余必须落在各自范围内。
func normalizeRequest(p RequestPolicy) (RequestPolicy, error) {
	switch {
	case p.Attempts != 0 && (p.Attempts < 1 || p.Attempts > 99):
		return p, errors.New("总尝试次数需为 1～99 的整数")
	case p.StreamTotalMillis != 0 && (p.StreamTotalMillis < 1000 || p.StreamTotalMillis > 1_200_000):
		return p, errors.New("流式空闲超时需在 1～1200 秒之间")
	case p.FirstByteSeconds != 0 && (p.FirstByteSeconds < 1 || p.FirstByteSeconds > 300):
		return p, errors.New("首次响应超时需为 1～300 秒的整数")
	case p.RequestSeconds != 0 && (p.RequestSeconds < 1 || p.RequestSeconds > 1200):
		return p, errors.New("非流式请求超时需为 1～1200 秒的整数")
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
	return name, providerDocument{Website: website, URL: serviceURL, Request: request}, nil
}

// provider 由独立列和文档组装对外的供应商配置。
func (doc providerDocument) provider(id, name string, enabled bool) Provider {
	return Provider{ID: id, Name: name, Website: doc.Website, URL: doc.URL, Enabled: enabled, Request: doc.Request}
}

// saveError 把写入失败转成用户可读的原因：名称被其他供应商占用时指出重名，其余保留原始错误。
func (s *Store) saveError(ctx context.Context, name, id string, cause error) error {
	var taken bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE name=? AND id<>?)", name, id).Scan(&taken); err == nil && taken {
		return fmt.Errorf("供应商名称「%s」已存在", name)
	}
	return fmt.Errorf("保存供应商: %w", cause)
}

// Create 新增供应商，默认启用。名称重复或任一字段不合法时不写入。
func (s *Store) Create(ctx context.Context, draft ProviderDraft) (Provider, error) {
	name, doc, err := draft.normalize()
	if err != nil {
		return Provider{}, err
	}
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.encodeDocument(doc)
	if err != nil {
		return Provider{}, err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO providers(id,name,enabled,document) VALUES(?,?,1,?)", id, name, string(data)); err != nil {
		return Provider{}, s.saveError(ctx, name, "", err)
	}
	return doc.provider(id, name, true), nil
}

// Update 保存供应商的名称与配置，不改变启停状态；供应商不存在、名称重复或字段不合法时不写入。
func (s *Store) Update(ctx context.Context, id string, draft ProviderDraft) (Provider, error) {
	name, doc, err := draft.normalize()
	if err != nil {
		return Provider{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err = s.db.QueryRowContext(ctx, "SELECT document FROM providers WHERE id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return Provider{}, errProviderMissing
	} else if err != nil {
		return Provider{}, err
	}
	var old providerDocument
	if err = s.decodeDocument([]byte(raw), &old); err != nil {
		return Provider{}, err
	}

	data, err := s.encodeDocument(doc)
	if err != nil {
		return Provider{}, err
	}
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
	// id 标识同步状态所属 Key；enabled 是否启用；health 为健康快照；doc 提供名称、倍率与优先级。
	id      string
	enabled bool
	health  ProviderKeyHealthSnapshot
	doc     keyDocument
}

// summarize 由供应商启停与它的全部 Key（按创建顺序）得出首选 Key 与健康汇总。
// 首选 Key 取可调用 Key 中优先级数值最小的一把，优先级相同时取先创建的；健康汇总取启用 Key 的最高健康分。
// 首选 Key 排除冷却、熔断或无凭据的 Key；健康分档不受供应商启停或即时准入影响。
func summarize(enabled bool, keys []keyBrief) (*PreferredKey, ProviderHealth) {
	var preferred *keyBrief
	var bestScore float64
	active := 0
	for i := range keys {
		if !keys[i].enabled {
			continue
		}
		active++
		bestScore = max(bestScore, keys[i].health.Score)
		if keys[i].health.Available && (preferred == nil || keys[i].doc.Priority < preferred.doc.Priority) {
			preferred = &keys[i]
		}
	}
	if active == 0 {
		return nil, ProviderHealth{State: ProviderHealthUnavailable, Reason: "没有已启用的 Key"}
	}
	// 用最高原始分数分档；坏掉的备用 Key 不拉低好线路，舍入后的兼容百分比不参与判断。
	percent := int(math.Round(bestScore * 10))
	state := ProviderHealthAbnormal
	switch {
	case bestScore >= 8:
		state = ProviderHealthNormal
	case bestScore >= 5:
		state = ProviderHealthDegraded
	case bestScore == 0:
		state = ProviderHealthCircuit
	}
	health := ProviderHealth{State: state, Percent: &percent}
	if !enabled || preferred == nil {
		return nil, health
	}
	return &PreferredKey{ID: preferred.id, Name: preferred.doc.Name, Multiplier: preferred.doc.Multiplier}, health
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
	rows, err := s.db.QueryContext(ctx, "SELECT p.id,p.name,p.enabled,p.document FROM providers p JOIN scheduling_provider_order o ON o.provider_id=p.id ORDER BY p.enabled DESC,o.position,p.name")
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
	rows, err := s.db.QueryContext(ctx, "SELECT id,provider_id, enabled,secret, document FROM provider_keys ORDER BY seq")
	if err != nil {
		return nil, fmt.Errorf("读取上游 Key: %w", err)
	}
	defer rows.Close()
	result := map[string][]keyBrief{}
	for rows.Next() {
		var providerID, data, keyID string
		var sealed []byte
		// 没有调用结果可结算前，每把 Key 都是初始满分且可用。
		brief := keyBrief{health: ProviderKeyHealthSnapshot{Score: initialKeyHealth, Available: true}}
		if err = rows.Scan(&keyID, &providerID, &brief.enabled, &sealed, &data); err != nil {
			return nil, err
		}
		if err = s.decodeDocument([]byte(data), &brief.doc); err != nil {
			return nil, fmt.Errorf("上游 Key 的数据已损坏: %w", err)
		}
		if s.healthLookup != nil {
			brief.health = s.healthLookup(keyID, executionVersion(sealed))
			brief.health.Available = brief.health.Available && sealed != nil
		}
		brief.id = keyID
		result[providerID] = append(result[providerID], brief)
	}
	return result, rows.Err()
}

// SetHealthLookup 在宿主启动、接收请求之前注入整Key健康快照；回调必须可并发读取且不能再访问本数据库。
func (s *Store) SetHealthLookup(lookup func(string, string) ProviderKeyHealthSnapshot) {
	s.healthLookup = lookup
}
