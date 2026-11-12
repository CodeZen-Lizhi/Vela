package providers

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// mappingDocument 读取隔离数据库中的原始映射文档，用于验证迁移不重写无关数据。
func mappingDocument(t *testing.T, f modelFixture, modelID string) string {
	t.Helper()
	var raw string
	if err := f.store.db.QueryRow("SELECT document FROM provider_model_mappings WHERE provider_id=? AND model_id=?", f.provider, modelID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// seedMappingDocument 写入旧版原始 JSON，不通过新保存接口丢弃旧字段。
func seedMappingDocument(t *testing.T, f modelFixture, modelID, raw string) {
	t.Helper()
	if _, err := f.store.db.Exec("INSERT INTO provider_model_mappings(provider_id,model_id,document) VALUES(?,?,?)", f.provider, modelID, raw); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveMappingOperations 验证真实 SQLite 旧数据仅删除范围字段，保留同级顺序、未知字段及关联权限，重复初始化不重写。
func TestRemoveMappingOperations(t *testing.T) {
	f := newModelFixture(t)
	ctx := context.Background()
	if err := f.store.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetModelEnabled(ctx, f.provider, f.ids[0], false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"requestName":"z","priority":2,"operations":["chat"],"future":{"large":9007199254740993}},{"requestName":"a","priority":2,"operations":[]},{"requestName":"first","priority":1,"operations":null}]`
	untouched := `[ { "requestName": "keep", "priority": 7, "future": true } ]`
	seedMappingDocument(t, f, f.ids[0], legacy)
	seedMappingDocument(t, f, f.ids[1], untouched)
	seedMappingDocument(t, f, f.ids[2], `[]`)
	before, err := f.store.Models(ctx, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	// 新类型不暴露旧字段，供迁移前后比较除 operations 外的业务配置。
	if err = f.store.InitModels(); err != nil {
		t.Fatal(err)
	}
	raw := mappingDocument(t, f, f.ids[0])
	want := `[{"requestName":"z","priority":2,"future":{"large":9007199254740993}},{"requestName":"a","priority":2},{"requestName":"first","priority":1}]`
	var gotItems, wantItems []map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &gotItems); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(want), &wantItems); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Fatalf("迁移改变其他字段或顺序: %s", raw)
	}
	after, err := f.store.Models(ctx, f.provider)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("迁移改变关联或映射: %+v %v", after, err)
	}
	assertModelPermissions(t, f.store, f.keys[0], false, f.ids[0], f.ids[1])
	if mappingDocument(t, f, f.ids[1]) != untouched || mappingDocument(t, f, f.ids[2]) != `[]` {
		t.Fatal("未含旧字段的文档被重写")
	}
	// 即使重复初始化，也不能再次更新已经清理过的文档。
	if _, err = f.store.db.Exec(`CREATE TRIGGER reject_mapping_rewrite BEFORE UPDATE ON provider_model_mappings BEGIN SELECT RAISE(ABORT,'unexpected rewrite'); END`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.InitModels(); err != nil {
		t.Fatalf("重复初始化重新写入文档: %v", err)
	}
	if mappingDocument(t, f, f.ids[0]) != raw {
		t.Fatal("重复初始化改变映射")
	}
}

// TestRemoveMappingOperationsInvalidDocument 验证全量文档校验失败时，合法记录也不发生部分清理。
func TestRemoveMappingOperationsInvalidDocument(t *testing.T) {
	for _, invalid := range []string{`{`, `null`, `{}`, `[null]`, `[{"requestName":12}]`, `[{"priority":"1"}]`} {
		t.Run(invalid, func(t *testing.T) {
			f := newModelFixture(t)
			if err := f.store.SaveModels(context.Background(), f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
				t.Fatal(err)
			}
			legacy := `[{"requestName":"keep","priority":3,"operations":["chat"]}]`
			seedMappingDocument(t, f, f.ids[0], legacy)
			seedMappingDocument(t, f, f.ids[1], invalid)
			if err := f.store.InitModels(); err == nil {
				t.Fatal("无效历史文档未阻止初始化")
			}
			if mappingDocument(t, f, f.ids[0]) != legacy || mappingDocument(t, f, f.ids[1]) != invalid {
				t.Fatal("失败初始化改变旧数据")
			}
		})
	}
}

// TestRemoveMappingOperationsWriteRollback 验证第二条写入失败时前一条清理也原子回滚。
func TestRemoveMappingOperationsWriteRollback(t *testing.T) {
	f := newModelFixture(t)
	if err := f.store.SaveModels(context.Background(), f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"requestName":"keep","priority":2,"operations":[]}]`
	for _, id := range f.ids[:2] {
		seedMappingDocument(t, f, id, legacy)
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER reject_second_mapping_cleanup BEFORE UPDATE ON provider_model_mappings WHEN EXISTS(SELECT 1 FROM provider_model_mappings WHERE json_type(document,'$[0].operations') IS NULL) BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.InitModels(); err == nil {
		t.Fatal("写入失败未报告")
	}
	for _, id := range f.ids[:2] {
		if mappingDocument(t, f, id) != legacy {
			t.Fatal("清理失败没有全部回滚")
		}
	}
}
