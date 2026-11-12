package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestCreate 验证确认草稿原子保存、本地与来源模型 ID 分离、来源保护、手动模型隔离及重复拒绝。
func TestCreate(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, ` {"acme":{"name":"Acme","models":{"test":{"name":"Test","canonical_model_id":"acme/test","cost":{"input":1,"output":5}}}}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, err := Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	draft := Create{ProviderID: "acme", ModelID: "local-test", SourceModelID: "test", Name: "确认名称", Pricing: Pricing{Base: []Rate{{Key: "input", Price: 3, Unit: "million_tokens"}}}}
	m, err := s.Create(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if m.ModelID != "local-test" || m.SourceModelID != "test" || m.Name != draft.Name || m.SourcePricing.Base[0].Price != 1 || m.Pricing.Base[0].Price != 3 || !contains(m.Overrides, "base") {
		t.Fatalf("确认来源或保护错误: %+v", m)
	}
	if _, err = s.Create(ctx, draft); err == nil {
		t.Fatal("重复创建应拒绝")
	}
	manual, err := s.Create(ctx, Create{ModelID: "custom-model", Name: "手动模型", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if manual.ProviderID != "" || manual.SourceAvailable || len(manual.Pricing.Base) != 0 {
		t.Fatalf("手动来源错误: %+v", manual)
	}
	result, err := s.Sync(ctx)
	if err != nil || len(result.Failures) != 0 || result.Updated != 1 {
		t.Fatalf("仅目录模型应按来源 ID 同步: %+v %v", result, err)
	}
	if _, err = s.Update(ctx, Edit{ID: manual.ID, Revision: manual.Revision, Name: manual.Name, Pricing: manual.Pricing, RestoreGroups: []string{"base"}}); err == nil {
		t.Fatal("无来源恢复应报错")
	}
	if _, err = s.Create(ctx, Create{ModelID: "bad", Name: "", Kind: "chat"}); err == nil {
		t.Fatal("空名称应拒绝")
	}
	if _, err = s.Create(ctx, Create{ModelID: "bad", Name: "bad", Kind: "other"}); err == nil {
		t.Fatal("错误类型应拒绝")
	}
	if _, err = s.Create(ctx, Create{ModelID: "bad id", Name: "bad", Kind: "chat"}); err == nil {
		t.Fatal("含空白的模型 ID 应拒绝")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("重启持久化: %+v %v", list, err)
	}
	for _, saved := range list {
		if saved.ID == m.ID && (saved.ModelID != "local-test" || saved.SourceModelID != "test" || saved.Pricing.Base[0].Price != 3 || saved.SourcePricing.Base[0].Price != 1) {
			t.Fatal("确认草稿未持久化")
		}
	}
}
