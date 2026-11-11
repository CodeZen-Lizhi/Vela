package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestLifecycle 通过真实 HTTP 来源和 SQLite 验证添加、编辑、同步保护、恢复、下架、重启与删除。
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version.Load() == 4 {
			fmt.Fprint(w, `broken`)
			return
		}
		if version.Load() == 3 {
			fmt.Fprint(w, `{"acme":{"name":"Acme","models":{"other":{"name":"Other"}}}}`)
			return
		}
		fmt.Fprintf(w, `{"acme":{"name":"Acme","models":{"test":{"id":"test","name":"Test","canonical_model_id":"acme/test","limit":{"context":200000},"cost":{"input":%d,"output":5,"tiers":[{"tier":{"type":"context","size":200000},"input":10}]}}}}}`, version.Load())
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, err := Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Catalog(ctx)
	if err != nil || len(entries) != 1 || !entries[0].Official {
		t.Fatalf("目录: %+v %v", entries, err)
	}
	m, err := s.Add(ctx, "acme", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Add(ctx, "acme", "test"); err == nil {
		t.Fatal("重复模型应拒绝")
	}
	m.Pricing.Schedules = []Schedule{{Timezone: "Asia/Shanghai", Days: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "12:00", Multiplier: 2}}
	m.Pricing.Baseline = "低谷价格"
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: "本地名称", Pricing: m.Pricing})
	if err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	result, err := s.Sync(ctx)
	if err != nil || result.Updated != 1 {
		t.Fatalf("同步: %+v %v", result, err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m = list[0]
	if m.Pricing.Base[0].Price != 2 || len(m.Pricing.Schedules) != 1 || !contains(m.Overrides, "schedules") {
		t.Fatalf("分组保护错误: %+v", m)
	}
	m.Pricing.Base[0].Price = 9
	m.Pricing.Tiers[0].Rates[0].Price = 99
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.Pricing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m, err = s.get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Pricing.Base[0].Price != 9 || m.Pricing.Tiers[0].Rates[0].Price != 99 {
		t.Fatal("同步覆盖本地规则")
	}
	m.Pricing.Base = m.SourcePricing.Base
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.Pricing, RestoreGroups: []string{"base"}})
	if err != nil || m.Pricing.Base[0].Price != 2 || contains(m.Overrides, "base") {
		t.Fatalf("恢复: %+v %v", m, err)
	}
	if len(m.Pricing.Schedules) != 1 {
		t.Fatal("恢复基础价不能清除分时")
	}
	version.Store(3)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	m, err = s.get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.Pricing, RestoreGroups: []string{"base"}}); err == nil || !strings.Contains(err.Error(), "没有可用的来源值") {
		t.Fatal("下架后恢复应报告无来源值", err)
	}
	version.Store(4)
	if _, err = s.Sync(ctx); err == nil {
		t.Fatal("目录解析失败应报错")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err = s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "本地名称" || list[0].SourceAvailable {
		t.Fatalf("重启保留: %+v %v", list, err)
	}
	if err = s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("删除未持久化")
	}
}

// TestPartialSync 验证单模型非法不阻止其他模型更新，来源移除字段保留本地有效值。
func TestPartialSync(t *testing.T) {
	var updated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if updated.Load() {
			fmt.Fprint(w, `{"acme":{"models":{"a":{"name":"A","cost":{"input":3}},"b":{"name":"B","cost":{"input":"bad"}}}}}`)
		} else {
			fmt.Fprint(w, `{"acme":{"models":{"a":{"name":"A","cost":{"input":1,"output":5}},"b":{"name":"B","cost":{"input":2}}}}}`)
		}
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "data.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err = s.Add(ctx, "acme", id); err != nil {
			t.Fatal(err)
		}
	}
	updated.Store(true)
	r, err := s.Sync(ctx)
	if err != nil || r.Updated != 1 || len(r.Failures) != 1 {
		t.Fatalf("部分同步: %+v %v", r, err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Pricing.Base[0].Price != 3 || list[1].Pricing.Base[0].Price != 2 {
		t.Fatal("部分同步未保持失败模型")
	}
	if len(list[0].Pricing.Base) != 2 {
		t.Fatal("缺失输出字段应保留已有值")
	}
}

// TestValidation 覆盖真实编辑入口的错误单位、时区与重叠时段，及代理错误不落库。
func TestValidation(t *testing.T) {
	for _, p := range []Pricing{{Base: []Rate{{Key: "input", Price: 1, Unit: "image"}}}, {Schedules: []Schedule{{Timezone: "bad", Days: []int{1}, Start: "09:00", End: "12:00", Multiplier: 2}}, Baseline: "低谷"}, {Schedules: []Schedule{{Timezone: "UTC", Days: []int{1}, Start: "09:00", End: "12:00", Multiplier: 2}, {Timezone: "UTC", Days: []int{1}, Start: "11:00", End: "14:00", Multiplier: 2}}, Baseline: "低谷"}} {
		if validatePricing(p) == nil {
			t.Fatalf("非法规则被接受: %+v", p)
		}
	}
	if _, err := proxyTransport(Proxy{Enabled: true, URL: "file:///tmp/test"}); err == nil {
		t.Fatal("非法代理被接受")
	}
	p, err := decodePricing([]byte(`{"input":0}`), nil, "vector")
	if err != nil || len(p.Base) != 1 || p.Base[0].Price != 0 {
		t.Fatal("显式免费价格丢失")
	}
	p, err = decodePricing(nil, nil, "chat")
	if err != nil || len(p.Base) != 0 {
		t.Fatal("未知价格不能补零")
	}
	if _, err = decodePricing([]byte(`{"tiers":[{"tier":{"size":200000},"input":2}]}`), nil, "chat"); err == nil || !strings.Contains(err.Error(), "边界") {
		t.Fatal("不能猜来源阶梯类型")
	}
}

// TestRestoreOnSave 验证恢复草稿随保存解除保护，而恢复后再次编辑仍保留本地修改。
func TestRestoreOnSave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"acme":{"models":{"a":{"name":"A","cost":{"input":1}}}}}`)
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Add(ctx, "acme", "a")
	if err != nil {
		t.Fatal(err)
	}
	m.Pricing.Base = []Rate{{Key: "input", Price: 3, Unit: "million_tokens"}}
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.Pricing})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.SourcePricing, RestoreGroups: []string{"base"}})
	if err != nil || contains(m.Overrides, "base") || m.Pricing.Base[0].Price != 1 {
		t.Fatalf("保存恢复草稿失败: %+v %v", m, err)
	}
	m.Pricing.Base = []Rate{{Key: "input", Price: 4, Unit: "million_tokens"}}
	m, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: m.Pricing, RestoreGroups: []string{"base"}})
	if err != nil || !contains(m.Overrides, "base") || m.Pricing.Base[0].Price != 4 {
		t.Fatalf("恢复后新编辑被覆盖: %+v %v", m, err)
	}
}

// TestGlobalProxy 通过两个真实 HTTP 入口观察代理开关，不以保存配置代替出口验证。
func TestGlobalProxy(t *testing.T) {
	var directHits, proxyHits atomic.Int32
	body := `{"acme":{"models":{"a":{"name":"A","cost":{"input":1}}}}}`
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directHits.Add(1); fmt.Fprint(w, body) }))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if !r.URL.IsAbs() {
			t.Error("代理未收到绝对目标地址")
		}
		fmt.Fprint(w, body)
	}))
	defer proxy.Close()
	s, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), direct.Client(), direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.SetProxy(ctx, Proxy{Enabled: true, URL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if proxyHits.Load() != 1 || directHits.Load() != 0 {
		t.Fatal("目录未走全局代理")
	}
	if err = s.SetProxy(ctx, Proxy{Enabled: false, URL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if directHits.Load() != 1 || proxyHits.Load() != 1 {
		t.Fatal("关闭代理未直连")
	}
}

// TestCompletePricing 通过编辑入口保存完整价格组合，并核对持久化后的单位与规则没有丢失。
func TestCompletePricing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"acme":{"models":{"a":{"name":"A","cost":{"input":1}}}}}`)
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Add(ctx, "acme", "a")
	if err != nil {
		t.Fatal(err)
	}
	factor := 2.0
	pricing := Pricing{Base: []Rate{{Key: "cache_write_1h", Price: 6, Unit: "million_tokens"}}, Tiers: []Tier{{Above: 200000, Rates: []Rate{{Key: "input", Price: 4, Unit: "million_tokens"}}}}, Modes: []Mode{{Name: "fast", Multiplier: &factor}, {Name: "priority", Rates: []Rate{{Key: "input", Price: 3, Unit: "million_tokens"}}, Tiers: []Tier{{Above: 200000, Rates: []Rate{{Key: "input", Price: 6, Unit: "million_tokens"}}}}}}, Baseline: "低谷价格", Schedules: []Schedule{{Timezone: "Asia/Shanghai", Days: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "12:00", Multiplier: 2, ExcludedDates: []string{"2026-10-01"}}}, Images: []ImagePrice{{Size: "1024x1024", Quality: "standard", Price: 0.04}}, Videos: []VideoPrice{{Resolution: "1080p", Price: 0.2}}}
	if _, err = s.Update(ctx, Edit{ID: m.ID, Revision: m.Revision, Name: m.Name, Pricing: pricing}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := list[0].Pricing
	if got.Base[0].Unit != "million_tokens" || got.Images[0].Price != 0.04 || got.Videos[0].Price != 0.2 || got.Modes[1].Tiers[0].Rates[0].Price != 6 || got.Schedules[0].ExcludedDates[0] != "2026-10-01" {
		t.Fatalf("完整规则持久化丢失: %+v", got)
	}
}

// TestEmptyCollections 验证 IPC 的空数组不会把未改动的 Fast 规则误标为本地修改。
func TestEmptyCollections(t *testing.T) {
	factor := 2.0
	a := Pricing{Modes: []Mode{{Name: "fast", Multiplier: &factor}}}
	b := Pricing{Modes: []Mode{{Name: "fast", Multiplier: &factor, Rates: []Rate{}, Tiers: []Tier{}}}}
	if !sameGroup(a, b, "modes") {
		t.Fatal("空集合导致错误的修改保护")
	}
}
