package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// testKeys 是隔离测试的内存凭据主密钥保存位置，不使用用户钥匙串。
type testKeys struct{ data []byte }

// Load 读取本测试主密钥，初次返回未建立。
func (k *testKeys) Load() ([]byte, error) {
	if k.data == nil {
		return nil, providers.ErrNoMasterKey
	}
	return bytes.Clone(k.data), nil
}

// Save 保存本测试主密钥副本。
func (k *testKeys) Save(data []byte) error { k.data = bytes.Clone(data); return nil }

// executionFixture 是实际SQLite、加密凭据、执行器和可记录出站顺序的本地上游。
type executionFixture struct {
	// catalog、vendors、policy、execution 共同组成真实应用模块链。
	catalog   *models.Store
	vendors   *providers.Store
	policy    *scheduling.Store
	execution *SchedulingExecution
	// provider、keys 是测试供应商和Key身份，mu保护上游观察结果。
	provider providers.Provider
	keys     []providers.ProviderKey
	mu       sync.Mutex
	calls    []string
	// fail 控制第一把Key返回可重试5xx，不替换真实调度实现。
	fail bool
	// stream 是本地上游返回的SSE内容，空值使用完整非流式响应。
	stream string
	// delay 是本地上游每次响应前的等待，用于保留真实请求期限条件。
	delay time.Duration
}

// newExecutionFixture 从真实模块操作建立两把Key、明确模型关联和可控HTTP上游。
func newExecutionFixture(t *testing.T) *executionFixture {
	t.Helper()
	ctx := context.Background()
	f := &executionFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "upstream-model" {
			t.Errorf("出站模型未映射: %v", body["model"])
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("客户端Cookie泄漏到上游")
		}
		f.mu.Lock()
		f.calls = append(f.calls, secret)
		fail := f.fail && secret == "Bearer key-a"
		stream := f.stream
		delay := f.delay
		f.mu.Unlock()
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			w.WriteHeader(502)
			io.WriteString(w, `{"error":{"type":"upstream_error"}}`)
			return
		}
		if stream != "" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, stream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	var err error
	f.catalog, err = models.Open(filepath.Join(t.TempDir(), "vela.sqlite"), http.DefaultClient, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.catalog.Close() })
	// 本例只需统一模型身份；记录使用生产模型表和真实外键，价格与目录无关。
	if _, err = f.catalog.DB().Exec(`INSERT INTO models VALUES('model-1','logical-model','{"kind":"chat"}')`); err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f.vendors, err = providers.New(f.catalog.DB(), &testKeys{}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.InitModels(); err != nil {
		t.Fatal(err)
	}
	f.policy, err = scheduling.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.provider, err = f.vendors.Create(ctx, providers.ProviderDraft{Name: "Local", URL: upstream.URL, Request: providers.RequestPolicy{Attempts: 2, TransportFailover: true, Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}})
	if err != nil {
		t.Fatal(err)
	}
	multiplier := 1.0
	for i, secret := range []string{"key-a", "key-b"} {
		key, err := f.vendors.CreateKey(ctx, f.provider.ID, providers.ProviderKeyDraft{Name: secret, Secret: secret, Multiplier: &multiplier, Priority: i + 1, CacheMinutes: 5, ProbeMinutes: 10, Proxy: providers.ProxyChoice{Mode: providers.ProxyProvider}})
		if err != nil {
			t.Fatal(err)
		}
		f.keys = append(f.keys, key)
	}
	if err = f.vendors.SaveModels(ctx, f.provider.ID, providers.ProviderModelsDraft{ModelIDs: []string{"model-1"}}); err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: "model-1", Mappings: []providers.ProviderModelMapping{{RequestName: "upstream-model"}, {RequestName: "unused-fallback-name"}}}}); err != nil {
		t.Fatal(err)
	}
	f.execution, err = NewSchedulingExecution(ctx, f.policy, f.vendors, f.catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.execution.Close)
	return f
}

// request 返回真实执行入口所需的同格式请求，不走模拟调度。
func (f *executionFixture) request() scheduling.ExecutionRequest {
	return scheduling.ExecutionRequest{Request: scheduling.Request{Model: "logical-model", Protocol: "openai:chat", Session: "test-session"}, Path: "/v1/chat/completions", Body: []byte(`{"model":"logical-model","messages":[{"role":"user","content":"hello"}]}`), Headers: http.Header{"Cookie": []string{"do-not-forward"}}, RetrySafe: true}
}

// TestSchedulingRealHTTPRetryAndAffinity 验证真实K1→K1→K2、逐次扣分、亲和续用和固定顺序恢复主Key。
func TestSchedulingRealHTTPRetryAndAffinity(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	f.fail = true
	var out bytes.Buffer
	result, err := f.execution.Executor.Execute(ctx, f.request(), &out)
	if err != nil || result.Attempts != 3 || !result.Result.Outcome.Success {
		t.Fatalf("真实故障转移: %+v %v", result, err)
	}
	f.mu.Lock()
	calls := append([]string(nil), f.calls...)
	f.calls = nil
	f.fail = false
	f.mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"Bearer key-a", "Bearer key-a", "Bearer key-b"}) {
		t.Fatalf("出站顺序: %v", calls)
	}
	keys, err := f.vendors.Keys(ctx, f.provider.ID)
	if err != nil || keys[0].HealthScore != 6 || keys[1].HealthScore != 10 {
		t.Fatalf("逐Key真实健康: %+v %v", keys, err)
	}
	out.Reset()
	if _, err = f.execution.Executor.Execute(ctx, f.request(), &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	calls = append([]string(nil), f.calls...)
	f.calls = nil
	f.mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"Bearer key-b"}) {
		t.Fatalf("亲和没有迁移到备用: %v", calls)
	}
	if err = f.policy.SaveStrategy(ctx, scheduling.FixedOrder); err != nil {
		t.Fatal(err)
	}
	if _, err = f.execution.Executor.Execute(ctx, f.request(), &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.calls, []string{"Bearer key-a"}) {
		t.Fatalf("固定顺序应重新用主Key: %v", f.calls)
	}
}

// TestSchedulingCommittedStreamNeverReplays 验证输出文本后断流失败，不能第二次出站或回健康分。
func TestSchedulingCommittedStreamNeverReplays(t *testing.T) {
	f := newExecutionFixture(t)
	f.stream = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	req := f.request()
	req.Stream = true
	var out bytes.Buffer
	result, err := f.execution.Executor.Execute(context.Background(), req, &out)
	if err == nil || !result.Result.Committed || result.Attempts != 1 || !bytes.Contains(out.Bytes(), []byte("partial")) {
		t.Fatalf("已输出不能重放: %+v %v %s", result, err, out.String())
	}
	keys, _ := f.vendors.Keys(context.Background(), f.provider.ID)
	if keys[0].HealthScore != 8 {
		t.Fatalf("断流未结算失败: %+v", keys)
	}
}

// TestSchedulingPermissionsAndContinuation 验证指定空权限不回退全模型，未验证连续会话不发起上游调用。
func TestSchedulingPermissionsAndContinuation(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	for _, key := range f.keys {
		if err := f.vendors.SaveModelPermissions(ctx, key.ID, providers.ProviderModelPermissions{AllModels: false}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if _, err := f.execution.Executor.Execute(ctx, f.request(), &out); !errors.Is(err, scheduling.ErrNoCandidates) {
		t.Fatalf("空授权必须拒绝: %v", err)
	}
	req := f.request()
	req.PreviousResponseID = "response-1"
	if _, err := f.execution.Executor.Execute(ctx, req, &out); !errors.Is(err, scheduling.ErrContinuation) {
		t.Fatalf("未知连续会话: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("权限/连续会话拒绝仍出站: %v", f.calls)
	}
}

// TestSchedulingHealthSurvivesRuntimeRestart 验证新运行核心恢复真实持久化分数和新Key默认值。
func TestSchedulingHealthSurvivesRuntimeRestart(t *testing.T) {
	f := newExecutionFixture(t)
	f.fail = true
	var out bytes.Buffer
	if _, err := f.execution.Executor.Execute(context.Background(), f.request(), &out); err != nil {
		t.Fatal(err)
	}
	restored, err := NewSchedulingExecution(context.Background(), f.policy, f.vendors, f.catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	routes, err := f.vendors.ExecutionCandidates(context.Background(), "logical-model")
	if err != nil {
		t.Fatal(err)
	}
	if score := restored.Executor.Runtime.Health(routes[0].Key.ID, routes[0].Version).Score; score != 6 {
		t.Fatalf("重启丢失健康: %v", score)
	}
	snapshot, err := f.vendors.Snapshot(context.Background())
	if err != nil || snapshot.Defaults.FirstByteSeconds != 30 || snapshot.Defaults.KeyProbeMinutes != 10 {
		t.Fatalf("新版默认值: %+v %v", snapshot.Defaults, err)
	}
}

// TestSchedulingStreamDeadlineDoesNotReset 验证切Key不重置真实总期限，超时不扣备用Key健康分。
func TestSchedulingStreamDeadlineDoesNotReset(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	f.fail = true
	f.delay = 600 * time.Millisecond
	f.stream = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	request := f.provider.Request
	request.Attempts = 1
	request.StreamTotalMillis = 1000
	if _, err := f.vendors.Update(ctx, f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: f.provider.URL, Request: request}); err != nil {
		t.Fatal(err)
	}
	req := f.request()
	req.Stream = true
	var out bytes.Buffer
	started := time.Now()
	result, err := f.execution.Executor.Execute(ctx, req, &out)
	if !errors.Is(err, context.DeadlineExceeded) || result.Attempts != 2 || out.Len() != 0 || time.Since(started) > 1800*time.Millisecond {
		t.Fatalf("切Key重置总期限: %+v %v elapsed=%s", result, err, time.Since(started))
	}
	keys, _ := f.vendors.Keys(ctx, f.provider.ID)
	if keys[0].HealthScore != 8 || keys[1].HealthScore != 10 {
		t.Fatalf("本地总期限不能扣健康分: %+v", keys)
	}
}

// TestSchedulingRejectsHTTPContinuationField 验证非null previous_response_id包括空字符串，均不会自动重开。
func TestSchedulingRejectsHTTPContinuationField(t *testing.T) {
	f := newExecutionFixture(t)
	for _, body := range []string{`{"model":"logical-model","previous_response_id":""}`, `{"model":"logical-model","previous_response_id":"old-response"}`} {
		req := f.request()
		req.Body = []byte(body)
		var out bytes.Buffer
		if _, err := f.execution.Executor.Execute(context.Background(), req, &out); !errors.Is(err, scheduling.ErrContinuation) {
			t.Fatalf("HTTP连续会话被放行: %v", err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("连续会话拒绝仍然出站")
	}
}

// TestSchedulingUsesProviderOperationMappings 验证合入后直接采用供应商模块的操作范围映射，不另建第二份关系。
func TestSchedulingUsesProviderOperationMappings(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	if err := f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: "model-1", Mappings: []providers.ProviderModelMapping{{RequestName: "chat-upstream", Operations: []string{"chat"}}, {RequestName: "responses-upstream", Operations: []string{"responses"}}}}}); err != nil {
		t.Fatal(err)
	}
	routes, err := f.vendors.ExecutionCandidates(ctx, "logical-model", "responses")
	if err != nil || len(routes) != 2 || routes[0].Model != "responses-upstream" {
		t.Fatalf("未复用操作范围: %+v %v", routes, err)
	}
	if _, err = f.vendors.ExecutionSecret(ctx, "logical-model", routes[0].Key.ID, routes[0].Version, "chat-upstream", "responses"); err == nil {
		t.Fatal("跨操作映射通过执行前复核")
	}
}
