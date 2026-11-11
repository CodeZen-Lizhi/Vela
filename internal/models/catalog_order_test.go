package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
)

// TestCatalogReleaseOrder 经真实 HTTP 和目录入口验证新模型优先、年月日期、未知日期及同日稳定排序。
func TestCatalogReleaseOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"openai":{"name":"OpenAI","models":{
				"old":{"name":"A old","release_date":"2024-05-13","canonical_model_id":"openai/old"},
				"new":{"name":"Z new","release_date":"2026-10-26","canonical_model_id":"openai/new"},
				"month":{"name":"Month","release_date":"2026-09"},
				"tie-b":{"name":"Same","release_date":"2026-08-01"},
				"tie-a":{"name":"Same","release_date":"2026-08-01"},
				"unknown":{"name":"Unknown","last_updated":"2099-01-01"},
				"invalid":{"name":"Invalid","release_date":"2099-13-40"}
			}},
			"other":{"name":"Other","models":{
				"newest":{"name":"Newest","release_date":"2026-11-01"},
				"official-tie":{"name":"AAA","release_date":"2026-10-26","canonical_model_id":"other/official-tie"},
				"reseller-tie":{"name":"AAA","release_date":"2026-10-26"}
			}}
		}`)
	}))
	defer server.Close()
	store, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got, openai []string
	for _, entry := range entries {
		got = append(got, entry.ModelID)
		if entry.ProviderID == "openai" {
			openai = append(openai, entry.ModelID)
		}
	}
	want := []string{"newest", "official-tie", "new", "reseller-tie", "month", "tie-a", "tie-b", "old", "invalid", "unknown"}
	if !slices.Equal(got, want) {
		t.Fatalf("目录发布日期顺序错误: got %v, want %v", got, want)
	}
	if !slices.Equal(openai, []string{"new", "month", "tie-a", "tie-b", "old", "invalid", "unknown"}) {
		t.Fatalf("厂商筛选后顺序错误: %v", openai)
	}
}
