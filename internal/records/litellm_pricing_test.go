package records

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// TestLiteLLMPriceChain 通过真实目录HTTP、创建、持久来源和实际估价验证分时与多阶梯/模式链路。
func TestLiteLLMPriceChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
 "deepseek/deepseek-flash":{"litellm_provider":"deepseek","mode":"chat","input_cost_per_token":0.0000003,"output_cost_per_token":0.0000012,"cache_read_input_token_cost":0.000000006,"off_peak_pricing":{"input_cost_per_token":0.00000015,"output_cost_per_token":0.0000006,"cache_read_input_token_cost":0.000000003,"windows":[{"hours_utc":["00:00-01:00","04:00-06:00","10:00-00:00"],"weekdays":[1,2,3,4,5]},{"hours_utc":"00:00-00:00","weekdays":[6,7]}]}},
 "openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000002,"output_cost_per_token":0.000010,"input_cost_per_token_above_200k_tokens":0.000004,"output_cost_per_token_above_200k_tokens":0.000015,"input_cost_per_token_above_500k_tokens":0.000008,"output_cost_per_token_above_500k_tokens":0.000030,"input_cost_per_token_priority":0.000004,"output_cost_per_token_priority":0.000020,"input_cost_per_token_above_200k_tokens_priority":0.000008,"output_cost_per_token_above_200k_tokens_priority":0.000030}
 }`)
	}))
	defer server.Close()
	ctx := context.Background()
	s, err := models.Open(filepath.Join(t.TempDir(), "chain.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ConfigureCatalogUpdates(false, 10)
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	flash, err := s.Add(ctx, "deepseek", "deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	one := 1.0
	u := &Usage{Input: 1000000, Output: 1000000, CacheRead: 1000000, Context: 2000000}
	peak := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	off := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)
	weekend := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		at   time.Time
		want float64
	}{{peak, 1.506}, {off, .753}, {weekend, .753}} {
		got, _ := estimate(flash.Pricing, u, &one, "", tt.at)
		if got == nil || math.Abs(*got-tt.want) > 1e-9 {
			t.Fatalf("%v cost %v want %v", tt.at, got, tt.want)
		}
	}
	m, err := s.Add(ctx, "openai", "test")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := estimate(m.Pricing, &Usage{Input: 600000, Context: 600000}, &one, "", peak)
	if got == nil || math.Abs(*got-4.8) > 1e-9 {
		t.Fatal("multiple tiers not applied", got)
	}
	got, _ = estimate(m.Pricing, &Usage{Input: 300000, Context: 300000}, &one, "priority", peak)
	if got == nil || math.Abs(*got-2.4) > 1e-9 {
		t.Fatal("mode tier not applied", got)
	}
}

// TestLiteUnmeteredDimensions 通过真实解析和估价入口核验未完整计量的媒体、缓存维度不返回部分金额。
func TestLiteUnmeteredDimensions(t *testing.T) {
	one := 1.0
	for _, field := range []string{"input_cost_per_video_token", "output_cost_per_video_token", "input_cost_per_image", "input_cost_per_second", "cache_creation_input_token_cost_above_12hr", "cache_creation_input_token_cost_above_30min", "cache_read_input_audio_token_cost", "cache_read_input_image_token_cost", "cache_creation_input_audio_token_cost"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"openai/test":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000002,"output_cost_per_token":0.000004,"cache_creation_input_token_cost":0.000001,"output_cost_per_image":0.04,%q:0.00001}}`, field)
			}))
			defer server.Close()
			s, err := models.Open(filepath.Join(t.TempDir(), "unmetered.sqlite"), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.ConfigureCatalogUpdates(false, 10)
			entries, err := s.Catalog(context.Background())
			if err != nil || len(entries) != 1 || len(entries[0].Warnings) == 0 {
				t.Fatalf("missing imported usage warning: %+v, %v", entries, err)
			}
			for _, usage := range []*Usage{{Input: 100, Output: 10, Context: 100, CacheWrite: 20}, {Quantity: 1, Unit: "image"}} {
				cost, charges := estimate(entries[0].Pricing, usage, &one, "", time.Now())
				if cost != nil || len(charges) != 0 {
					t.Fatalf("incomplete cost returned as complete: %v, %+v", cost, charges)
				}
			}
		})
	}
}
