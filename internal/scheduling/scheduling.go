// Package scheduling 维护不依赖桌面宿主的全局调度策略：取值、默认值与 SQLite 持久化。
// 当前只保存策略；供应商顺序与网关的实际排序在供应商和网关模块接入后加入。
package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SchedulingStrategy 是全局唯一的候选排序方式，取值即持久化与前后端传递的字符串。
type SchedulingStrategy string

// 三种调度策略；任何时刻有且只有一种生效。
const (
	// CacheAffinity 同一会话优先沿用上次成功的上游，没有可沿用的上游时按供应商顺序。
	CacheAffinity SchedulingStrategy = "cache_affinity"
	// FixedOrder 始终按供应商顺序主备，不做会话亲和。
	FixedOrder SchedulingStrategy = "fixed_order"
	// CostBased 兼容候选中 Key 配置倍率低的优先。
	CostBased SchedulingStrategy = "cost_based"
)

// DefaultStrategy 是尚未保存任何策略时生效的策略。
const DefaultStrategy = CacheAffinity

// SchedulingPolicy 是调度页读取的已保存调度配置。
type SchedulingPolicy struct {
	// Strategy 当前生效的调度策略。
	Strategy SchedulingStrategy `json:"strategy"`
}

// Validate 只接受三种已定义的策略，返回可直接展示给用户的错误。
func (s SchedulingStrategy) Validate() error {
	switch s {
	case CacheAffinity, FixedOrder, CostBased:
		return nil
	}
	return errors.New("调度策略只能是缓存亲和、固定顺序或成本优先")
}

// Store 在共享的本地数据库中读写调度策略；数据库生命周期由打开它的模块管理。
type Store struct {
	// db 是与模型、设置模块共用的 SQLite 句柄。
	db *sql.DB
}

// New 复用已打开的数据库并确保调度策略表存在。
// 该表至多一行（id 固定为 1），策略取值由 CHECK 约束限定，与 SchedulingStrategy 的三个常量保持一致。
func New(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS scheduling_policy (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 strategy TEXT NOT NULL CHECK (strategy IN ('cache_affinity','fixed_order','cost_based')))`)
	if err != nil {
		return nil, fmt.Errorf("初始化调度数据: %w", err)
	}
	return &Store{db: db}, nil
}

// Policy 读取已保存的调度配置：尚未保存时返回默认策略；读取失败或内容不合法时报错，不用默认策略冒充已保存的配置。
func (s *Store) Policy(ctx context.Context) (SchedulingPolicy, error) {
	var strategy SchedulingStrategy
	err := s.db.QueryRowContext(ctx, "SELECT strategy FROM scheduling_policy WHERE id = 1").Scan(&strategy)
	if errors.Is(err, sql.ErrNoRows) {
		return SchedulingPolicy{Strategy: DefaultStrategy}, nil
	}
	if err != nil {
		return SchedulingPolicy{}, fmt.Errorf("读取调度策略: %w", err)
	}
	if err = strategy.Validate(); err != nil {
		return SchedulingPolicy{}, fmt.Errorf("调度策略数据无效: %w", err)
	}
	return SchedulingPolicy{Strategy: strategy}, nil
}

// SaveStrategy 校验后以单条语句替换已保存的策略；失败时原策略保持不变。
func (s *Store) SaveStrategy(ctx context.Context, strategy SchedulingStrategy) error {
	if err := strategy.Validate(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO scheduling_policy(id, strategy) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET strategy = excluded.strategy", string(strategy)); err != nil {
		return fmt.Errorf("保存调度策略: %w", err)
	}
	return nil
}
