package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// TestSchedulingOrderLifecycle 经真实供应商操作验证创建顺序、原子排序、停用保留、删除和失败回退。
func TestSchedulingOrderLifecycle(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	create := func(name string) providers.Provider {
		t.Helper()
		p, err := f.vendors.Create(ctx, providers.ProviderDraft{Name: name, URL: "http://127.0.0.1:18081", Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	b, c := create("B"), create("C")
	policy, err := f.policy.Policy(ctx)
	if err != nil || !reflect.DeepEqual(policy.ProviderOrder, []string{f.provider.ID, b.ID, c.ID}) {
		t.Fatalf("创建顺序: %+v %v", policy, err)
	}
	want := []string{c.ID, f.provider.ID, b.ID}
	if err = f.policy.SaveProviderOrder(ctx, want); err != nil {
		t.Fatal(err)
	}
	saved, _ := f.policy.Policy(ctx)
	if saved.Revision != policy.Revision+1 {
		t.Fatalf("真实改动必须递增修订: %+v", saved)
	}
	if err = f.policy.SaveProviderOrder(ctx, want); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := f.policy.Policy(ctx)
	if unchanged.Revision != saved.Revision {
		t.Fatal("无变化不应失效亲和")
	}
	for _, ids := range [][]string{{c.ID, f.provider.ID}, {c.ID, c.ID, b.ID}, {c.ID, f.provider.ID, "unknown"}} {
		if err = f.policy.SaveProviderOrder(ctx, ids); err == nil {
			t.Fatalf("非法顺序被接受: %v", ids)
		}
	}
	service := NewVelaService(Options{Providers: f.vendors, Scheduling: f.policy})
	snapshot, err := service.SchedulingSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range want {
		if snapshot.Providers[i].Provider.ID != id {
			t.Fatalf("调度页顺序: %+v", snapshot)
		}
	}
	if err = f.vendors.SetEnabled(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.SchedulingSnapshot(ctx)
	if err != nil || snapshot.Providers[0].Provider.Enabled || snapshot.Providers[0].Provider.ID != c.ID {
		t.Fatalf("停用项应保留位置: %+v %v", snapshot, err)
	}
	vendorList, _ := service.Providers(ctx)
	if vendorList.Providers[0].Provider.ID != f.provider.ID || vendorList.Providers[2].Provider.ID != c.ID {
		t.Fatalf("供应商列表启用/顺位组合: %+v", vendorList)
	}
	if err = f.vendors.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := scheduling.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = reopened.Policy(ctx)
	if !reflect.DeepEqual(saved.ProviderOrder, []string{c.ID, f.provider.ID}) {
		t.Fatalf("删除或重读改变相对顺序: %+v", saved)
	}
	if _, err = f.catalog.DB().Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if err = reopened.SaveProviderOrder(ctx, []string{f.provider.ID, c.ID}); err == nil {
		t.Fatal("只读写入未失败")
	}
	after, _ := reopened.Policy(ctx)
	if !reflect.DeepEqual(saved, after) {
		t.Fatalf("失败改变配置: %+v", after)
	}
	if _, err = f.catalog.DB().Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
}

// TestSchedulingImportAtomicity 验证导入非法顺序不会只写入策略，合法导入后两者一起重读。
func TestSchedulingImportAtomicity(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	before, _ := f.policy.Policy(ctx)
	if err := f.policy.ImportPolicy(ctx, scheduling.SchedulingPolicy{Strategy: scheduling.CostBased, ProviderOrder: []string{"missing"}, Revision: 999}); err == nil {
		t.Fatal("非法导入未拒绝")
	}
	after, _ := f.policy.Policy(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("非法顺序污染策略: %+v", after)
	}
	if err := f.policy.ImportPolicy(ctx, scheduling.SchedulingPolicy{Strategy: scheduling.CostBased, ProviderOrder: before.ProviderOrder, Revision: 999}); err != nil {
		t.Fatal(err)
	}
	after, _ = f.policy.Policy(ctx)
	if after.Strategy != scheduling.CostBased || after.Revision != before.Revision+1 {
		t.Fatalf("导入未同时生效或信任外部修订: %+v", after)
	}
}
