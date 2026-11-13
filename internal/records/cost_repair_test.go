package records

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// TestRepairMissingCost 验证升级后恢复可选缓存时长未发生的旧请求，并同步明细、长期累计与概览。
func TestRepairMissingCost(t *testing.T) {
	s, catalog := fixture(t)
	one := 0.03
	started := s.now().UnixMilli()
	usage := &Usage{Input: 1938, Output: 938, CacheRead: 2458, Context: 4396}
	pricing := models.Pricing{Base: []models.Rate{
		{Key: "input", Price: 2, Unit: "million_tokens"},
		{Key: "output", Price: 10, Unit: "million_tokens"},
		{Key: "cache_read", Price: 0.1, Unit: "million_tokens"},
		{Key: "cache_write", Price: 2.5, Unit: "million_tokens"},
		{Key: "cache_write_12h", Price: 5, Unit: "million_tokens"},
	}}
	r := Record{
		ID: "repair-cost", StartedAt: started, EndedAt: started + 1,
		ModelID: "m1", Model: "logical", ModelName: "测试模型", ProviderID: "p1", Protocol: "chat",
		State: "success", Usage: usage, Pricing: pricing, UnknownCosts: 1,
		Attempts: []Attempt{{
			Target: Target{ProviderID: "p1", KeyID: "k1", KeyName: "测试 Key", Multiplier: &one},
			Number: 1, Status: 200, Usage: usage,
		}},
	}
	if err := s.save(context.Background(), r, true); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DELETE FROM settings WHERE name='records-cost-repair-v1'`,
		`DELETE FROM settings WHERE name='records-overview-v2'`,
		`DELETE FROM request_overview`,
	} {
		if _, err := catalog.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	repaired, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	record, err := repaired.Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	const standard, actual = 0.0135018, 0.000405054
	if record.Cost == nil || math.Abs(*record.Cost-actual) > 1e-12 ||
		record.Attempts[0].Cost == nil || math.Abs(*record.Attempts[0].Cost-actual) > 1e-12 ||
		record.UnknownCosts != 0 || len(record.Charges) != 3 {
		t.Fatalf("记录费用未恢复: %+v", record)
	}
	page, err := repaired.Query(context.Background(), Query{Search: r.ID})
	if err != nil || len(page.Records) != 1 {
		t.Fatal(page, err)
	}
	listed := page.Records[0]
	if listed.StandardCost == nil || math.Abs(*listed.StandardCost-standard) > 1e-12 ||
		listed.Cost == nil || math.Abs(*listed.Cost-actual) > 1e-12 {
		t.Fatalf("列表费用未恢复: standard=%v actual=%v", listed.StandardCost, listed.Cost)
	}
	var totalCost float64
	var unknownCost int
	if err = catalog.DB().QueryRow(`SELECT cost,unknown_cost FROM request_totals WHERE model_id='m1' AND provider_id='p1'`).Scan(&totalCost, &unknownCost); err != nil ||
		math.Abs(totalCost-actual) > 1e-12 || unknownCost != 0 {
		t.Fatal("请求累计未修复", totalCost, unknownCost, err)
	}
	keys, err := repaired.KeyTotals(context.Background(), Query{})
	if err != nil || len(keys) != 1 || math.Abs(keys[0].Cost-actual) > 1e-12 || keys[0].UnknownCost != 0 {
		t.Fatal("Key 累计未修复", keys, err)
	}
	_, logical, _ := overviewSums(t, repaired)
	if math.Abs(logical.Cost-actual) > 1e-12 || math.Abs(logical.StandardCost-standard) > 1e-12 ||
		logical.UnknownCost != 0 || logical.UnknownStandard != 0 {
		t.Fatalf("概览未修复: %+v", logical)
	}
	if _, err = repaired.Get(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = New(catalog.DB()); err != nil {
		t.Fatal(err)
	}
	if err = catalog.DB().QueryRow(`SELECT cost,unknown_cost FROM request_totals WHERE model_id='m1' AND provider_id='p1'`).Scan(&totalCost, &unknownCost); err != nil ||
		math.Abs(totalCost-actual) > 1e-12 || unknownCost != 0 {
		t.Fatal("重复修复改变累计", totalCost, unknownCost, err)
	}
	var marked string
	if err = catalog.DB().QueryRow(`SELECT document FROM settings WHERE name='records-cost-repair-v1'`).Scan(&marked); err != nil || marked != "done" {
		t.Fatal("费用修复未幂等完成", marked, err)
	}
}

// TestRepairMissingTotalsRollsBack 验证缺失任一累计行时全部费用调整回滚且不写完成标记，补回累计后可安全重试。
func TestRepairMissingTotalsRollsBack(t *testing.T) {
	for _, table := range []string{"request_key_totals", "request_totals"} {
		t.Run(table, func(t *testing.T) {
			s, catalog := fixture(t)
			ctx := context.Background()
			one := 1.0
			usage := &Usage{Input: 1000, Context: 1000}
			for _, id := range []string{"repair-a", "repair-b"} {
				r := Record{ID: id, StartedAt: s.now().UnixMilli(), EndedAt: s.now().UnixMilli() + 1,
					ModelID: "m1", ProviderID: id, Protocol: "chat", State: "success", Usage: usage, UnknownCosts: 1,
					Pricing:  models.Pricing{Base: []models.Rate{{Key: "input", Price: 2, Unit: "million_tokens"}}},
					Attempts: []Attempt{{Target: Target{ProviderID: id, KeyID: id, Multiplier: &one}, Number: 1, Status: 200, Usage: usage}}}
				if err := s.save(ctx, r, true); err != nil {
					t.Fatal(err)
				}
			}
			// 表名来自上面的固定测试列表；备份缺失行以验证修复后的重试，不从明细猜测长期累计。
			for _, query := range []string{
				`DELETE FROM settings WHERE name='records-cost-repair-v1'`,
				`CREATE TEMP TABLE missing_total AS SELECT * FROM ` + table + ` WHERE provider_id='repair-b'`,
				`DELETE FROM ` + table + ` WHERE provider_id='repair-b'`,
			} {
				if _, err := catalog.DB().Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := New(catalog.DB()); err == nil || !strings.Contains(err.Error(), "累计") {
				t.Fatalf("缺失累计仍完成费用修复: %v", err)
			}
			for _, id := range []string{"repair-a", "repair-b"} {
				var raw string
				var cost any
				if err := catalog.DB().QueryRow(`SELECT document,cost FROM request_records WHERE id=?`, id).Scan(&raw, &cost); err != nil {
					t.Fatal(err)
				}
				var r Record
				if err := json.Unmarshal([]byte(raw), &r); err != nil || cost != nil || r.Cost != nil || r.Attempts[0].Cost != nil {
					t.Fatalf("修复失败未回滚明细 %s: %+v, %v", id, r, err)
				}
			}
			var marked int
			if err := catalog.DB().QueryRow(`SELECT COUNT(*) FROM settings WHERE name=?`, costRepairMarker).Scan(&marked); err != nil || marked != 0 {
				t.Fatal("失败仍写完成标记", marked, err)
			}
			for _, totals := range []string{"request_key_totals", "request_totals"} {
				var cost float64
				var unknown int
				if err := catalog.DB().QueryRow(`SELECT cost,unknown_cost FROM `+totals+` WHERE provider_id='repair-a'`).Scan(&cost, &unknown); err != nil || cost != 0 || unknown != 1 {
					t.Fatal("修复失败未回滚累计", totals, cost, unknown, err)
				}
			}
			if _, err := catalog.DB().Exec(`INSERT INTO ` + table + ` SELECT * FROM missing_total`); err != nil {
				t.Fatal(err)
			}
			if _, err := New(catalog.DB()); err != nil {
				t.Fatal("补齐后不能重试", err)
			}
			if _, err := New(catalog.DB()); err != nil {
				t.Fatal(err)
			}
			for _, totals := range []string{"request_key_totals", "request_totals"} {
				var cost float64
				var unknown int
				if err := catalog.DB().QueryRow(`SELECT SUM(cost),SUM(unknown_cost) FROM `+totals).Scan(&cost, &unknown); err != nil || math.Abs(cost-0.004) > 1e-12 || unknown != 0 {
					t.Fatal("重试费用错误或重复累加", totals, cost, unknown, err)
				}
			}
		})
	}
}

// TestRepairPreservesMixedMediaCacheUnknown 验证历史重读后仍能识别混合媒体缓存，不把未知费用修复成错误金额。
func TestRepairPreservesMixedMediaCacheUnknown(t *testing.T) {
	s, catalog := fixture(t)
	one := 1.0
	started := s.now().UnixMilli()
	usage := parseUsage([]byte(`{"usage":{"prompt_tokens":30,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":20,"image_tokens":10}}}`), "chat")
	if usage == nil || usage.Input != 10 || usage.CacheRead != 20 || usage.ImageInput != 10 {
		t.Fatalf("测试用量无效: %+v", usage)
	}
	pricing := models.Pricing{Base: []models.Rate{
		{Key: "input", Price: 2, Unit: "million_tokens"},
		{Key: "output", Price: 10, Unit: "million_tokens"},
		{Key: "cache_read", Price: 0.1, Unit: "million_tokens"},
	}}
	r := Record{
		ID: "mixed-media-cache", StartedAt: started, EndedAt: started + 1,
		ModelID: "m1", Model: "logical", ModelName: "测试模型", ProviderID: "p1", Protocol: "chat",
		State: "success", Usage: usage, Pricing: pricing, UnknownCosts: 1,
		Attempts: []Attempt{{
			Target: Target{ProviderID: "p1", KeyID: "k1", KeyName: "测试 Key", Multiplier: &one},
			Number: 1, Status: 200, Usage: usage,
		}},
	}
	if err := s.save(context.Background(), r, true); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DELETE FROM settings WHERE name='records-cost-repair-v1'`,
		`DELETE FROM settings WHERE name='records-overview-v2'`,
		`DELETE FROM request_overview`,
	} {
		if _, err := catalog.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	repaired, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	record, err := repaired.Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Cost != nil || record.UnknownCosts != 1 || record.Attempts[0].Cost != nil {
		t.Fatalf("混合媒体缓存被错误计价: cost=%v unknown=%d attempt=%v", record.Cost, record.UnknownCosts, record.Attempts[0].Cost)
	}
	page, err := repaired.Query(context.Background(), Query{Search: r.ID})
	if err != nil || len(page.Records) != 1 || page.Records[0].StandardCost != nil {
		t.Fatalf("列表标准费用未保持未知: %+v, %v", page.Records, err)
	}
	var cost float64
	var unknownCost int
	if err = catalog.DB().QueryRow(`SELECT cost,unknown_cost FROM request_totals WHERE model_id='m1' AND provider_id='p1'`).Scan(&cost, &unknownCost); err != nil ||
		cost != 0 || unknownCost != 1 {
		t.Fatal("请求累计错误", cost, unknownCost, err)
	}
	keys, err := repaired.KeyTotals(context.Background(), Query{})
	if err != nil || len(keys) != 1 || keys[0].Cost != 0 || keys[0].UnknownCost != 1 {
		t.Fatal("Key 累计错误", keys, err)
	}
	_, logical, _ := overviewSums(t, repaired)
	if logical.Cost != 0 || logical.StandardCost != 0 || logical.UnknownCost != 1 || logical.UnknownStandard != 1 {
		t.Fatalf("概览错误: %+v", logical)
	}
}
