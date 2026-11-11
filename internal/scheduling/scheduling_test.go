package scheduling

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openStore 在真实 SQLite 文件上打开调度模块，供重启重读场景复用同一路径。
func openStore(t *testing.T, path string) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

// TestDefaultAndPersistence 验证未保存时为缓存亲和，每种策略保存后重新打开数据库仍可重读。
func TestDefaultAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, db := openStore(t, path)
	if policy, err := s.Policy(ctx); err != nil || policy.Strategy != CacheAffinity {
		t.Fatalf("默认策略 = %+v, %v", policy, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []SchedulingStrategy{FixedOrder, CostBased, CacheAffinity} {
		s, db = openStore(t, path)
		if err := s.SaveStrategy(ctx, want); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		s, db = openStore(t, path)
		if policy, err := s.Policy(ctx); err != nil || policy.Strategy != want {
			t.Fatalf("重读策略 = %+v, %v，期望 %s", policy, err, want)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestInvalidStrategyKeepsSaved 验证未定义的策略被拒绝且不覆盖已保存的策略。
func TestInvalidStrategyKeepsSaved(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	if err := s.SaveStrategy(ctx, CostBased); err != nil {
		t.Fatal(err)
	}
	for _, value := range []SchedulingStrategy{"", "load_balance", "缓存亲和", "Fixed_Order"} {
		if err := s.SaveStrategy(ctx, value); err == nil {
			t.Fatalf("策略 %q 应被拒绝", value)
		}
	}
	if policy, err := s.Policy(ctx); err != nil || policy.Strategy != CostBased {
		t.Fatalf("策略应保持成本优先，实际 %+v, %v", policy, err)
	}
}

// TestWriteFailureKeepsSaved 验证数据库拒绝写入时保存报错，重新打开后仍是上次成功保存的策略。
func TestWriteFailureKeepsSaved(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, db := openStore(t, path)
	if err := s.SaveStrategy(ctx, FixedOrder); err != nil {
		t.Fatal(err)
	}
	// 只读模式让后续写入由 SQLite 真实拒绝，读取仍然可用。
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveStrategy(ctx, CostBased); err == nil {
		t.Fatal("只读数据库上的保存应失败")
	}
	if policy, err := s.Policy(ctx); err != nil || policy.Strategy != FixedOrder {
		t.Fatalf("保存失败后策略 = %+v, %v", policy, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, db = openStore(t, path)
	defer db.Close()
	if policy, err := s.Policy(ctx); err != nil || policy.Strategy != FixedOrder {
		t.Fatalf("重启后策略 = %+v, %v", policy, err)
	}
}

// TestStoredProblemsAreReported 验证表不可读时报错，不把默认策略当作已保存的配置返回。
func TestStoredProblemsAreReported(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	if _, err := db.Exec("DROP TABLE scheduling_policy"); err != nil {
		t.Fatal(err)
	}
	if policy, err := s.Policy(ctx); err == nil {
		t.Fatalf("表缺失时应报错，实际返回 %+v", policy)
	}
	// 约束之外写入的未知取值同样不能读成有效策略。
	if _, err := db.Exec("CREATE TABLE scheduling_policy (id INTEGER PRIMARY KEY, strategy TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO scheduling_policy(id, strategy) VALUES(1, 'load_balance')"); err != nil {
		t.Fatal(err)
	}
	if policy, err := s.Policy(ctx); err == nil {
		t.Fatalf("未知策略应报错，实际返回 %+v", policy)
	}
}
