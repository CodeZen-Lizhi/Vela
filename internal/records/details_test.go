package records

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetailArchiveLifecycle 验证旧正文迁移、独立清理、停存和摘要降级。
func TestDetailArchiveLifecycle(t *testing.T) {
	legacy, catalog := fixture(t)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "retained")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","message":"try again later"}}`)
	}))
	defer upstream.Close()
	old, _ := exchange(t, legacy, upstream.URL, "chat", false)
	if _, err := legacy.db.Exec(`UPDATE request_payloads SET document=json_remove(document,'$.bodyState') WHERE request_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "details")
	store, err := NewWithDetails(catalog.DB(), root)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Get(ctx, old.ID)
	if err != nil || migrated.Client == nil || migrated.Client.BodyState != "available" || migrated.Client.RequestBody != old.Client.RequestBody {
		t.Fatalf("旧正文迁移不一致: %v", err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("正文目录权限: %v %v", info, err)
	}
	var stored string
	if err = store.db.QueryRow(`SELECT document FROM request_payloads WHERE request_id=? AND number=0`, old.ID).Scan(&stored); err != nil || strings.Contains(stored, "hello") {
		t.Fatalf("旧正文仍在 SQLite: %v", err)
	}
	fresh, _ := exchange(t, store, upstream.URL, "chat", false)
	if fresh.Client == nil || fresh.Client.RequestBody == "" || len(fresh.Attempts) != 1 || !strings.Contains(fresh.Attempts[0].Error, "rate_limit_exceeded") {
		t.Fatal("新正文或独立错误摘要缺失")
	}
	before, err := store.Preview(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	details, err := store.PreviewDetails(ctx, 0)
	if err != nil || details.Count != 2 {
		t.Fatalf("正文预览: %+v %v", details, err)
	}
	if count, err := store.CleanupDetails(ctx, details.Before); err != nil || count != 2 {
		t.Fatalf("正文清理: %d %v", count, err)
	}
	after, err := store.Get(ctx, fresh.ID)
	if err != nil || after.Client == nil || after.Client.BodyState != "cleared" || !strings.Contains(after.Attempts[0].Payload.ResponseHeaders, "retained") || !strings.Contains(after.Attempts[0].Error, "try again later") {
		t.Fatalf("正文清理后详情降级失败: %v", err)
	}
	if count, err := store.Preview(ctx, 0); err != nil || count.Count != before.Count {
		t.Fatalf("正文清理改变了明细: %+v %v", count, err)
	}
	fresh, _ = exchange(t, store, upstream.URL, "chat", false)
	records, err := store.Preview(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cleanup(ctx, records.Before); err != nil {
		t.Fatal(err)
	}
	if _, err = store.archive.read(fresh.ID, fresh.EndedAt); err != nil {
		t.Fatalf("清理明细删除了正文: %v", err)
	}
	if err = store.SetRetention(ctx, true, 0, 0); err != nil {
		t.Fatal(err)
	}
	if days, err := store.Retention(ctx, true); err != nil || days != 0 {
		t.Fatalf("正文期限未关闭: %d %v", days, err)
	}
	if days, err := store.detailExpiryDays(ctx); err != nil || days != 7 {
		t.Fatalf("关闭采集改变既有正文期限: %d %v", days, err)
	}
	if err = store.SetRetention(ctx, true, 1, 0); err == nil {
		t.Fatal("重新启用较短正文期限前未要求预览")
	}
	preview, err := store.PreviewDetails(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetRetention(ctx, true, 1, preview.Before); err != nil {
		t.Fatalf("确认预览后重新启用正文期限失败: %v", err)
	}
	fresh, _ = exchange(t, store, upstream.URL, "chat", false)
	if fresh.Client == nil || fresh.Client.RequestBody == "" || fresh.Client.BodyState != "available" {
		t.Fatal("重新启用后未保存正文")
	}
}

// TestDetailArchiveFailureKeepsRequest 验证正文写失败不会丢掉调用摘要与错误原因。
func TestDetailArchiveFailureKeepsRequest(t *testing.T) {
	legacy, catalog := fixture(t)
	root := filepath.Join(t.TempDir(), "details")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewWithDetails(catalog.DB(), root)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","message":"try again later"}}`)
	}))
	defer upstream.Close()
	record, _ := exchange(t, store, upstream.URL, "chat", false)
	if record.Client == nil || record.Client.BodyState != "saveFailed" || len(record.Attempts) != 1 || !strings.Contains(record.Attempts[0].Error, "try again later") {
		t.Fatalf("正文写入失败影响了可用明细: %+v", record)
	}
	var count int
	if err = legacy.db.QueryRow(`SELECT COUNT(*) FROM request_totals`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("正文写入失败影响了长期累计: %d %v", count, err)
	}
}
