package providers

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/models"
)

// modelFixture 通过真实模型与供应商入口建立隔离 SQLite 配置。
type modelFixture struct {
	// store 供应商配置入口。
	store *Store
	// models 统一模型入口，与供应商复用同一数据库。
	models *models.Store
	// provider 当前测试供应商稳定身份。
	provider string
	// ids 三个真实统一模型的稳定身份。
	ids []string
	// keys 三把上游 Key，初始均为全部模型模式。
	keys []string
}

// newModelFixture 初始化真实数据库、模型、供应商与 Key，不复制被测保存逻辑。
func newModelFixture(t *testing.T) modelFixture {
	t.Helper()
	ms, err := models.Open(filepath.Join(t.TempDir(), "models.db"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Error(err)
		}
	})
	s, err := New(ms.DB(), &memoryKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InitModels(); err != nil {
		t.Fatal(err)
	}
	if err = s.InitModels(); err != nil {
		t.Fatalf("重复初始化: %v", err)
	}
	p, err := s.Create(context.Background(), validDraft("供应商"))
	if err != nil {
		t.Fatal(err)
	}
	f := modelFixture{store: s, models: ms, provider: p.ID, ids: []string{}, keys: []string{}}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		m, err := ms.Create(context.Background(), models.Create{ModelID: name, Name: name, Kind: "chat"})
		if err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, m.ID)
		key, err := s.CreateKey(context.Background(), p.ID, keyDraft(name, "", 1))
		if err != nil {
			t.Fatal(err)
		}
		f.keys = append(f.keys, key.ID)
	}
	return f
}

// assertModelPermissions 通过公开查询核对权限模式及指定集合。
func assertModelPermissions(t *testing.T, s *Store, key string, all bool, ids ...string) {
	t.Helper()
	p, err := s.ModelPermissions(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	if p.AllModels != all || len(p.ModelIDs) != len(ids) {
		t.Fatalf("权限 = %+v，期望全部=%v 指定=%v", p, all, ids)
	}
	for _, id := range p.ModelIDs {
		if !want[id] {
			t.Fatalf("意外权限: %s", id)
		}
	}
	if p.ModelIDs == nil {
		t.Fatal("空权限必须序列化为数组")
	}
}

// TestModelsAtomicSave 验证关联变更的事务、保留项启停及映射、新增关联不扩大 Key 权限及移除清理。
func TestModelsAtomicSave(t *testing.T) {
	f := newModelFixture(t)
	s := f.store
	ctx := context.Background()
	if err := s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	for _, key := range f.keys[:2] {
		if err := s.SaveModelPermissions(ctx, key, ProviderModelPermissions{ModelIDs: []string{f.ids[0]}}); err != nil {
			t.Fatal(err)
		}
	}
	original := []ProviderModelMappingsDraft{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: " upstream "}}}}
	if err := s.SaveMappings(ctx, f.provider, original); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelEnabled(ctx, f.provider, f.ids[0], false); err != nil {
		t.Fatal(err)
	}
	before, err := s.Models(ctx, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	err = s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: []string{f.ids[1], f.ids[2], "missing"}})
	expectError(t, err, "不存在")
	after, err := s.Models(ctx, f.provider)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("失败事务改变原关联: %+v %v", after, err)
	}
	assertModelPermissions(t, s, f.keys[0], false, f.ids[0])
	if err = s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids}); err != nil {
		t.Fatal(err)
	}
	assertModelPermissions(t, s, f.keys[0], false, f.ids[0])
	assertModelPermissions(t, s, f.keys[1], false, f.ids[0])
	assertModelPermissions(t, s, f.keys[2], true)
	after, err = s.Models(ctx, f.provider)
	if err != nil || after[0].Enabled || after[0].Mappings[0].RequestName != "upstream" {
		t.Fatalf("保留项被覆盖: %+v %v", after, err)
	}
	if err = s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[1:]}); err != nil {
		t.Fatal(err)
	}
	assertModelPermissions(t, s, f.keys[1], false)
	assertModelPermissions(t, s, f.keys[0], false)
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM provider_model_mappings").Scan(&count); err != nil || count != 0 {
		t.Fatalf("移除未清映射: %d %v", count, err)
	}
	// 重新关联是新配置，不恢复已清理权限或映射。
	if err = s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids}); err != nil {
		t.Fatal(err)
	}
	after, err = s.Models(ctx, f.provider)
	if err != nil || !after[0].Enabled || len(after[0].Mappings) != 0 {
		t.Fatalf("重新关联: %+v %v", after, err)
	}
	assertModelPermissions(t, s, f.keys[1], false)
}

// TestModelPermissionModesAndValidation 验证新增关联保留启用和停用 Key 的权限模式，以及非法输入回滚。
func TestModelPermissionModesAndValidation(t *testing.T) {
	f := newModelFixture(t)
	s := f.store
	ctx := context.Background()
	if err := s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:1]}); err != nil {
		t.Fatal(err)
	}
	for _, key := range f.keys[:2] {
		if err := s.SaveModelPermissions(ctx, key, ProviderModelPermissions{ModelIDs: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetKeyEnabled(ctx, f.keys[1], false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	assertModelPermissions(t, s, f.keys[0], false)
	assertModelPermissions(t, s, f.keys[1], false)
	assertModelPermissions(t, s, f.keys[2], true)
	expectError(t, s.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{ModelIDs: []string{f.ids[0], f.ids[2]}}), "未关联")
	assertModelPermissions(t, s, f.keys[0], false)
	expectError(t, s.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{AllModels: true, ModelIDs: f.ids[:1]}), "同时指定")
	expectError(t, s.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{ModelIDs: []string{f.ids[0], f.ids[0]}}), "重复")
	assertModelPermissions(t, s, f.keys[0], false)
	// 新关联的权限只通过独立入口设置，保存后必须能重新读取。
	if err := s.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	assertModelPermissions(t, s, f.keys[0], false, f.ids[0], f.ids[1])
	if err := s.SaveModelPermissions(ctx, f.keys[1], ProviderModelPermissions{AllModels: true}); err != nil {
		t.Fatal(err)
	}
	assertModelPermissions(t, s, f.keys[1], true)
	expectError(t, s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: []string{f.ids[0], f.ids[0]}}), "重复")
	expectError(t, s.SaveModels(ctx, "missing", ProviderModelsDraft{}), "不存在")
	expectError(t, s.SaveModelPermissions(ctx, "missing", ProviderModelPermissions{AllModels: true}), "不存在")
}

// TestMappingsAtomicSaveAndValidation 验证映射工作台跨模型原子保存、校验失败保留及清空恢复默认。
func TestMappingsAtomicSaveAndValidation(t *testing.T) {
	f := newModelFixture(t)
	s := f.store
	ctx := context.Background()
	if err := s.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	original := []ProviderModelMappingsDraft{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: "a"}, {RequestName: "b"}}}, {ModelID: f.ids[1], Mappings: []ProviderModelMapping{{RequestName: "c"}}}}
	if err := s.SaveMappings(ctx, f.provider, original); err != nil {
		t.Fatal(err)
	}
	before, err := s.Models(ctx, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].Mappings[1].Priority != 2 {
		t.Fatal("没有生成连续优先级")
	}
	for _, draft := range [][]ProviderModelMappingsDraft{
		{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: "new"}}}, {ModelID: f.ids[2], Mappings: []ProviderModelMapping{{RequestName: "unlinked"}}}},
		{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: " a "}, {RequestName: "a"}}}},
		{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: " "}}}},
		{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: "a", Priority: -1}}}},
		{{ModelID: f.ids[0]}, {ModelID: f.ids[0]}},
	} {
		if err = s.SaveMappings(ctx, f.provider, draft); err == nil {
			t.Fatal("非法映射被接受")
		}
		after, err := s.Models(ctx, f.provider)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("映射失败未回滚: %+v %v", after, err)
		}
	}
	if err = s.SaveMappings(ctx, f.provider, []ProviderModelMappingsDraft{{ModelID: f.ids[0]}}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Models(ctx, f.provider)
	if err != nil || len(after[0].Mappings) != 0 || !reflect.DeepEqual(after[1], before[1]) {
		t.Fatalf("清空影响其他模型: %+v %v", after, err)
	}
}

// TestUnifiedModelDeleteCascade 验证真实统一模型 Delete 同步清理全部供应商关系，不扩大 Key 权限、不影响其他模型。
func TestUnifiedModelDeleteCascade(t *testing.T) {
	f := newModelFixture(t)
	s := f.store
	ctx := context.Background()
	other, err := s.Create(ctx, validDraft("另一供应商"))
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := s.CreateKey(ctx, other.ID, keyDraft("另一Key", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.provider, other.ID} {
		if err = s.SaveModels(ctx, p, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveMappings(ctx, p, []ProviderModelMappingsDraft{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: "upstream"}}}, {ModelID: f.ids[1], Mappings: []ProviderModelMapping{{RequestName: "preserve"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{f.keys[0], otherKey.ID} {
		if err = s.SaveModelPermissions(ctx, key, ProviderModelPermissions{ModelIDs: f.ids[:1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.models.Delete(ctx, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.provider, other.ID} {
		got, err := s.Models(ctx, p)
		if err != nil || len(got) != 1 || got[0].ModelID != f.ids[1] || got[0].Mappings[0].RequestName != "preserve" {
			t.Fatalf("删除清理不完整: %+v %v", got, err)
		}
	}
	assertModelPermissions(t, s, f.keys[0], false)
	assertModelPermissions(t, s, otherKey.ID, false)
	assertModelPermissions(t, s, f.keys[2], true)
	if err = s.SaveModelPermissions(ctx, f.keys[0], ProviderModelPermissions{ModelIDs: f.ids[1:2]}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteKey(ctx, f.keys[0]); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM provider_key_models WHERE key_id=?", f.keys[0]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("删除Key残留: %d %v", count, err)
	}
	if err = s.Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT COUNT(*) FROM provider_model_mappings WHERE provider_id=?", other.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("删除供应商残留映射: %d %v", count, err)
	}
	remaining, err := f.models.List(ctx)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("删除供应商影响统一模型: %+v %v", remaining, err)
	}
}

// TestModelConfigurationRestart 验证关闭数据库后重新装配仍保留停用状态、指定零权限及有序映射。
func TestModelConfigurationRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	ms, err := models.Open(path, http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ms.DB(), &memoryKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InitModels(); err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(ctx, validDraft("重启"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ms.Create(ctx, models.Create{ModelID: "restart", Name: "重启模型", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateKey(ctx, p.ID, keyDraft("重启Key", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModels(ctx, p.ID, ProviderModelsDraft{ModelIDs: []string{m.ID}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModelPermissions(ctx, k.ID, ProviderModelPermissions{}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetModelEnabled(ctx, p.ID, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMappings(ctx, p.ID, []ProviderModelMappingsDraft{{ModelID: m.ID, Mappings: []ProviderModelMapping{{RequestName: "first"}, {RequestName: "second"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = ms.Close(); err != nil {
		t.Fatal(err)
	}
	ms, err = models.Open(path, http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	s, err = New(ms.DB(), &memoryKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InitModels(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Models(ctx, p.ID)
	if err != nil || len(got) != 1 || got[0].Enabled || len(got[0].Mappings) != 2 || got[0].Mappings[0].RequestName != "first" || got[0].Mappings[1].Priority != 2 {
		t.Fatalf("重启配置: %+v %v", got, err)
	}
	assertModelPermissions(t, s, k.ID, false)
}

// TestResolveModelRequestName 验证优先级、历史同级名称顺序及无映射时默认名回退，不执行网关。
func TestResolveModelRequestName(t *testing.T) {
	cases := []struct {
		// name 描述场景；mappings 为已有映射；want 为期望的请求名。
		name     string
		mappings []ProviderModelMapping
		want     string
	}{
		{"默认", nil, "default"},
		{"空数组回退", []ProviderModelMapping{}, "default"},
		{"优先级优先于列表位置", []ProviderModelMapping{{RequestName: "later", Priority: 3}, {RequestName: "selected", Priority: 1}, {RequestName: "middle", Priority: 2}}, "selected"},
		{"同级名称排序", []ProviderModelMapping{{RequestName: "z", Priority: 1}, {RequestName: "a", Priority: 1}}, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]ProviderModelMapping(nil), tc.mappings...)
			got := ResolveModelRequestName("default", tc.mappings)
			if got != tc.want {
				t.Fatalf("解析 = %q，期望 %q", got, tc.want)
			}
			for i := range before {
				if before[i] != tc.mappings[i] {
					t.Fatal("解析改变原始映射顺序")
				}
			}
		})
	}
}
