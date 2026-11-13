package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestCatalogCacheLifecycle 用真实HTTP和SQLite核验冷启动、条件检查、失败保留、修改保护和重启缓存。
func TestCatalogCacheLifecycle(t *testing.T) {
	var calls, version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		v := version.Load()
		if v < 0 {
			http.Error(w, "unavailable", 503)
			return
		}
		tag := fmt.Sprintf("\"%d\"", v)
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(304)
			return
		}
		fmt.Fprintf(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":%g,"output_cost_per_token":0.000005}}`, float64(v)/1e6)
	}))
	defer server.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	s, err := Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureCatalogUpdates(false, 10)
	entries, err := s.Catalog(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cold: %v %+v", err, entries)
	}
	m, err := s.Add(ctx, "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	savedInput := m.Pricing.Base[0].Price
	if savedInput != 1 {
		t.Fatalf("unit conversion: %+v", m.Pricing)
	}
	original := clonePricing(m.Pricing)
	// 本地只改基础价，之后源价变化必须更新来源快照而保留有效值。
	modified := clonePricing(m.Pricing)
	modified.Base[0].Price = 9
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: modified})
	if err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	if list[0].Pricing.Base[0].Price != 9 || list[0].SourcePricing.Base[0].Price != 2 {
		t.Fatalf("protection: %+v", list[0])
	}
	before := calls.Load()
	for range 5 {
		if _, err = s.Catalog(ctx); err != nil {
			t.Fatal(err)
		}
		s.CheckCatalog()
	}
	if calls.Load() != before {
		t.Fatal("disabled cached reads performed network requests")
	}
	status := s.CatalogStatus()
	if status.CheckedAt == "" || status.UpdatedAt == "" {
		t.Fatal(status)
	}
	rev := list[0].Revision
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(ctx)
	if list[0].Revision != rev {
		t.Fatal("304 changed model version")
	}
	version.Store(-1)
	if _, err = s.Sync(ctx); err == nil {
		t.Fatal("503 should fail")
	}
	if s.CatalogStatus().Error == "" {
		t.Fatal("missing failure status")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	if s.CatalogStatus().Error == "" {
		t.Fatal("restart lost failure status")
	}
	before = calls.Load()
	entries, err = s.Catalog(ctx)
	if err != nil || entries[0].Pricing.Base[0].Price != 2 || calls.Load() != before {
		t.Fatalf("persisted cache: %v %+v", err, entries)
	}
	_ = original
}

// TestColdCatalogFailureRestart 核验没有有效缓存时失败状态仍持久化，关闭自动更新后重启不自行重试。
func TestColdCatalogFailureRestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "cold.sqlite")
	s, err := Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureCatalogUpdates(false, 10)
	if _, err = s.Catalog(context.Background()); err == nil {
		t.Fatal("cold download should fail")
	}
	want := s.CatalogStatus().Error
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	status := s.CheckCatalog()
	if want == "" || status.Error != want || status.UpdatedAt != "" || status.Refreshing || calls.Load() != 1 {
		t.Fatalf("cold failure lost or retried: %+v, calls=%d", status, calls.Load())
	}
	if _, err = s.Catalog(context.Background()); err == nil || calls.Load() != 2 {
		t.Fatalf("explicit cold retry: %v, calls=%d", err, calls.Load())
	}
}

// TestCatalogAutomaticPolicy 验证配置变更即时触发到期检查、间隔限制和关闭后的首次下载例外。
func TestCatalogAutomaticPolicy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000001}}`)
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "auto.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	s.CheckCatalog()
	if calls.Load() != 0 {
		t.Fatal("disabled startup requested network")
	}
	s.ConfigureCatalogUpdates(true, 7)
	s.CheckCatalog()
	waitCatalog(t, s)
	if calls.Load() != 1 {
		t.Fatalf("enabled check count %d", calls.Load())
	}
	s.CheckCatalog()
	if calls.Load() != 1 {
		t.Fatal("fresh cache requested again")
	}
	s.mu.Lock()
	s.catalogAttempt = time.Now().Add(-8 * time.Minute)
	s.mu.Unlock()
	s.CheckCatalog()
	waitCatalog(t, s)
	if calls.Load() != 2 {
		t.Fatal("configured interval ignored")
	}
	s.ConfigureCatalogUpdates(false, 7)
	s.mu.Lock()
	s.catalogAttempt = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	s.CheckCatalog()
	if calls.Load() != 2 {
		t.Fatal("disabled overdue requested")
	}
}

// waitCatalog 等待实际异步刷新结束；限时失败避免挂起测试。
func waitCatalog(t *testing.T, s *Store) {
	t.Helper()
	s.mu.Lock()
	flight := s.catalogFlight
	s.mu.Unlock()
	if flight != nil {
		select {
		case <-flight:
		case <-time.After(5 * time.Second):
			t.Fatal("catalog refresh stuck")
		}
	}
	if status := s.CatalogStatus(); status.Error != "" {
		t.Fatal(status.Error)
	}
}

// TestDraftMergeAfterRefresh 验证背景更新不破坏确认草稿、未修改组采用新价且人工并发仍拒绝。
func TestDraftMergeAfterRefresh(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":%g}}`, float64(version.Load())/1e6)
	}))
	defer server.Close()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "draft.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	entries, err := s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := clonePricing(entries[0].Pricing)
	version.Store(2)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Create(ctx, Create{ProviderID: "openai", SourceModelID: "test", ModelID: "local", Name: "Local", Pricing: original, OriginalPricing: &original})
	if err != nil {
		t.Fatal(err)
	}
	if m.Pricing.Base[0].Price != 2 || len(m.Overrides) != 0 {
		t.Fatalf("stale create: %+v", m)
	}
	draftOriginal := clonePricing(m.Pricing)
	version.Store(3)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	edit := Edit{ID: m.ID, Revision: m.Revision, EditRevision: m.EditRevision, Name: "Renamed", Pricing: draftOriginal, OriginalPricing: &draftOriginal}
	m, err = s.Update(ctx, edit)
	if err != nil || m.Pricing.Base[0].Price != 3 || len(m.Overrides) != 0 {
		t.Fatalf("draft merge %v %+v", err, m)
	}
	if _, err = s.Update(ctx, edit); err == nil {
		t.Fatal("concurrent manual edit accepted")
	}
}

// TestDraftRestoreAfterRefresh 通过真实保存入口验证恢复采用最新来源，恢复后再次编辑则保护新输入。
func TestDraftRestoreAfterRefresh(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":%g}}`, float64(version.Load())/1e6)
	}))
	defer server.Close()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "restore.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Add(ctx, "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := clonePricing(m.Pricing)
	user.Base[0].Price = 9
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: user})
	if err != nil {
		t.Fatal(err)
	}
	original, source := clonePricing(m.Pricing), clonePricing(m.SourcePricing)
	version.Store(2)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, EditRevision: m.EditRevision, Name: m.Name, Pricing: source, OriginalPricing: &original, OriginalSourcePricing: &source, RestoreGroups: []string{"base"}})
	if err != nil || m.Pricing.Base[0].Price != 2 || len(m.Overrides) != 0 {
		t.Fatalf("restore merge: %+v %v", m, err)
	}
	original, source = clonePricing(m.Pricing), clonePricing(m.SourcePricing)
	version.Store(3)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	user = clonePricing(source)
	user.Base[0].Price = 7
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, EditRevision: m.EditRevision, Name: m.Name, Pricing: user, OriginalPricing: &original, OriginalSourcePricing: &source, RestoreGroups: []string{"base"}})
	if err != nil || m.Pricing.Base[0].Price != 7 || !contains(m.Overrides, "base") {
		t.Fatalf("restore then edit: %+v %v", m, err)
	}
}

// TestSharedRefreshAndFastCache 验证多个首次打开合并请求，已缓存目录不等待慢网络任务。
func TestSharedRefreshAndFastCache(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000001}}`)
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "shared.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	ctx := context.Background()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	s.ConfigureCatalogUpdates(true, 1)
	s.mu.Lock()
	s.catalogAttempt = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	s.CheckCatalog()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow update not started")
	}
	started := time.Now()
	entries, err := s.Catalog(ctx)
	if err != nil || len(entries) != 1 || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("cache blocked: %v", err)
	}
	for range 4 {
		s.CheckCatalog()
	}
	if calls.Load() != 2 {
		t.Fatal("concurrent checks were not merged")
	}
	close(release)
	waitCatalog(t, s)
}

// TestExplicitEditEqualNewSourceStillProtected 保留明确的人工改价意图，即使新源值恰好与输入相同。
func TestExplicitEditEqualNewSourceStillProtected(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":%g}}`, float64(version.Load())/1e6)
	}))
	defer server.Close()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "equal.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	entries, err := s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := clonePricing(entries[0].Pricing)
	m, err := s.Add(ctx, "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	changed := clonePricing(original)
	changed.Base[0].Price = 2
	version.Store(2)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, EditRevision: m.EditRevision, Name: m.Name, Pricing: changed, OriginalPricing: &original})
	if err != nil || !contains(m.Overrides, "base") {
		t.Fatalf("equal explicit edit lost protection: %+v %v", m, err)
	}
	created, err := s.Create(ctx, Create{ProviderID: "openai", SourceModelID: "test", ModelID: "other", Name: "Other", Pricing: changed, OriginalPricing: &original})
	if err != nil || !contains(created.Overrides, "base") {
		t.Fatalf("equal create lost protection: %+v %v", created, err)
	}
	version.Store(3)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	for _, saved := range list {
		if saved.Pricing.Base[0].Price != 2 {
			t.Fatal("later source overwrote explicit price", saved)
		}
	}
}
