package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// testKeys 用隔离内存主密钥替代系统钥匙串，仍走真实供应商密文存储。
type testKeys struct{ value []byte }

// Load 返回测试主密钥，首次调用遵守缺失契约。
func (k *testKeys) Load() ([]byte, error) {
	if k.value == nil {
		return nil, providers.ErrNoMasterKey
	}
	return k.value, nil
}

// Save 保存隔离测试的主密钥。
func (k *testKeys) Save(value []byte) error { k.value = value; return nil }

// fixture 通过真实公共操作创建模型、供应商、Key和关联。
func fixture(t *testing.T, url string) (*Manager, VerificationPlan) {
	t.Helper()
	ctx := context.Background()
	catalog, err := models.Open(filepath.Join(t.TempDir(), "vela.sqlite"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	vendors, err := providers.New(catalog.DB(), &testKeys{})
	if err != nil {
		t.Fatal(err)
	}
	if err = vendors.InitModels(); err != nil {
		t.Fatal(err)
	}
	p, err := vendors.Create(ctx, providers.ProviderDraft{Name: "检测上游", URL: url, Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}})
	if err != nil {
		t.Fatal(err)
	}
	plan := VerificationPlan{ProviderID: p.ID, Hours: 6, Lines: []VerificationLine{}}
	multiplier := 1.0
	for i := 0; i < 2; i++ {
		model, err := catalog.Create(ctx, models.Create{ModelID: fmt.Sprintf("model-%d", i), Name: fmt.Sprintf("模型%d", i), Kind: "chat"})
		if err != nil {
			t.Fatal(err)
		}
		key, err := vendors.CreateKey(ctx, p.ID, providers.ProviderKeyDraft{Name: fmt.Sprintf("Key%d", i), Secret: fmt.Sprintf("sk-fixture-%d", i), Multiplier: &multiplier, ProbeMinutes: 10, Proxy: providers.ProxyChoice{Mode: providers.ProxyProvider}})
		if err != nil {
			t.Fatal(err)
		}
		plan.Lines = append(plan.Lines, VerificationLine{ModelID: model.ID, KeyID: key.ID, Protocol: "chat_completions", Effort: "auto"})
	}
	ids := []string{plan.Lines[0].ModelID, plan.Lines[1].ModelID}
	if err = vendors.SaveModels(ctx, p.ID, providers.ProviderModelsDraft{ModelIDs: ids}); err != nil {
		t.Fatal(err)
	}
	m, err := New(catalog.DB(), vendors, catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Close()
		if err := catalog.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, plan
}

// TestPlanPersistenceAndPartialDeletion 验证计划事务、权限、重读及局部外键删除。
func TestPlanPersistenceAndPartialDeletion(t *testing.T) {
	m, p := fixture(t, "http://127.0.0.1:9000")
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	p.Enabled = true
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	plans, err := m.Plans(ctx)
	if err != nil || len(plans) != 1 || len(plans[0].Lines) != 2 || plans[0].NextAt != now.Add(6*time.Hour).UnixMilli() {
		t.Fatalf("保存后重读 %+v %v", plans, err)
	}
	rebuilt, err := New(m.db, m.vendors, m.catalog)
	if err != nil {
		t.Fatal(err)
	}
	again, err := rebuilt.Plans(ctx)
	if err != nil || again[0].NextAt != plans[0].NextAt {
		t.Fatalf("重新创建模块后配置变化: %+v %v", again, err)
	}
	bad := p
	bad.Lines = []VerificationLine{p.Lines[0], p.Lines[0]}
	if err = m.SavePlan(ctx, bad); err == nil {
		t.Fatal("重复线路未拒绝")
	}
	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range p.Lines {
		target, err := m.resolve(ctx, p.ProviderID, line)
		if err != nil {
			t.Fatal(err)
		}
		r := VerificationReport{ID: identity(target), Target: target, Time: now.UnixMilli(), Conclusion: "insufficient", Candidates: []VerificationCandidate{}, Samples: []VerificationSample{}}
		if err = m.saveReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshot.Targets) != 4 {
		t.Fatalf("目标组合%+v", snapshot.Targets)
	}
	if err = m.vendors.DeleteKey(ctx, p.Lines[0].KeyID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = m.Snapshot(ctx)
	if err != nil || len(snapshot.Plans[0].Lines) != 1 || len(snapshot.Reports) != 1 || snapshot.Reports[0].Target.KeyID != p.Lines[1].KeyID {
		t.Fatalf("局部清理影响了无关目标: %+v %v", snapshot, err)
	}
	if err = m.vendors.Delete(ctx, p.ProviderID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = m.Snapshot(ctx)
	if err != nil || len(snapshot.Plans) != 0 || len(snapshot.Reports) != 0 {
		t.Fatalf("供应商删除未清理: %+v %v", snapshot, err)
	}
}

// TestNoCatchupAndManualKeepsSchedule 验证开启不执行、暂停跳过及手动不重置定时。
func TestNoCatchupAndManualKeepsSchedule(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "invalid authentication", http.StatusUnauthorized)
	}))
	defer upstream.Close()
	m, p := fixture(t, upstream.URL)
	ctx := context.Background()
	now := time.Now()
	m.now = func() time.Time { return now }
	p.Enabled = true
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("开启触发网络")
	}
	plans, _ := m.Plans(ctx)
	next := plans[0].NextAt
	if err := m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	plans, _ = m.Plans(ctx)
	if plans[0].NextAt != next {
		t.Fatal("手动检测重置周期")
	}
	if calls.Load() != 6 {
		t.Fatalf("401不得重试，实际请求%d", calls.Load())
	}
	now = now.Add(19 * time.Hour)
	if err := m.tick(ctx, now, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 6 {
		t.Fatal("恢复发生补测")
	}
	plans, _ = m.Plans(ctx)
	if plans[0].NextAt <= now.UnixMilli() {
		t.Fatal("恢复未跳过已过周期")
	}
}

// TestReportReplacementAndMappingInvalidation 验证只保留最近详情及映射变化时清除受影响结果。
func TestReportReplacementAndMappingInvalidation(t *testing.T) {
	m, p := fixture(t, "http://127.0.0.1:9000")
	ctx := context.Background()
	target, err := m.resolve(ctx, p.ProviderID, p.Lines[0])
	if err != nil {
		t.Fatal(err)
	}
	r := VerificationReport{ID: identity(target), Target: target, Time: 1, Samples: []VerificationSample{{Text: "old"}}}
	if err = m.saveReport(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Time = 2
	r.Samples[0].Text = "latest"
	if err = m.saveReport(ctx, r); err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Snapshot(ctx)
	if err != nil || len(snapshot.Reports) != 1 || snapshot.Reports[0].Time != 2 || snapshot.Reports[0].Samples[0].Text != "latest" {
		t.Fatalf("最新结果错误 %+v %v", snapshot.Reports, err)
	}
	if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: target.ModelID, Mappings: []providers.ProviderModelMapping{{RequestName: "new-model"}}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = m.Snapshot(ctx)
	if err != nil || len(snapshot.Reports) != 0 {
		t.Fatalf("映射改变保留旧结果 %+v %v", snapshot.Reports, err)
	}
	var data string
	if err = m.db.QueryRow("SELECT COALESCE(group_concat(document),'') FROM verification_reports").Scan(&data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "old") {
		t.Fatal("旧回答仍保留逻辑记录")
	}
}

// TestLiveRoundScoring 经真实HTTP和固定上游样本观察真实排名、结论、重试与最近报告替换。
func TestLiveRoundScoring(t *testing.T) {
	var fixtureData struct {
		Cases []struct{ Outputs []struct{ Text string } }
	}
	data, err := os.ReadFile("fingerprint/testdata/analyze-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixtureData); err != nil {
		t.Fatal(err)
	}
	text := fixtureData.Cases[0].Outputs[0].Text
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-fixture-0" {
			t.Error("请求偏离固定目标或认证")
		}
		attempt := calls.Add(1)
		if attempt == 1 {
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	m, p := fixture(t, upstream.URL)
	ctx := context.Background()
	p.Lines = p.Lines[:1]
	p.Lines[0].ReferenceModel = "claude-haiku-4.5"
	if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: p.Lines[0].ModelID, Mappings: []providers.ProviderModelMapping{{RequestName: "claude-haiku-4.5"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	snap, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Reports) != 1 || snap.Reports[0].Conclusion != "match" || len(snap.Reports[0].Candidates) != 69 || snap.Reports[0].Candidates[0].Model != "claude-haiku-4.5" || calls.Load() != 4 {
		t.Fatalf("真实评分/重试结果不符: reports=%+v calls=%d", snap.Reports, calls.Load())
	}

	// 同一组真实回答对不同预期身份只能改变比较结论，不改变真实排名。
	for _, test := range []struct{ model, conclusion string }{{"gpt-4o", "difference"}, {"", "insufficient"}} {
		p.Lines[0].ReferenceModel = test.model
		if err = m.SavePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: p.Lines[0].ModelID, Mappings: []providers.ProviderModelMapping{{RequestName: "auto/vendor-alias"}}}}); err != nil {
			t.Fatal(err)
		}
		if err = m.Start(ctx, p.ProviderID, ""); err != nil {
			t.Fatal(err)
		}
		m.wg.Wait()
		result, err := m.Snapshot(ctx)
		if err != nil || len(result.Reports) != 1 || result.Reports[0].Conclusion != test.conclusion || result.Reports[0].Candidates[0].Model != "claude-haiku-4.5" {
			t.Fatalf("预期%s结论%s: %+v %v", test.model, test.conclusion, result.Reports, err)
		}
	}
	p.Lines[0].ReferenceModel = "claude-haiku-4.5"
	if err = m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	// 选择参考身份后，旧报告保持原结论；从结果行复测应用新计划选择。
	previous, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Reports[0].Conclusion != "insufficient" {
		t.Fatal("编辑计划改写了旧报告")
	}
	if err = m.Start(ctx, p.ProviderID, previous.Reports[0].ID); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	updated, err := m.Snapshot(ctx)
	if err != nil || updated.Reports[0].Conclusion != "match" || updated.Reports[0].Target.ReferenceModel != "claude-haiku-4.5" {
		t.Fatal("结果行复测未应用新参考选择", err)
	}
	if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: p.Lines[0].ModelID, Mappings: []providers.ProviderModelMapping{{RequestName: "claude-haiku-4.5"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	snap, err = m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldID := snap.Reports[0].ID
	if err = m.Start(ctx, p.ProviderID, oldID); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	snap, err = m.Snapshot(ctx)
	if err != nil || len(snap.Reports) != 1 || snap.Reports[0].ID != oldID || calls.Load() != 19 {
		t.Fatalf("复测未替换同一结果: %+v %v", snap.Reports, err)
	}
	// 原始模型 ID 命中参考库时，无需显式选择；请求别名和误导显示名不能改变比较身份。
	original, err := m.catalog.Create(ctx, models.Create{ModelID: "claude-haiku-4.5", Name: "gpt-4o", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.vendors.SaveModels(ctx, p.ProviderID, providers.ProviderModelsDraft{ModelIDs: []string{p.Lines[0].ModelID, original.ID}}); err != nil {
		t.Fatal(err)
	}
	p.Lines[0].ModelID = original.ID
	p.Lines[0].ReferenceModel = ""
	if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: original.ID, Mappings: []providers.ProviderModelMapping{{RequestName: "auto/vendor-alias"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	snap, err = m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, report := range snap.Reports {
		if report.Target.ModelID == original.ID {
			found = report.Conclusion == "match" && report.Target.OriginalModel == "claude-haiku-4.5" && report.Target.RequestModel == "auto/vendor-alias"
		}
	}
	if !found {
		t.Fatal("未使用原始模型 ID 自动对应参考身份")
	}

}

// TestSuspendCancelAndResumeNoCatchup 验证系统暂停取消真实在途HTTP，唤醒不会重新派发。
func TestSuspendCancelAndResumeNoCatchup(t *testing.T) {
	arrived := make(chan struct{}, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	m, p := fixture(t, upstream.URL)
	p.Lines = p.Lines[:1]
	ctx := context.Background()
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("采样未到达上游")
	}
	m.Suspend()
	m.wg.Wait()
	snap, err := m.Snapshot(ctx)
	if err != nil || snap.Progress.Running || len(snap.Reports) != 1 || snap.Reports[0].State != "cancelled" {
		t.Fatalf("睡眠未结束在途: %+v %v", snap, err)
	}
	if err = m.Start(ctx, p.ProviderID, ""); err == nil {
		t.Fatal("睡眠中仍能采样")
	}
	if err = m.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err = m.Snapshot(ctx)
	if err != nil || snap.Progress.Running {
		t.Fatalf("唤醒触发补测: %+v %v", snap.Progress, err)
	}
}

// TestDisabledTargetCanStopSchedule 验证失效线路仍可关闭自动计划。
func TestDisabledTargetCanStopSchedule(t *testing.T) {
	m, p := fixture(t, "http://127.0.0.1:9000")
	ctx := context.Background()
	p.Enabled = true
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := m.vendors.SetKeyEnabled(ctx, p.Lines[0].KeyID, false); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(ctx, p.ProviderID, false); err != nil {
		t.Fatal(err)
	}
	plans, err := m.Plans(ctx)
	if err != nil || plans[0].Enabled {
		t.Fatalf("失效线路未关闭: %+v %v", plans, err)
	}
}

// TestTargetDisabledDuringSamplingReplacesSuccess 验证执行中停用Key会替换旧成功并明确失败原因。
func TestTargetDisabledDuringSamplingReplacesSuccess(t *testing.T) {
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		arrived <- struct{}{}
		<-release
		http.Error(w, "invalid", 401)
	}))
	defer upstream.Close()
	m, p := fixture(t, upstream.URL)
	ctx := context.Background()
	p.Lines = p.Lines[:1]
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	target, err := m.resolve(ctx, p.ProviderID, p.Lines[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = m.saveReport(ctx, VerificationReport{ID: identity(target), Target: target, State: "complete", Conclusion: "match", Candidates: []VerificationCandidate{}, Samples: []VerificationSample{}}); err != nil {
		t.Fatal(err)
	}
	if err = m.Start(ctx, p.ProviderID, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("上游未收到请求")
	}
	if err = m.vendors.SetKeyEnabled(ctx, p.Lines[0].KeyID, false); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	m.wg.Wait()
	snap, err := m.Snapshot(ctx)
	if err != nil || len(snap.Reports) != 1 || snap.Reports[0].State != "failed" || snap.Reports[0].Conclusion != "insufficient" || !strings.Contains(snap.Reports[0].Reason, "失效") {
		t.Fatalf("旧成功掩盖本轮失效: %+v %v", snap.Reports, err)
	}
}

// TestVerificationMappingPriorityAndDefault 验证检测目标和执行凭据使用同一优先级映射，清空后回退默认请求名。
func TestVerificationMappingPriorityAndDefault(t *testing.T) {
	m, p := fixture(t, "http://127.0.0.1:9000")
	ctx := context.Background()
	line := p.Lines[0]
	if err := m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: line.ModelID, Mappings: []providers.ProviderModelMapping{{RequestName: "later", Priority: 2}, {RequestName: "first", Priority: 1}}}}); err != nil {
		t.Fatal(err)
	}
	target, err := m.resolve(ctx, p.ProviderID, line)
	if err != nil || target.RequestModel != "first" {
		t.Fatalf("检测目标未选首优先级: %+v %v", target, err)
	}
	candidate, _, err := m.credential(ctx, target)
	if err != nil || candidate.Model != "first" {
		t.Fatalf("执行目标不一致: %+v %v", candidate, err)
	}
	if err = m.vendors.SaveMappings(ctx, p.ProviderID, []providers.ProviderModelMappingsDraft{{ModelID: line.ModelID}}); err != nil {
		t.Fatal(err)
	}
	target, err = m.resolve(ctx, p.ProviderID, line)
	if err != nil || target.RequestModel != "model-0" {
		t.Fatalf("检测目标未回退默认名: %+v %v", target, err)
	}
	candidate, _, err = m.credential(ctx, target)
	if err != nil || candidate.Model != "model-0" {
		t.Fatalf("默认执行目标不一致: %+v %v", candidate, err)
	}
}

// TestReferencePlanMigrationAndValidation 验证旧表迁移、选择的持久化、取消选择及非法身份不覆盖旧计划。
func TestReferencePlanMigrationAndValidation(t *testing.T) {
	m, p := fixture(t, "https://example.invalid")
	ctx := context.Background()
	if err := m.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本表，保留真实计划、线路和外键。
	if _, err := m.db.Exec("ALTER TABLE verification_lines DROP COLUMN reference_model"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := New(m.db, m.vendors, m.catalog)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := upgraded.Plans(ctx)
	if err != nil || len(plans) != 1 || len(plans[0].Lines) != 2 || plans[0].Lines[0].ReferenceModel != "" {
		t.Fatal("旧计划迁移失败", err)
	}
	p.Lines[0].ReferenceModel = "deepseek-v4.1-flash-1017"
	if err = upgraded.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(m.db, m.vendors, m.catalog)
	if err != nil {
		t.Fatal(err)
	}
	plans, err = reopened.Plans(ctx)
	if err != nil || plans[0].Lines[0].ReferenceModel != p.Lines[0].ReferenceModel {
		t.Fatal("参考选择未持久化", err)
	}
	p.Lines[0].ReferenceModel = "invalid-reference"
	if err = reopened.SavePlan(ctx, p); err == nil {
		t.Fatal("接受了非法参考身份")
	}
	plans, err = reopened.Plans(ctx)
	if err != nil || plans[0].Lines[0].ReferenceModel != "deepseek-v4.1-flash-1017" {
		t.Fatal("非法保存破坏旧选择", err)
	}
	p.Lines[0].ReferenceModel = ""
	if err = reopened.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	plans, err = reopened.Plans(ctx)
	if err != nil || plans[0].Lines[0].ReferenceModel != "" {
		t.Fatal("取消参考选择未保存", err)
	}
}
