// Package scheduling 维护不依赖桌面宿主的全局调度策略：取值、默认值与 SQLite 持久化。
// 配置保存与运行核心分开，SQLite 修订号隔离旧配置的在途亲和写入。
package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
	// ProviderOrder 是全部现存供应商的稳定顺序，停用项仍保留。
	ProviderOrder []string `json:"providerOrder"`
	// Revision 仅在策略、顺序或供应商集合实际变化后递增，隔离旧亲和。
	Revision int64 `json:"revision"`
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
 strategy TEXT NOT NULL CHECK (strategy IN ('cache_affinity','fixed_order','cost_based')));
CREATE TABLE IF NOT EXISTS scheduling_meta(id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
INSERT OR IGNORE INTO scheduling_meta VALUES(1,0);
CREATE TABLE IF NOT EXISTS scheduling_provider_order(provider_id TEXT PRIMARY KEY, position INTEGER NOT NULL UNIQUE);`)
	if err != nil {
		return nil, fmt.Errorf("初始化调度数据: %w", err)
	}
	return &Store{db: db}, nil
}

// Policy 读取已保存的调度配置：尚未保存时返回默认策略；读取失败或内容不合法时报错，不用默认策略冒充已保存的配置。
func (s *Store) Policy(ctx context.Context) (SchedulingPolicy, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SchedulingPolicy{}, err
	}
	defer tx.Rollback()
	p, err := readPolicy(ctx, tx)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// ExportConfiguration 在调用方事务内读取完整策略，供配置文件获得同一时刻的资源和顺序。
func (s *Store) ExportConfiguration(ctx context.Context, tx *sql.Tx) (SchedulingPolicy, error) {
	return readPolicy(ctx, tx)
}

// ImportConfiguration 在整包事务内应用调度；保留模式保留已保存的策略和原有顺序，新增供应商按来源顺序追加。
// 覆盖模式将导入供应商排在来源顺序，再追加本机独有项；不导入外部修订号或运行健康状态。
func (s *Store) ImportConfiguration(ctx context.Context, tx *sql.Tx, incoming SchedulingPolicy, providerIDs map[string]string, previous SchedulingPolicy, overwrite bool) error {
	if err := incoming.Strategy.Validate(); err != nil {
		return err
	}
	strategy := incoming.Strategy
	if !overwrite {
		var saved bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_policy WHERE id=1)").Scan(&saved); err != nil {
			return err
		}
		if saved {
			strategy = previous.Strategy
		}
	}
	order, seen := []string{}, map[string]bool{}
	if !overwrite {
		for _, id := range previous.ProviderOrder {
			order, seen[id] = append(order, id), true
		}
	}
	for _, sourceID := range incoming.ProviderOrder {
		id, ok := providerIDs[sourceID]
		if !ok {
			return errors.New("调度顺序引用了未导入的供应商")
		}
		if !seen[id] {
			order, seen[id] = append(order, id), true
		}
	}
	for _, id := range previous.ProviderOrder {
		if !seen[id] {
			order, seen[id] = append(order, id), true
		}
	}
	return writeConfiguration(ctx, tx, strategy, order, true)
}

// readPolicy 在调用方事务内同时读取策略、完整顺序和修订号，避免组合两个版本的配置。
func readPolicy(ctx context.Context, tx *sql.Tx) (SchedulingPolicy, error) {
	p := SchedulingPolicy{Strategy: DefaultStrategy, ProviderOrder: []string{}}
	err := tx.QueryRowContext(ctx, "SELECT strategy FROM scheduling_policy WHERE id=1").Scan(&p.Strategy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("读取调度策略: %w", err)
	}
	if err = p.Strategy.Validate(); err != nil {
		return p, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM scheduling_meta WHERE id=1").Scan(&p.Revision); err != nil {
		return p, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT provider_id FROM scheduling_provider_order ORDER BY position")
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return p, err
		}
		p.ProviderOrder = append(p.ProviderOrder, id)
	}
	return p, rows.Err()
}

// SaveStrategy 原子保存策略，实际变化才递增修订；失败保持旧配置和旧亲和范围。
func (s *Store) SaveStrategy(ctx context.Context, strategy SchedulingStrategy) error {
	return s.saveConfiguration(ctx, &strategy, nil, false)
}

// SaveProviderOrder 原子保存全部现存供应商顺序；重复、缺失、未知身份均拒绝。
func (s *Store) SaveProviderOrder(ctx context.Context, ids []string) error {
	return s.saveConfiguration(ctx, nil, ids, true)
}

// ImportPolicy 供配置搬运在导入供应商后原子应用策略与完整顺序；忽略外部修订号，失败保留现有配置。
// 整份供应商和凭据导入的事务由配置搬运模块负责，此方法不宣称实现整份配置导入。
func (s *Store) ImportPolicy(ctx context.Context, policy SchedulingPolicy) error {
	return s.saveConfiguration(ctx, &policy.Strategy, policy.ProviderOrder, true)
}

// saveConfiguration 在同一事务内校验并应用策略和顺序，避免导入只写成功一半。
func (s *Store) saveConfiguration(ctx context.Context, strategy *SchedulingStrategy, ids []string, replaceOrder bool) error {
	if strategy != nil {
		if err := strategy.Validate(); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := readPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if strategy == nil {
		strategy = &p.Strategy
	}
	if err = writeConfiguration(ctx, tx, *strategy, ids, replaceOrder); err != nil {
		return err
	}
	return tx.Commit()
}

// writeConfiguration 复用配置保存与整包导入的完整集合校验和修订更新，提交由调用方负责。
func writeConfiguration(ctx context.Context, tx *sql.Tx, strategy SchedulingStrategy, ids []string, replaceOrder bool) error {
	p, err := readPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if replaceOrder {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM providers")
		if err != nil {
			return err
		}
		expected := map[string]bool{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			expected[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) != len(expected) {
			return errors.New("供应商已变化，请刷新后重新排序")
		}
		for _, id := range ids {
			if !expected[id] {
				return errors.New("供应商顺序包含重复或不存在的供应商")
			}
			delete(expected, id)
		}
	} else {
		ids = p.ProviderOrder
	}
	if strategy == p.Strategy && slices.Equal(ids, p.ProviderOrder) {
		// 默认策略也需要保存为本机选择，后续“保留原有”导入才能区分已有策略与全新实例。
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO scheduling_policy(id,strategy) VALUES(1,?)", strategy)
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO scheduling_policy(id,strategy) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET strategy=excluded.strategy", strategy); err != nil {
		return fmt.Errorf("保存调度策略: %w", err)
	}
	if !slices.Equal(ids, p.ProviderOrder) {
		if _, err = tx.ExecContext(ctx, "DELETE FROM scheduling_provider_order"); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err = tx.ExecContext(ctx, "INSERT INTO scheduling_provider_order VALUES(?,?)", id, i+1); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE scheduling_meta SET revision=revision+1 WHERE id=1"); err != nil {
		return err
	}
	return nil
}
