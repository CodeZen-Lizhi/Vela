package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

// TestCatalogDeprecated 通过真实 HTTP 与 SQLite 验证弃用过滤、候选失效和已有模型继续同步。
func TestCatalogDeprecated(t *testing.T) {
	var deprecated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := ""
		if deprecated.Load() {
			status = "deprecated"
		}
		fmt.Fprintf(w, `{"openai":{"name":"OpenAI","models":{
			"existing":{"name":"Existing","status":%q,"cost":{"input":2}},
			"retired":{"name":"Retired","status":"deprecated"},
			"alpha":{"name":"Alpha","status":"alpha"},
			"beta":{"name":"Beta","status":"beta"},
			"unknown":{"name":"Unknown"}
		}}}`, status)
	}))
	defer server.Close()
	store, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	entries, err := store.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range entries {
		ids = append(ids, entry.ModelID)
	}
	if !slices.Equal(ids, []string{"alpha", "beta", "existing", "unknown"}) {
		t.Fatalf("弃用过滤错误: %v", ids)
	}
	model, err := store.Add(ctx, "openai", "existing")
	if err != nil {
		t.Fatal(err)
	}
	deprecated.Store(true)
	entries, err = store.Catalog(ctx)
	if err != nil || len(entries) != 3 {
		t.Fatalf("新弃用模型未从候选移除: %+v, %v", entries, err)
	}
	if _, err = store.Add(ctx, "openai", "existing"); err == nil {
		t.Fatal("弃用模型不应仍是添加候选")
	}
	result, err := store.Sync(ctx)
	if err != nil || result.Updated != 1 || len(result.Failures) != 0 {
		t.Fatalf("弃用不应阻断已有模型同步: %+v, %v", result, err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != model.ID || !list[0].SourceAvailable {
		t.Fatalf("已有模型或来源状态被改变: %+v, %v", list, err)
	}
}

// TestCatalogAllDeprecated 全部模型弃用时返回正常空候选，不报成目录下载失败。
func TestCatalogAllDeprecated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"openai":{"models":{"old":{"name":"Old","status":"deprecated"}}}}`)
	}))
	defer server.Close()
	store, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.Catalog(context.Background())
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("全部弃用时应为空候选: %+v, %v", entries, err)
	}
}
