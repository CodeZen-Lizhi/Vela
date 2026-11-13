package app

import (
	"context"
	"io"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// TestOverviewEncryptedKeyNames 经真实调度结算读取桌面加密配置，不能把 Key 密文交给 SQLite JSON 函数。
func TestOverviewEncryptedKeyNames(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	if _, err = f.execution.Executor.Execute(ctx, f.request(), io.Discard); err != nil {
		t.Fatal(err)
	}
	var encrypted int
	if err = f.catalog.DB().QueryRow(`SELECT COUNT(*) FROM provider_keys WHERE NOT json_valid(document)`).Scan(&encrypted); err != nil || encrypted != len(f.keys) {
		t.Fatal("未保留桌面配置加密条件", encrypted, err)
	}
	service := NewVelaService(Options{Records: history, Providers: f.vendors})
	snapshot, err := service.Overview(ctx)
	if err != nil {
		t.Fatal("加密 Key 配置下总览查询失败", err)
	}
	var logical, attempts float64
	for _, row := range snapshot.Rows {
		if row.KeyID == "" {
			logical += row.Metrics.Requests
			continue
		}
		attempts += row.Metrics.Requests
		if row.KeyName != f.keys[0].Name {
			t.Fatalf("Key 名称不符：%q", row.KeyName)
		}
	}
	if logical != 1 || attempts != 1 {
		t.Fatal("总览遗漏或重复统计", logical, attempts)
	}
	key := f.keys[0]
	if _, err = f.vendors.UpdateKey(ctx, key.ID, providers.ProviderKeyDraft{Name: "改名后的Key", Multiplier: key.Multiplier, Priority: key.Priority, ProbeMinutes: key.ProbeMinutes, Proxy: key.Proxy}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.Overview(ctx)
	if err != nil {
		t.Fatal("改名后读取加密配置失败", err)
	}
	for _, row := range snapshot.Rows {
		if row.KeyID == key.ID && row.KeyName != "改名后的Key" {
			t.Fatal("总览未使用解密后的当前名称")
		}
	}
}
