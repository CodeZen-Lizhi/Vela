package modeltest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
)

// testVault 保存隔离测试主密钥，不访问用户凭据库。
type testVault struct{ data []byte }

// Load 读取当前测试主密钥，未建立时沿用真实未初始化错误。
func (v *testVault) Load() ([]byte, error) {
	if v.data == nil {
		return nil, providers.ErrNoMasterKey
	}
	return bytes.Clone(v.data), nil
}

// Save 保存测试主密钥副本。
func (v *testVault) Save(value []byte) error { v.data = bytes.Clone(value); return nil }

// fixture 通过真实 SQLite、加密凭据和本地 HTTP 上游验证完整诊断链路。
type fixture struct {
	// manager、catalog、vendors 为真实业务入口。
	manager *Manager
	catalog *models.Store
	vendors *providers.Store
	// provider、model、keys 为隔离测试创建的稳定身份。
	provider providers.Provider
	model    models.Model
	keys     []providers.ProviderKey
	// mu 保护本地上游记录及取消测试开关。
	mu      sync.Mutex
	calls   []string
	block   bool
	started chan struct{}
}

// newFixture 创建包含三条映射、两把停用 Key 的真实配置；本地上游在第二条映射第二把 Key 成功。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{started: make(chan struct{}, 1)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if _, ok := body["stream"]; ok {
			t.Error("非流式测试发送了 stream 字段")
		}
		model, _ := body["model"].(string)
		key := r.Header.Get("Authorization")
		f.mu.Lock()
		f.calls = append(f.calls, model+":"+key)
		block := f.block
		f.mu.Unlock()
		select {
		case f.started <- struct{}{}:
		default:
		}
		if block {
			<-r.Context().Done()
			return
		}
		if model == "second" && key == "Bearer key-b-secret" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			return
		}
		w.WriteHeader(502)
		w.Write([]byte(`{"error":{"message":"key-a-secret key-b-secret"}}`))
	}))
	t.Cleanup(upstream.Close)
	var err error
	f.catalog, err = models.Open(filepath.Join(t.TempDir(), "test.sqlite"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.catalog.Close(); err != nil {
			t.Error(err)
		}
	})
	f.vendors, err = providers.New(f.catalog.DB(), &testVault{})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.InitModels(); err != nil {
		t.Fatal(err)
	}
	f.provider, err = f.vendors.Create(ctx, providers.ProviderDraft{Name: "诊断供应商", URL: upstream.URL, Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}, RequestSeconds: 5}})
	if err != nil {
		t.Fatal(err)
	}
	f.model, err = f.catalog.Create(ctx, models.Create{ModelID: "logical-model", Name: "真实诊断模型", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.SaveModels(ctx, f.provider.ID, providers.ProviderModelsDraft{ModelIDs: []string{f.model.ID}}); err != nil {
		t.Fatal(err)
	}
	multiplier := 1.0
	for _, name := range []string{"key-a", "key-b"} {
		key, e := f.vendors.CreateKey(ctx, f.provider.ID, providers.ProviderKeyDraft{Name: name, Secret: name + "-secret", Multiplier: &multiplier, CacheMinutes: 5, ProbeMinutes: 10, Proxy: providers.ProxyChoice{Mode: providers.ProxyProvider}})
		if e != nil {
			t.Fatal(e)
		}
		f.keys = append(f.keys, key)
		if e = f.vendors.SetKeyEnabled(ctx, key.ID, false); e != nil {
			t.Fatal(e)
		}
	}
	if err = f.vendors.SetEnabled(ctx, f.provider.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.SetModelEnabled(ctx, f.provider.ID, f.model.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: f.model.ID, Mappings: []providers.ProviderModelMapping{{RequestName: "first", Priority: 1}, {RequestName: "second", Priority: 2}, {RequestName: "third", Priority: 3}}}}); err != nil {
		t.Fatal(err)
	}
	f.manager, err = New(f.catalog.DB(), f.vendors, func(context.Context, providers.ExecutionCandidate) (*http.Client, error) {
		return upstream.Client(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.manager.Close)
	return f
}

// request 固定真实测试模型，自动使用映射与当前有权限的 Key。
func (f *fixture) request(id string) Request {
	return Request{ID: id, ProviderID: f.provider.ID, ModelID: f.model.ID, Protocol: "chat", Message: "hello"}
}

// TestDiagnosticOrderPersistenceAndCascade 验证停用对象诊断、映射顺序、首成功停止、逐次响应脱敏持久化及解除关联清理。
func TestDiagnosticOrderPersistenceAndCascade(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	options, err := f.manager.Options(ctx, f.provider.ID, f.model.ID)
	if err != nil || len(options.Keys) != 2 {
		t.Fatalf("诊断选项: %+v %v", options, err)
	}
	result, err := f.manager.Run(ctx, f.request("order"))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "success" || len(result.Attempts) != 6 || result.Attempts[4].State != "unused" || result.RequestModel != "second" || result.KeyName != "key-b" {
		t.Fatalf("结果: %+v", result)
	}
	want := []string{"first:Bearer key-a-secret", "first:Bearer key-b-secret", "second:Bearer key-a-secret", "second:Bearer key-b-secret"}
	f.mu.Lock()
	calls := append([]string{}, f.calls...)
	f.mu.Unlock()
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("顺序 %v，期望 %v", calls, want)
	}
	for i, a := range result.Attempts {
		if i < 3 && a.State != "failed" {
			t.Fatalf("失败尝试丢失: %+v", a)
		}
		if i < 3 && a.ResponseBody != `{"error":{"message":"[REDACTED] [REDACTED]"}}` {
			t.Fatalf("失败响应未逐次保留或脱敏: %q", a.ResponseBody)
		}
		if i == 3 && !strings.Contains(a.ResponseBody, `"content":"ok"`) || i > 3 && a.ResponseBody != "" {
			t.Fatalf("响应归属错误: 尝试 %d，%q", i+1, a.ResponseBody)
		}
	}
	raw, _ := json.Marshal(result)
	for _, key := range []string{"key-a-secret", "key-b-secret"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("诊断结果泄露认证")
		}
	}
	// 重新装配管理器后从同一数据库读取，证明结果并非只保存在当前页面内存。
	reopened, err := New(f.catalog.DB(), f.vendors, f.manager.clientFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Results(ctx, f.provider.ID)
	if err != nil || len(saved) != 1 || saved[0].ID != "order" {
		t.Fatalf("回读: %+v %v", saved, err)
	}
	if !reflect.DeepEqual(saved[0].Attempts, result.Attempts) {
		t.Fatal("回读丢失逐次响应快照")
	}
	if err = f.vendors.SaveModels(ctx, f.provider.ID, providers.ProviderModelsDraft{}); err != nil {
		t.Fatal(err)
	}
	saved, err = reopened.Results(ctx, f.provider.ID)
	if err != nil || len(saved) != 0 {
		t.Fatalf("解除关联残留诊断: %+v %v", saved, err)
	}
}

// TestDiagnosticPermissionAndSelection 验证权限限制与指定映射/Key，不影响正式调度的停用约束。
func TestDiagnosticPermissionAndSelection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.vendors.SaveModelPermissions(ctx, f.keys[0].ID, providers.ProviderModelPermissions{}); err != nil {
		t.Fatal(err)
	}
	request := f.request("selected")
	request.KeyID = f.keys[0].ID
	if _, err := f.manager.Run(ctx, request); err == nil {
		t.Fatal("无权限 Key 被接受")
	}
	f.mu.Lock()
	count := len(f.calls)
	f.mu.Unlock()
	if count != 0 {
		t.Fatal("权限失败仍然出站")
	}
	request.KeyID = f.keys[1].ID
	request.RequestModel = "second"
	result, err := f.manager.Run(ctx, request)
	if err != nil || result.State != "success" || len(result.Attempts) != 1 {
		t.Fatalf("指定组合: %+v %v", result, err)
	}
	request.ID = "invalid-map"
	request.RequestModel = "not-configured"
	if _, err = f.manager.Run(ctx, request); err == nil {
		t.Fatal("未配置映射被接受")
	}
	formal, err := f.vendors.ExecutionCandidates(ctx, f.model.ModelID)
	if err != nil || len(formal) != 0 {
		t.Fatalf("诊断扩大正式准入: %+v %v", formal, err)
	}
}

// TestDiagnosticCancelAndDuplicate 验证停止真正取消上游请求，运行期间重复提交被拒绝且取消结果可回读。
func TestDiagnosticCancelAndDuplicate(t *testing.T) {
	f := newFixture(t)
	f.mu.Lock()
	f.block = true
	f.mu.Unlock()
	done := make(chan Result, 1)
	failed := make(chan error, 1)
	go func() { r, e := f.manager.Run(context.Background(), f.request("cancel-me")); done <- r; failed <- e }()
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("未开始真实HTTP")
	}
	if _, err := f.manager.Run(context.Background(), f.request("duplicate")); err == nil {
		t.Fatal("重复执行未阻止")
	}
	f.manager.Cancel("cancel-me")
	select {
	case result := <-done:
		if err := <-failed; err != nil {
			t.Fatal(err)
		}
		if result.State != "cancelled" || result.Attempts[0].State != "cancelled" || result.Attempts[1].State != "unused" {
			t.Fatalf("取消结果: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消没有中断上游")
	}
	saved, err := f.manager.Results(context.Background(), f.provider.ID)
	if err != nil || len(saved) != 1 || saved[0].State != "cancelled" {
		t.Fatalf("取消回读: %+v %v", saved, err)
	}
}

// TestDiagnosticCancelDuringPreparation 验证数据库尚未完成读取时停止也能阻止全部出站。
func TestDiagnosticCancelDuringPreparation(t *testing.T) {
	f := newFixture(t)
	conn, err := f.catalog.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { _, e := f.manager.Run(context.Background(), f.request("prepare-cancel")); done <- e }()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		f.manager.mu.Lock()
		_, registered := f.manager.active["prepare-cancel"]
		f.manager.mu.Unlock()
		if registered {
			break
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("准备阶段未登记取消入口")
		}
	}
	f.manager.Cancel("prepare-cancel")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("准备阶段取消没有停止调用")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("数据库准备阶段未响应取消")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatal("准备阶段取消后仍向上游发请求")
	}
}

// TestDiagnosticSnapshotKeepsActualTarget 验证后续候选出站前失败不覆盖上一条真实 HTTP 响应的目标身份。
func TestDiagnosticSnapshotKeepsActualTarget(t *testing.T) {
	f := newFixture(t)
	calls := 0
	clients := f.manager.clientFor
	f.manager.clientFor = func(ctx context.Context, c providers.ExecutionCandidate) (*http.Client, error) {
		calls++
		if calls > 1 {
			return nil, fmt.Errorf("出口无法建立")
		}
		return clients(ctx, c)
	}
	request := f.request("target-snapshot")
	request.RequestModel = "first"
	result, err := f.manager.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" || result.Status != 502 || result.KeyName != "key-a" || result.Attempts[1].KeyName != "key-b" {
		t.Fatalf("实际目标与 HTTP 快照不一致: %+v", result)
	}
}

// TestDiagnosticMappingPriorityAndDefault 验证诊断候选仅按优先级和历史同级名称排序，无映射时回退默认名。
func TestDiagnosticMappingPriorityAndDefault(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mappings := []providers.ProviderModelMapping{{RequestName: "z", Priority: 2}, {RequestName: "a", Priority: 2}, {RequestName: "first", Priority: 1}}
	if err := f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: f.model.ID, Mappings: mappings}}); err != nil {
		t.Fatal(err)
	}
	options, err := f.manager.Options(ctx, f.provider.ID, f.model.ID)
	if err != nil || !reflect.DeepEqual(options.Mappings, []string{"first", "a", "z"}) {
		t.Fatalf("诊断优先级: %+v %v", options, err)
	}
	if err = f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: f.model.ID}}); err != nil {
		t.Fatal(err)
	}
	options, err = f.manager.Options(ctx, f.provider.ID, f.model.ID)
	if err != nil || !reflect.DeepEqual(options.Mappings, []string{f.model.ModelID}) {
		t.Fatalf("诊断默认名回退: %+v %v", options, err)
	}
}
