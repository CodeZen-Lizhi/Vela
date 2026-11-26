package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// combinedFixture 使用真实单连接 SQLite，调用方可检查事务中断和网络发起时的已提交状态。
func combinedFixture(t *testing.T, base string) (*Accounts, Provider) {
	t.Helper()
	store, closeDB := openStore(t, filepath.Join(t.TempDir(), "combined.sqlite"), &memoryKeys{})
	t.Cleanup(closeDB)
	accounts, err := NewAccounts(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	draft := validDraft("combined")
	draft.URL = base
	provider, err := store.Create(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return accounts, provider
}

// keySaveSnapshot 记录 Key 和查询行的全部可变列，用于验证错误后无任何部分保存。
func keySaveSnapshot(t *testing.T, a *Accounts, id string) []string {
	t.Helper()
	var keyDoc, keySecret, queryDoc, querySecret, revision string
	err := a.store.db.QueryRow(`SELECT k.document,hex(k.secret),q.document,hex(q.secret),q.revision
 FROM provider_keys k JOIN provider_key_queries q ON q.key_id=k.id WHERE k.id=?`, id).Scan(&keyDoc, &keySecret, &queryDoc, &querySecret, &revision)
	if err != nil {
		t.Fatal(err)
	}
	return []string{keyDoc, keySecret, queryDoc, querySecret, revision}
}

// TestSaveKeyFirstFailureUnknown 经组合入口验证首次失败仍保存 Key、来源和配置，倍率未知且可原地重试。
func TestSaveKeyFirstFailureUnknown(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"effective_rate_multiplier":0}`)
	}))
	defer server.Close()
	a, p := combinedFixture(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	draft := keyDraft("first", "fixture-first-secret", 1)
	supplied := 5.0
	draft.Multiplier = &supplied
	query := ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api"}, MultiplierSource: "upstream"}
	result, err := a.SaveKey(ctx, p.ID, "", draft, query)
	if err != nil || result.KeyID == "" || result.MultiplierSource != "upstream" || result.MultiplierStatus.State != "failed" {
		t.Fatalf("first save %+v %v", result, err)
	}
	keys, err := a.store.Keys(ctx, p.ID)
	if err != nil || len(keys) != 1 || keys[0].Multiplier != nil || keys[0].Name != "first" {
		t.Fatalf("unknown %+v %v", keys, err)
	}
	saved, err := a.ProviderKeyQuery(ctx, result.KeyID)
	if err != nil || saved.Config.Mode != "usage_api" || saved.Config.BaseURL != server.URL {
		t.Fatalf("query persisted %+v %v", saved, err)
	}
	fail.Store(false)
	result, err = a.RefreshProviderKey(ctx, result.KeyID)
	if err != nil || result.MultiplierStatus.State != "success" {
		t.Fatalf("retry %+v %v", result, err)
	}
	keys, err = a.store.Keys(ctx, p.ID)
	if err != nil || len(keys) != 1 || keys[0].Multiplier == nil || *keys[0].Multiplier != 0 {
		t.Fatalf("retry keys %+v %v", keys, err)
	}
}

// TestSaveKeyValidationAndRollback 验证创建/编辑的非法查询、重复密钥和数据库中途失败均不部分写入。
func TestSaveKeyValidationAndRollback(t *testing.T) {
	a, p := combinedFixture(t, "https://example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manual := ProviderKeyQueryDraft{ReuseProvider: true}
	first, err := a.SaveKey(ctx, p.ID, "", keyDraft("first", "fixture-first-secret", 1), manual)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.SaveKey(ctx, p.ID, "", keyDraft("second", "fixture-second-secret", 2), manual)
	if err != nil {
		t.Fatal(err)
	}
	before := keySaveSnapshot(t, a, first.KeyID)
	invalid := ProviderKeyQueryDraft{Config: QueryConfig{Mode: "custom", Method: "POST", URL: "https://example.com/query", Body: "{"}, MultiplierSource: "upstream"}
	for _, id := range []string{"", first.KeyID} {
		if _, err = a.SaveKey(ctx, p.ID, id, keyDraft("changed", "fixture-replacement", 3), invalid); err == nil {
			t.Fatal("invalid query accepted")
		}
		if !reflect.DeepEqual(before, keySaveSnapshot(t, a, first.KeyID)) {
			t.Fatal("invalid query partially saved")
		}
		if _, err = a.SaveKey(ctx, p.ID, id, keyDraft("duplicate", "fixture-second-secret", 3), manual); err == nil {
			t.Fatal("duplicate secret accepted")
		}
		if !reflect.DeepEqual(before, keySaveSnapshot(t, a, first.KeyID)) {
			t.Fatal("duplicate partially saved")
		}
	}
	// 查询行写入在 Key 写入之后失败，真实 SQLite 事务必须把前一条 Key 写入一同回滚。
	if _, err = a.store.db.Exec(`CREATE TRIGGER reject_combined_query BEFORE INSERT ON provider_key_queries
 BEGIN SELECT RAISE(ABORT,'fixture rollback'); END`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", first.KeyID} {
		if _, err = a.SaveKey(ctx, p.ID, id, keyDraft("changed", "fixture-replacement", 3), manual); err == nil {
			t.Fatal("injected database failure accepted")
		}
		if !reflect.DeepEqual(before, keySaveSnapshot(t, a, first.KeyID)) {
			t.Fatal("transaction partially saved")
		}
	}
	keys, err := a.store.Keys(ctx, p.ID)
	if err != nil || len(keys) != 2 || keys[1].ID != second.KeyID {
		t.Fatalf("rollback changed key count %+v %v", keys, err)
	}
}

// TestSaveKeySourceTransition 验证普通编辑绕过查询队列，刷新失败保留有效倍率，切回手动重新校验输入。
func TestSaveKeySourceTransition(t *testing.T) {
	var fail atomic.Bool
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"effective_rate_multiplier":120}`)
	}))
	defer server.Close()
	a, p := combinedFixture(t, server.URL)
	ctx := context.Background()
	query := ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api"}, MultiplierSource: "upstream"}
	draft := keyDraft("original", "fixture-secret", 1)
	result, err := a.SaveKey(ctx, p.ID, "", draft, query)
	if err != nil || result.MultiplierStatus.State != "success" {
		t.Fatalf("initial %+v %v", result, err)
	}
	fail.Store(true)
	draft.Name = "edited"
	draft.Secret = ""
	draft.Multiplier = nil
	// 占住查询席位以模拟慢上游任务，普通编辑仍应完成本地保存。
	if err = a.beginQuery(ctx); err != nil {
		t.Fatal(err)
	}
	editCtx, cancelEdit := context.WithTimeout(ctx, time.Second)
	result, err = a.SaveKey(editCtx, p.ID, result.KeyID, draft, query)
	cancelEdit()
	a.endQuery()
	if err != nil || result.MultiplierStatus.State != "success" || requests.Load() != 1 {
		t.Fatalf("仅改名称不应重新查询上游：请求数 %d，错误 %v", requests.Load(), err)
	}
	result, err = a.RefreshProviderKey(ctx, result.KeyID)
	if err != nil || result.MultiplierStatus.State != "failed" || requests.Load() != 2 {
		t.Fatalf("主动刷新应记录失败：请求数 %d，错误 %v", requests.Load(), err)
	}
	keys, err := a.store.Keys(ctx, p.ID)
	if err != nil || keys[0].Name != "edited" || keys[0].Multiplier == nil || *keys[0].Multiplier != 120 {
		t.Fatalf("known rate lost %+v %v", keys, err)
	}
	before := keySaveSnapshot(t, a, result.KeyID)
	query.MultiplierSource = "manual"
	tooLarge := 120.0
	for _, value := range []*float64{nil, &tooLarge} {
		draft.Multiplier = value
		if _, err = a.SaveKey(ctx, p.ID, result.KeyID, draft, query); err == nil {
			t.Fatal("invalid manual multiplier accepted")
		}
		if !reflect.DeepEqual(before, keySaveSnapshot(t, a, result.KeyID)) {
			t.Fatal("invalid transition partially saved")
		}
	}
	calls := requests.Load()
	zero := 0.0
	draft.Multiplier = &zero
	result, err = a.SaveKey(ctx, p.ID, result.KeyID, draft, query)
	if err != nil || result.MultiplierSource != "manual" || requests.Load() != calls {
		t.Fatalf("manual transition %+v %v", result, err)
	}
	keys, err = a.store.Keys(ctx, p.ID)
	if err != nil || *keys[0].Multiplier != 0 {
		t.Fatalf("manual input ignored %+v %v", keys, err)
	}
}

// TestSaveKeyCommitsBeforeNetwork 用上游 HTTP 处理器重读同一单连接数据库，证明网络在事务提交和解锁之后发起。
func TestSaveKeyCommitsBeforeNetwork(t *testing.T) {
	var accounts *Accounts
	var observed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var count int
		err := accounts.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM provider_keys k
 JOIN provider_key_queries q ON q.key_id=k.id WHERE json_extract(k.document,'$.multiplier') IS NULL`).Scan(&count)
		observed.Store(err == nil && count == 1)
		// 写锁必须也已释放；使用本地操作证明提交后的查询不占用 Store.mu。
		if err == nil {
			_, err = accounts.store.Create(r.Context(), validDraft("created-during-query"))
		}
		if err != nil {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"effective_rate_multiplier":2}`)
	}))
	defer server.Close()
	a, p := combinedFixture(t, server.URL)
	accounts = a
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := a.SaveKey(ctx, p.ID, "", keyDraft("first", "fixture-first", 1), ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api"}, MultiplierSource: "upstream"})
	if err != nil || !observed.Load() || result.MultiplierStatus.State != "success" {
		t.Fatalf("network before commit %+v %v", result, err)
	}
}
