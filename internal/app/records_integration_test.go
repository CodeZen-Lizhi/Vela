package app

import (
	"bytes"
	"context"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"io"
	"testing"
)

// TestRequestRecordsSchedulingIntegration 验证真实调度重试和转移只产生一个逻辑记录，保留各次上游错误。
func TestRequestRecordsSchedulingIntegration(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	f.fail = true
	var output bytes.Buffer
	if _, err = f.execution.Executor.Execute(ctx, f.request(), &output); err != nil {
		t.Fatal(err)
	}
	page, err := history.Query(ctx, records.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Summary.Total != 1 || len(page.Records) != 1 {
		t.Fatal("逻辑请求重复", page.Summary)
	}
	detail, err := history.Get(ctx, page.Records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 3 || detail.Execution != "both" || detail.State != "success" {
		t.Fatalf("链路不符: %+v", detail)
	}
	if detail.Attempts[0].Status != 502 || detail.Attempts[0].Payload == nil || detail.Attempts[0].Payload.ResponseBody == "" {
		t.Fatal("原始上游错误丢失")
	}
	if detail.Cost != nil || detail.Usage != nil {
		t.Fatal("缺失用量被伪造")
	}
}

// TestRequestRecordsCircuitSource 验证熔断被排序阶段过滤时，未发上游记录仍关联真实触发请求。
func TestRequestRecordsCircuitSource(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	f.fail = true
	if err = f.vendors.SetKeyEnabled(ctx, f.keys[1].ID, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		_, _ = f.execution.Executor.Execute(ctx, f.request(), io.Discard)
	}
	page, err := history.Query(ctx, records.Query{States: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) == 0 {
		t.Fatal("没有产生熔断拦截记录")
	}
	local, err := history.Get(ctx, page.Records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if local.CircuitSourceID == "" || len(local.Attempts) != 0 {
		t.Fatalf("熔断关联缺失: %+v", local)
	}
	source, err := history.Get(ctx, local.CircuitSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Attempts) == 0 || source.Attempts[0].Status != 502 {
		t.Fatal("熔断来源不是实际上游错误")
	}
	f.mu.Lock()
	before := len(f.calls)
	f.mu.Unlock()
	_, _ = f.execution.Executor.Execute(ctx, f.request(), io.Discard)
	f.mu.Lock()
	after := len(f.calls)
	f.mu.Unlock()
	if before != after {
		t.Fatal("本地熔断仍发出上游请求")
	}
}

// TestRequestKeyDeletion 保留加密配置的真实读写条件，验证删除前最后名称及历史快照互不覆盖。
func TestRequestKeyDeletion(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	// 原 fixture 的名称等于测试凭据；改用独立名称，避免正确脱敏干扰名称快照断言。
	key := f.keys[0]
	key.Name = "请求时名称"
	if _, err = f.vendors.UpdateKey(ctx, key.ID, providers.ProviderKeyDraft{Name: key.Name, Multiplier: key.Multiplier, Priority: key.Priority, CacheMinutes: key.CacheMinutes, ProbeMinutes: key.ProbeMinutes, Proxy: key.Proxy}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.execution.Executor.Execute(ctx, f.request(), io.Discard); err != nil {
		t.Fatal(err)
	}
	service := &VelaService{records: history, providers: f.vendors}
	if _, err = f.vendors.UpdateKey(ctx, key.ID, providers.ProviderKeyDraft{Name: "最后名称", Multiplier: key.Multiplier, Priority: key.Priority, CacheMinutes: key.CacheMinutes, ProbeMinutes: key.ProbeMinutes, Proxy: key.Proxy}); err != nil {
		t.Fatal(err)
	}
	totals, err := service.RequestKeyTotals(ctx, records.Query{})
	if err != nil || len(totals) != 1 || totals[0].KeyName != "最后名称" {
		t.Fatal(totals, err)
	}
	if err = service.DeleteProviderKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	totals, err = service.RequestKeyTotals(ctx, records.Query{})
	if err != nil || len(totals) != 1 || totals[0].KeyName != "最后名称（已删除）" {
		t.Fatal(totals, err)
	}
	page, err := history.Query(ctx, records.Query{})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.RequestRecord(ctx, page.Records[0].ID)
	if err != nil || detail.KeyName != key.Name || detail.CurrentKeyName != "已删除" {
		t.Fatal(detail.KeyName, detail.CurrentKeyName, err)
	}
}
