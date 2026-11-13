package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"

	_ "modernc.org/sqlite"
)

// Store 将持久化、来源读取和价格合并集中在一个模块；调用方只提交用户操作。
type Store struct {
	// db 维护本地配置；client 和 catalogURL 定义外部来源；mu 保护操作与目录候选。
	db         *sql.DB
	client     *http.Client
	catalogURL string
	mu         sync.Mutex
	catalog    []CatalogEntry
	// catalogState 为持久目录快照；更新请求和策略由 mu 保护，不占网络期间的写入锁。
	catalogState   catalogCache
	catalogFlight  chan struct{}
	catalogAttempt time.Time
	catalogResult  SyncResult
	catalogError   error
	catalogAuto    bool
	catalogMinutes int
	catalogWake    chan struct{}
	// catalogContext 属于模块生命周期；关闭时取消网络并等待共享任务结束。
	catalogContext context.Context
	catalogCancel  context.CancelFunc
	closed         bool
	// credentials 加密可能含账号密码的全局代理配置，桌面入口在启动时接入。
	credentials *credentials.Store
}

// Open 打开本地 SQLite；client 和目录地址由入口提供，便于真实网络和隔离上游共用同一操作接口。
func Open(path string, client *http.Client, catalogURL string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("创建模型数据目录: %w", err)
	}
	// 每个连接都启用外键约束：共用本数据库的供应商模块依赖外键级联清理从属数据。
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// 模型主体以 JSON 保存完整价格规则；稳定身份及统一模型名由 SQLite 唯一约束维护。
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS models (id TEXT PRIMARY KEY, model_id TEXT NOT NULL UNIQUE, document TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS settings (name TEXT PRIMARY KEY, document TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化模型数据: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	catalogContext, cancel := context.WithCancel(context.Background())
	s := &Store{db: db, client: client, catalogURL: catalogURL, catalogContext: catalogContext, catalogCancel: cancel, catalogAuto: true, catalogMinutes: 10, catalogWake: make(chan struct{}, 1)}
	s.catalogState.Status = CatalogStatus{Source: catalogSource, Failures: []string{}}
	s.loadCatalogCache(context.Background())
	return s, nil
}

// Close 由应用退出时关闭数据库，不暴露给桌面绑定。
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	s.catalogCancel()
	flight := s.catalogFlight
	s.mu.Unlock()
	if flight != nil {
		<-flight
	}
	return s.db.Close()
}

// DB 返回本地数据库句柄，供同一数据文件中的其他模块复用；生命周期仍由 Close 管理。
func (s *Store) DB() *sql.DB { return s.db }

// List 读取已保存模型，按显示名称与稳定身份排序，空集合仍返回数组。
func (s *Store) List(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT document FROM models ORDER BY json_extract(document,'$.name'), id")
	if err != nil {
		return nil, fmt.Errorf("读取模型: %w", err)
	}
	defer rows.Close()
	result := []Model{}
	for rows.Next() {
		var data string
		var m Model
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &m); err != nil {
			return nil, fmt.Errorf("模型数据损坏: %w", err)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// get 读取单个完整模型，缺失时给出可供界面直接展示的错误。
func (s *Store) get(ctx context.Context, id string) (Model, error) {
	var data string
	var m Model
	err := s.db.QueryRowContext(ctx, "SELECT document FROM models WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return m, errors.New("模型不存在，请刷新列表")
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal([]byte(data), &m)
	return m, err
}

// save 在单条语句内替换完整规则并递增版本，返回已保存版本；调用方持有写入锁。
func (s *Store) save(ctx context.Context, m Model) (Model, error) {
	m.Revision++
	data, err := json.Marshal(m)
	if err != nil {
		return Model{}, err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE models SET document=? WHERE id=?", string(data), m.ID)
	return m, err
}

// Add 从已读取的真实来源候选添加模型，重复统一身份返回错误而不覆盖已有配置。
func (s *Store) Add(ctx context.Context, providerID, modelID string) (Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(ctx, providerID, modelID, nil)
}

// add 在写入锁内按来源模型 ID 构造模型，一次保存确认草稿与可信来源快照。
func (s *Store) add(ctx context.Context, providerID, sourceModelID string, draft *Create) (Model, error) {
	var entry *CatalogEntry
	for i := range s.catalog {
		e := &s.catalog[i]
		if e.ProviderID == providerID && e.ModelID == sourceModelID && !e.Deprecated {
			entry = e
			break
		}
	}
	if entry == nil {
		return Model{}, errors.New("请先加载目录并选择模型")
	}
	if entry.Problem != "" {
		return Model{}, errors.New(entry.Problem)
	}
	id, err := newID()
	if err != nil {
		return Model{}, err
	}
	m := Model{Source: catalogSource, SourceKey: entry.SourceKey, Warnings: entry.Warnings, ID: id, ModelID: entry.ModelID, Name: entry.Name, Family: entry.Family, Kind: entry.Kind, Context: entry.Context, Capabilities: entry.Capabilities, ProviderID: entry.ProviderID, ProviderName: entry.ProviderName, SourceModelID: entry.ModelID, CanonicalID: entry.CanonicalID, Official: entry.Official, SourceAvailable: true, SyncedAt: time.Now().UTC().Format(time.RFC3339), Pricing: entry.Pricing, SourcePricing: entry.Pricing, Overrides: []string{}}
	// 同一来源模型的跨供应商候选共用统一模型名，保留实际来源名称用于后续同步。
	if entry.CanonicalID != "" {
		parts := strings.SplitN(entry.CanonicalID, "/", 2)
		if len(parts) == 2 {
			m.ModelID = parts[1]
		}
	}
	if draft != nil {
		// 用户确认的本地模型 ID 与目录来源 ID 分离；后续同步仍按 SourceModelID 命中来源。
		m.ModelID = draft.ModelID
		m.Name = draft.Name
		m.Pricing = clonePricing(draft.Pricing)
		if draft.OriginalPricing != nil {
			for _, g := range groups {
				if sameGroup(draft.Pricing, *draft.OriginalPricing, g) {
					copyGroup(&m.Pricing, entry.Pricing, g)
				}
			}
		}
		for _, g := range groups {
			if !sameGroup(m.Pricing, m.SourcePricing, g) || (draft.OriginalPricing != nil && !sameGroup(draft.Pricing, *draft.OriginalPricing, g)) {
				m.Overrides = append(m.Overrides, g)
			}
		}
	}
	return s.insert(ctx, m)
}

// insert 由数据库唯一约束拒绝重复身份，不覆盖已有配置。
func (s *Store) insert(ctx context.Context, m Model) (Model, error) {
	m.Revision = 1
	data, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO models(id,model_id,document) VALUES(?,?,?)", m.ID, m.ModelID, string(data))
	if err != nil {
		var exists bool
		if e := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM models WHERE model_id=?)", m.ModelID).Scan(&exists); e == nil && exists {
			return Model{}, errors.New("该模型已添加，请在列表中编辑")
		}
		return Model{}, fmt.Errorf("保存模型失败: %w", err)
	}
	return m, nil
}

// Update 拒绝过期草稿，校验可编辑字段并按规则组设置修改保护，不更改来源身份。
func (s *Store) Update(ctx context.Context, edit Edit) (Model, error) {
	edit.Name = strings.TrimSpace(edit.Name)
	if edit.Name == "" || len([]rune(edit.Name)) > 80 {
		return Model{}, errors.New("显示名称需为 1～80 个字符")
	}
	if err := validatePricing(edit.Pricing); err != nil {
		return Model{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.get(ctx, edit.ID)
	if err != nil {
		return m, err
	}
	if (edit.OriginalPricing == nil && edit.Revision != m.Revision) || (edit.OriginalPricing != nil && edit.EditRevision != m.EditRevision) {
		return Model{}, errors.New("模型已更新，请取消编辑并重新打开，已保留最新配置")
	}
	userChanged := map[string]bool{}
	if edit.OriginalPricing != nil {
		merged := clonePricing(m.Pricing)
		for _, g := range groups {
			if !sameGroup(edit.Pricing, *edit.OriginalPricing, g) {
				userChanged[g] = true
				copyGroup(&merged, edit.Pricing, g)
			}
		}
		edit.Pricing = merged
	}
	for _, g := range groups {
		if (userChanged[g] || !sameGroup(m.Pricing, edit.Pricing, g)) && !contains(m.Overrides, g) {
			m.Overrides = append(m.Overrides, g)
		}
	}
	for _, g := range edit.RestoreGroups {
		if !contains(groups, g) {
			return Model{}, errors.New("未知价格规则组")
		}
		if !m.SourceAvailable || !hasGroup(m.SourcePricing, g) {
			return Model{}, errors.New("没有可用的来源值，已保留本地配置")
		}
		// 恢复后未再次改价时采用最新来源，避免后台刷新后将旧来源误保护为人工改价。
		if edit.OriginalSourcePricing != nil && sameGroup(edit.Pricing, *edit.OriginalSourcePricing, g) {
			copyGroup(&edit.Pricing, m.SourcePricing, g)
		}
		// 用户恢复后又编辑该组时保留新的编辑，不用恢复标记覆盖用户输入。
		if !sameGroup(edit.Pricing, m.SourcePricing, g) {
			continue
		}
		remaining := []string{}
		for _, existing := range m.Overrides {
			if existing != g {
				remaining = append(remaining, existing)
			}
		}
		m.Overrides = remaining
	}
	m.Name = edit.Name
	m.EditRevision++
	m.Pricing = edit.Pricing
	return s.save(ctx, m)
}

// Delete 删除统一模型，由外键在同一语句事务中级联清理供应商关联、映射及 Key 模型权限。
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM models WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("模型不存在，请刷新列表")
	}
	return nil
}

// GetProxy 解密并返回全局网络设置；尚未保存时明确使用直连。
func (s *Store) GetProxy(ctx context.Context) (Proxy, error) {
	var p Proxy
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM settings WHERE name='proxy'").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	plain := []byte(data)
	if s.credentials != nil {
		plain, err = s.credentials.DecodeDocument("global-proxy", data)
		if err != nil {
			return p, err
		}
	}
	err = json.Unmarshal(plain, &p)
	return p, err
}

// SetProxy 保存校验后的全局代理；桌面环境加密落盘，目录请求在执行时读取最新配置。
func (s *Store) SetProxy(ctx context.Context, p Proxy) error {
	if _, err := proxyTransport(p); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if s.credentials != nil {
		sealed, encodeErr := s.credentials.EncodeDocument("global-proxy", data)
		if encodeErr != nil {
			return encodeErr
		}
		data = []byte(sealed)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO settings(name,document) VALUES('proxy',?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", string(data))
	return err
}

// Create 一次保存确认后的目录模型或手动模型，不提前落库。
func (s *Store) Create(ctx context.Context, draft Create) (Model, error) {
	draft.Name = strings.TrimSpace(draft.Name)
	if draft.Name == "" || len([]rune(draft.Name)) > 80 {
		return Model{}, errors.New("显示名称需为 1～80 个字符")
	}
	if err := validatePricing(draft.Pricing); err != nil {
		return Model{}, err
	}
	modelID, err := normalizeModelID(draft.ModelID)
	if err != nil {
		return Model{}, err
	}
	draft.ModelID = modelID
	s.mu.Lock()
	defer s.mu.Unlock()
	if draft.ProviderID != "" {
		// 兼容旧调用方只传目录 ID 的请求；新调用方显式区分来源 ID 与本地统一 ID。
		draft.SourceModelID = strings.TrimSpace(draft.SourceModelID)
		if draft.SourceModelID == "" {
			draft.SourceModelID = draft.ModelID
		}
		return s.add(ctx, draft.ProviderID, draft.SourceModelID, &draft)
	}
	if !contains([]string{"chat", "image", "video", "vector"}, draft.Kind) {
		return Model{}, errors.New("请选择模型类型")
	}
	id, err := newID()
	if err != nil {
		return Model{}, err
	}
	return s.insert(ctx, Model{ID: id, ModelID: draft.ModelID, Name: draft.Name, Kind: draft.Kind, ProviderName: "手动创建", Capabilities: []string{}, Pricing: draft.Pricing, SourcePricing: Pricing{}, Overrides: []string{}})
}

// normalizeModelID 清理本地统一模型 ID 的首尾空白，并拒绝空值、超过 256 字节或包含空白的输入。
func normalizeModelID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("请填写有效的模型 ID（不能包含空白，最长 256 字节）")
	}
	return value, nil
}
