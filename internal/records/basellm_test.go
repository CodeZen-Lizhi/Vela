package records

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// TestBasellmScheduleEstimation 经公开目录入口将来源规则送入真实计费函数，核验峰谷边界、周末和缓存单价。
func TestBasellmScheduleEstimation(t *testing.T) {
	data, err := os.ReadFile("../models/testdata/basellm.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	store, err := models.Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pricing models.Pricing
	for _, entry := range entries {
		if entry.ModelID == "deepseek-flash" {
			if entry.Problem != "" {
				t.Fatal(entry.Problem)
			}
			pricing = entry.Pricing
		}
	}
	factor := 1.0
	// 内部用量的 Input 已扣除缓存，Context 仍包含全部输入。
	usage := &Usage{Input: 900, Context: 1000, Output: 100, CacheRead: 100}
	for _, tc := range []struct {
		// At 为请求开始时刻，Want 为来源价目表手工核对的美元总额。
		at   string
		want float64
	}{
		{"2026-11-13T08:59:00+08:00", .0001953},
		{"2026-11-13T09:00:00+08:00", .0003906},
		{"2026-11-13T12:00:00+08:00", .0001953},
		{"2026-11-13T14:00:00+08:00", .0003906},
		{"2026-11-13T18:00:00+08:00", .0001953},
		{"2026-11-14T09:00:00+08:00", .0001953},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := estimate(pricing, usage, &factor, "standard", at)
		if got == nil || math.Abs(*got-tc.want) > 1e-12 {
			t.Fatalf("%s: got %v want %.7f", tc.at, got, tc.want)
		}
	}
}
