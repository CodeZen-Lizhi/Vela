package records

import (
	"context"
	"math"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// TestQueryStandardCost 验证列表沿用历史价格逐次计价，零倍率、未知及进行中不混淆。
func TestQueryStandardCost(t *testing.T) {
	s, _ := fixture(t)
	now := s.now().UnixMilli()
	pricing := models.Pricing{Base: []models.Rate{{Key: "input", Price: 2, Unit: "million_tokens"}}}
	zero, actual := 0.0, 0.0003
	retryStandard, zeroStandard := 0.0006, 0.0002
	cases := []struct {
		name     string
		record   Record
		standard *float64
	}{
		{"重试分别计价", Record{State: "success", Pricing: pricing, Cost: &actual, Attempts: []Attempt{{Usage: &Usage{Input: 100, Context: 100}}, {Usage: &Usage{Input: 200, Context: 200}}}}, &retryStandard},
		{"零倍率仍有标准费", Record{State: "success", Pricing: pricing, Cost: &zero, Usage: &Usage{Input: 100, Context: 100}}, &zeroStandard},
		{"部分尝试未知不冒充完整费用", Record{State: "failed", Pricing: pricing, Attempts: []Attempt{{Usage: &Usage{Input: 100, Context: 100}}, {Usage: nil}}}, nil},
		{"进行中不显示中间费用", Record{State: "active", Pricing: pricing, Cost: &actual, Usage: &Usage{Input: 100, Context: 100}}, nil},
		{"缺少价格", Record{State: "success", Cost: &actual, Usage: &Usage{Input: 100, Context: 100}}, nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.record
			r.ID = tc.name
			r.StartedAt = now + int64(i)
			if r.State != "active" {
				r.EndedAt = now + int64(i) + 1
			}
			if err := s.save(context.Background(), r, false); err != nil {
				t.Fatal(err)
			}
			page, err := s.Query(context.Background(), Query{Search: tc.name})
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("查询失败: %+v, %v", page, err)
			}
			got := page.Records[0]
			if tc.standard == nil {
				if got.StandardCost != nil {
					t.Fatalf("未知或进行中金额被补齐: %v", *got.StandardCost)
				}
			} else if got.StandardCost == nil || math.Abs(*got.StandardCost-*tc.standard) > 1e-12 {
				t.Fatalf("标准费用错误: %v", got.StandardCost)
			}
			if (got.Cost == nil) != (r.Cost == nil) || got.Cost != nil && *got.Cost != *r.Cost {
				t.Fatal("倍率后费用被改变")
			}

		})
	}
}
