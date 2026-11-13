package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestBasellmCatalogLifecycle 用真实 HTTP 与 SQLite 验证换源、分时、阶梯、长期缓存、汇率变更及失败保留。
func TestBasellmCatalogLifecycle(t *testing.T) {
	fixture, err := os.ReadFile("testdata/basellm.json")
	if err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	var currencyRate atomic.Int32
	currencyRate.Store(5)
	var fullDownloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/legacy":
			fmt.Fprint(w, `{"deepseek/deepseek-flash":{"litellm_provider":"deepseek","input_cost_per_token":0.0000003,"output_cost_per_token":0.0000012},"deepseek/deepseek-v3.2":{"litellm_provider":"deepseek","input_cost_per_token":0.0000001}}`)
		case "/manifest.json":
			fmt.Fprintf(w, `{"newapi":{"exchangeRates":{"CNY":%d}}}`, currencyRate.Load())
		case "/all.json":
			if failed.Load() {
				http.Error(w, "offline", 503)
				return
			}
			if r.Header.Get("If-None-Match") == `"same"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			fullDownloads.Add(1)
			w.Header().Set("ETag", `"same"`)
			w.Write(fixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "models.sqlite")
	s, err := Open(path, server.Client(), server.URL+"/legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := s.Add(ctx, "deepseek", "deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	old.Pricing.Base[0].Price = 9
	old, err = s.Update(ctx, Edit{ID: old.ID, Revision: old.Revision, Name: "我的模型", Pricing: old.Pricing})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.Add(ctx, "deepseek", "deepseek-v3.2")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟换源前的持久来源标记；现有数据库无需离线迁移。
	old.Source = "LiteLLM"
	if _, err = s.save(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL+"/all.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 || s.CatalogStatus().Source != "basellm" {
		t.Fatalf("目录或来源错误: %d %+v", len(entries), s.CatalogStatus())
	}
	found := map[string]CatalogEntry{}
	for _, e := range entries {
		if e.Problem != "" {
			t.Fatalf("%s: %s", e.ModelID, e.Problem)
		}
		found[e.ModelID] = e
	}
	flash := found["deepseek-flash"]
	if !flash.Official || len(flash.Metadata) == 0 || len(flash.Pricing.Schedules) != 2 || flash.Pricing.Baseline != "低谷价格" {
		t.Fatalf("分时来源丢失: %+v", flash)
	}
	liteTestRate(t, flash.Pricing.Base, "input", .15)
	for _, window := range flash.Pricing.Schedules {
		if window.Multiplier != 2 || window.Timezone != "UTC" || len(window.Days) != 5 {
			t.Fatalf("分时窗口错误: %+v", window)
		}
	}
	gpt := found["gpt-5.4"]
	if len(gpt.Pricing.Tiers) != 1 || gpt.Pricing.Tiers[0].Above != 272000 {
		t.Fatalf("阶梯边界错误: %+v", gpt.Pricing)
	}
	// 模型实际声明缓存写入时才展开 12 小时价格。
	cached := found["gpt-5.6"]
	liteTestRate(t, cached.Pricing.Base, "cache_write_12h", cached.Pricing.Base[0].Price*2.5)
	liteTestRate(t, found["image-01"].Pricing.Base, "per_image", .005)
	saved, err := s.get(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ModelID != old.ModelID || saved.Name != "我的模型" || saved.Source != "basellm" || !contains(saved.Overrides, "base") {
		t.Fatalf("迁移覆盖身份或修改保护: %+v", saved)
	}
	liteTestRate(t, saved.Pricing.Base, "input", 9)
	liteTestRate(t, saved.SourcePricing.Base, "input", .15)
	gone, err := s.get(ctx, missing.ID)
	if err != nil || gone.SourceAvailable {
		t.Fatalf("缺失模型应保留: %+v %v", gone, err)
	}
	liteTestRate(t, gone.Pricing.Base, "input", .1)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if fullDownloads.Load() != 1 {
		t.Fatal("内容不变未使用条件请求")
	}
	currencyRate.Store(10)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if fullDownloads.Load() != 2 {
		t.Fatal("汇率变化未强制重取并重算目录")
	}
	entries, err = s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ModelID == "image-01" {
			liteTestRate(t, entry.Pricing.Base, "per_image", .0025)
		}
	}
	failed.Store(true)
	if _, err = s.Sync(ctx); err == nil {
		t.Fatal("网络失败应返回错误")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL+"/all.json")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	entries, err = s.Catalog(ctx)
	if err != nil || len(entries) != 5 {
		t.Fatalf("重启未保留有效缓存: %d %v", len(entries), err)
	}
}

// TestBasellmRejectsInexactPrices 验证不能准确表达的来源规则不会伪装为有效价格。
func TestBasellmRejectsInexactPrices(t *testing.T) {
	for _, cost := range []string{
		`{"currency":"CNY","input":2}`,
		`{"input":1,"output":2,"schedule":{"timezone":"UTC","fallback":"off_peak","windows":[{"hours":["01:00-04:00"],"input":2,"output":3}]}}`,
		`{"input":1,"context_over_200k":{"input":2}}`,
	} {
		entries, err := decodeProviderCatalog([]byte(`{"test":{"id":"test","models":{"m":{"cost":`+cost+`}}}}`), nil)
		if err != nil || len(entries) != 1 || entries[0].Problem == "" {
			t.Fatalf("非法规则未隔离: %+v %v", entries, err)
		}
	}
	// 汇率归一化只处理一次，包括继承币种的模式和阶梯缓存维度。
	raw := `{"test":{"id":"test","billing":{"cacheWrite1h":2},"models":{"m":{"cost":{"currency":"CNY","input":10,"output":20,"cache_write":12.5,"tiers":[{"tier":{"type":"context","size":100},"input":20}]},"experimental":{"modes":{"fast":{"cost":{"input":20,"output":40}}}}}}}}`
	entries, err := decodeProviderCatalog([]byte(raw), map[string]float64{"CNY": 5})
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.Problem != "" {
		t.Fatal(e.Problem)
	}
	liteTestRate(t, e.Pricing.Tiers[0].Rates, "cache_write", 2.5)
	liteTestRate(t, e.Pricing.Tiers[0].Rates, "cache_write_1h", 8)
	liteTestRate(t, e.Pricing.Modes[0].Rates, "input", 4)
	if !strings.Contains(e.SourceKey, "test/m") {
		t.Fatal("未保存精确来源键")
	}
}

// TestBasellmSubscriptionUnknownPrice 预付套餐的来源零价表示缺少按量价格，不能展示为免费。
func TestBasellmSubscriptionUnknownPrice(t *testing.T) {
	entries, err := decodeProviderCatalog([]byte(`{"plan":{"id":"plan","subscription":true,"models":{"m":{"cost":{"input":0,"output":0}}}}}`), nil)
	if err != nil || len(entries) != 1 {
		t.Fatalf("套餐目录: %+v %v", entries, err)
	}
	e := entries[0]
	if len(e.Pricing.Base) != 0 || len(e.Warnings) != 1 || e.Problem != "" {
		t.Fatalf("套餐零价被当作免费: %+v", e)
	}
}

// TestBasellmCanonicalOwnership 目录原生厂商身份不能覆盖模型明确声明的外部规范归属。
func TestBasellmCanonicalOwnership(t *testing.T) {
	entries, err := decodeProviderCatalog([]byte(`{"host":{"id":"host","models":{"external":{"canonical_model_id":"creator/model"},"native":{}}}}`), nil)
	if err != nil || len(entries) != 2 {
		t.Fatalf("目录: %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Official != (entry.ModelID == "native") {
			t.Fatalf("官方归属错误: %+v", entry)
		}
	}
}
