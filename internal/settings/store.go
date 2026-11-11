package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// 各设置分类在 settings 表中的行名；每个分类独立保存，互不覆盖。
const (
	rowGeneral   = "general"
	rowGateway   = "gateway"
	rowAccount   = "account"
	rowAttention = "attention"
)

// validator 是可在写入前自检的设置分类。
type validator interface {
	// Validate 返回可直接展示给用户的错误。
	Validate() error
}

// Store 在共享的本地数据库中读写全局设置；数据库生命周期由打开它的模块管理。
type Store struct {
	// db 是与模型模块共用的 SQLite 句柄。
	db *sql.DB
}

// New 复用已打开的数据库并确保 settings 表存在。
// 该表与模型模块共用：全局代理的 proxy 行由模型模块读写，表结构须与其建表语句保持一致。
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (name TEXT PRIMARY KEY, document TEXT NOT NULL)`); err != nil {
		return nil, fmt.Errorf("初始化设置数据: %w", err)
	}
	return &Store{db: db}, nil
}

// read 读取一个分类：尚未保存时返回 fallback；已保存内容损坏或不合法时报错并返回 fallback，不把半解析的值交给调用方。
func read[T validator](ctx context.Context, s *Store, name string, fallback T) (T, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM settings WHERE name=?", name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return fallback, fmt.Errorf("读取设置: %w", err)
	}
	// 从默认值开始解码，旧文档缺少的字段保持默认。
	value := fallback
	if err = json.Unmarshal([]byte(data), &value); err != nil {
		return fallback, fmt.Errorf("设置数据损坏: %w", err)
	}
	if err = value.Validate(); err != nil {
		return fallback, fmt.Errorf("设置数据无效: %w", err)
	}
	return value, nil
}

// write 校验后以单条语句替换一个分类，失败时原设置保持不变。
func (s *Store) write(ctx context.Context, name string, value validator) error {
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO settings(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", name, string(data)); err != nil {
		return fmt.Errorf("保存设置: %w", err)
	}
	return nil
}

// General 读取常规设置。
func (s *Store) General(ctx context.Context) (GeneralSettings, error) {
	return read(ctx, s, rowGeneral, DefaultGeneral())
}

// SaveGeneral 保存常规设置。
func (s *Store) SaveGeneral(ctx context.Context, value GeneralSettings) error {
	return s.write(ctx, rowGeneral, value)
}

// Gateway 读取网关监听设置。
func (s *Store) Gateway(ctx context.Context) (GatewaySettings, error) {
	return read(ctx, s, rowGateway, DefaultGateway())
}

// SaveGateway 保存网关监听设置。
func (s *Store) SaveGateway(ctx context.Context, value GatewaySettings) error {
	return s.write(ctx, rowGateway, value)
}

// AccountRefresh 读取全局账户刷新设置。
func (s *Store) AccountRefresh(ctx context.Context) (AccountRefresh, error) {
	return read(ctx, s, rowAccount, DefaultAccountRefresh())
}

// SaveAccountRefresh 保存全局账户刷新设置。
func (s *Store) SaveAccountRefresh(ctx context.Context, value AccountRefresh) error {
	return s.write(ctx, rowAccount, value)
}

// Attention 读取总览提醒偏好。
func (s *Store) Attention(ctx context.Context) (AttentionSettings, error) {
	return read(ctx, s, rowAttention, DefaultAttention())
}

// SaveAttention 保存总览提醒偏好。
func (s *Store) SaveAttention(ctx context.Context, value AttentionSettings) error {
	return s.write(ctx, rowAttention, value)
}
