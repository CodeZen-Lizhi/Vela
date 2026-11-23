package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// TestGatewayResponsesLargePrelude 通过鉴权本机 HTTP 入口、真实调度和持久化模块验证大前奏流式调用，避免再次返回 502。
func TestGatewayResponsesLargePrelude(t *testing.T) {
	for _, tc := range []struct {
		// name 标识前奏与终态场景，size 是每个准备事件的指令字节数，failed 控制提交后失败。
		name   string
		size   int
		failed bool
	}{{"original", 22000, false}, {"over-four-mib", 2200000, false}, {"failed-after-commit", 300000, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			var stream strings.Builder
			for _, kind := range []string{"response.created", "response.in_progress"} {
				stream.WriteString("event: " + kind + "\ndata: {\"type\":\"" + kind + "\",\"response\":{\"instructions\":\"" + strings.Repeat("i", tc.size) + "\",\"tools\":[{\"description\":\"" + strings.Repeat("t", 27000) + "\"}]}}\n\n")
			}
			if tc.failed {
				stream.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n")
			} else {
				stream.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
				stream.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
			}
			f.stream = stream.String()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			port := listener.Addr().(*net.TCPAddr).Port
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			server := gateway.New(f.catalog, f.execution.Executor)
			server.SetAccessKey("gateway-test-key")
			if err = server.Listen(ctx, port); err != nil {
				t.Fatal(err)
			}
			defer server.Close(context.Background())
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/v1/responses", strings.NewReader(`{"model":"logical-model","stream":true,"input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer gateway-test-key")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || string(body) != f.stream {
				t.Fatalf("网关流式响应: status=%d bytes=%d，期望完整 %d 字节", response.StatusCode, len(body), len(f.stream))
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.calls) != 1 {
				t.Fatalf("正常前奏不应触发重试: calls=%d", len(f.calls))
			}
		})
	}
}

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
	// handle 覆盖本地上游响应以检查真实出站协议，不替换调度、认证、记录与传输。
	handle func(http.ResponseWriter, *http.Request, map[string]any)
	// gateway 暴露本地验收监听器，便于验证暂停与升级连接的生命周期。
	gateway *gateway.Server
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
		if r.Method != http.MethodGet && body["model"] != "upstream-model" && !strings.Contains(r.URL.Path, "/models/upstream-model:") {
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
		handle := f.handle
		f.mu.Unlock()
		if handle != nil {
			handle(w, r, body)
			return
		}
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
	// 保留网关读取所需的完整模型身份；记录使用生产模型表和真实外键，价格与目录无关。
	if _, err = f.catalog.DB().Exec(`INSERT INTO models VALUES('model-1','logical-model','{"id":"model-1","modelId":"logical-model","name":"Logical Model","kind":"chat"}')`); err != nil {
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
	f.provider, err = f.vendors.Create(ctx, providers.ProviderDraft{Name: "Local", URL: upstream.URL, Request: providers.RequestPolicy{Attempts: 2, Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}})
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

// TestSchedulingHealthNotificationReadsCurrentProvider 验证真实上游结果结算归零时，通知接收方可立即读取列表和 Key 详情。
func TestSchedulingHealthNotificationReadsCurrentProvider(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	if err := f.vendors.SetKeyEnabled(ctx, f.keys[1].ID, false); err != nil {
		t.Fatal(err)
	}
	f.handle = func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"error":{"code":"upstream_error"}}`)
	}

	var notifications int
	var circuitObserved bool
	f.execution.Executor.Runtime.SetHealthChanged(func(keyID string) {
		notifications++
		if keyID != f.keys[0].ID {
			t.Errorf("通知指向错误的 Key: %s", keyID)
		}
		snapshot, err := f.vendors.Snapshot(ctx)
		if err != nil || len(snapshot.Providers) != 1 {
			t.Fatalf("重读供应商列表: %+v, %v", snapshot, err)
		}
		row := snapshot.Providers[0]
		if row.Health.Percent == nil || *row.Health.Percent != 0 {
			return
		}
		circuitObserved = true
		if row.Health.State != providers.ProviderHealthCircuit || row.PreferredKey != nil {
			t.Errorf("通知发出时列表尚未显示熔断: %+v", row)
		}
		keys, err := f.vendors.Keys(ctx, f.provider.ID)
		if err != nil || len(keys) != 2 || keys[0].HealthScore != 0 || keys[0].HealthState != providers.ProviderKeyHealthCircuit {
			t.Errorf("通知发出时详情尚未显示零分熔断: %+v, %v", keys, err)
		}
	})
	var out bytes.Buffer
	for range 2 {
		_, _ = f.execution.Executor.Execute(ctx, f.request(), &out)
	}
	if notifications != 4 || !circuitObserved {
		t.Fatalf("预期四次扣分通知且最后一次读到熔断，实际通知 %d 次，熔断 %t", notifications, circuitObserved)
	}
}

// TestSchedulingHealthProjectionIncludesCircuitRecovery 验证零分熔断会进入 Key 列表并带出真实下次试探时刻。
func TestSchedulingHealthProjectionIncludesCircuitRecovery(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	routes, err := f.vendors.ExecutionCandidates(ctx, "logical-model")
	if err != nil || len(routes) == 0 {
		t.Fatalf("候选 = %+v, %v", routes, err)
	}
	route := routes[0]
	candidate := scheduling.Candidate{
		Provider:          route.Provider,
		Key:               route.Key,
		Model:             route.Model,
		CredentialVersion: route.Version,
	}
	attempt, err := f.execution.Executor.Runtime.Acquire(candidate)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Finish(scheduling.Outcome{Penalty: scheduling.Unavailable})
	health := f.execution.Executor.Runtime.Health(route.Key.ID, route.Version)
	keys, err := f.vendors.Keys(ctx, f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 || keys[0].HealthState != providers.ProviderKeyHealthCircuit || !keys[0].RecoverAt.Equal(health.NextProbe) {
		t.Fatalf("熔断投影 = %+v，运行时恢复时刻 %v", keys, health.NextProbe)
	}
}

// TestResetProviderKeyHealth 验证恢复按钮更新真实运行健康、失败回滚且新快照重启后仍为满分。
func TestResetProviderKeyHealth(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	f.fail = true
	var out bytes.Buffer
	if _, err := f.execution.Executor.Execute(ctx, f.request(), &out); err != nil {
		t.Fatal(err)
	}
	routes, err := f.vendors.ExecutionCandidates(ctx, "logical-model")
	if err != nil {
		t.Fatal(err)
	}
	keyID := routes[0].Key.ID
	version := routes[0].Version
	if score := f.execution.Executor.Runtime.Health(keyID, version).Score; score != 6 {
		t.Fatalf("故障后健康分 = %v", score)
	}

	originalPersist := f.execution.Executor.Persist
	f.execution.Executor.Persist = func(context.Context) error { return errors.New("injected health save failure") }
	service := NewVelaService(Options{Providers: f.vendors, Execution: f.execution})
	if err = service.ResetProviderKeyHealth(ctx, keyID); err == nil {
		t.Fatal("持久化失败必须返回错误")
	}
	if score := f.execution.Executor.Runtime.Health(keyID, version).Score; score != 6 {
		t.Fatalf("保存失败应回滚健康分，实际 %v", score)
	}

	f.execution.Executor.Persist = originalPersist
	if err = service.ResetProviderKeyHealth(ctx, keyID); err != nil {
		t.Fatal(err)
	}
	health := f.execution.Executor.Runtime.Health(keyID, version)
	if health.Score != 10 || health.CircuitOpen || !health.CooldownUntil.IsZero() || health.ConsecutiveFailures != 0 {
		t.Fatalf("恢复后的运行状态 = %+v", health)
	}

	restored, err := NewSchedulingExecution(ctx, f.policy, f.vendors, f.catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if score := restored.Executor.Runtime.Health(keyID, version).Score; score != 10 {
		t.Fatalf("重启后的健康分 = %v", score)
	}
}

// TestSchedulingStreamFailoverIdleTimeout 验证切 Key 后按当前上游空闲计时，不因累计耗时中断正常备用输出。
func TestSchedulingStreamFailoverIdleTimeout(t *testing.T) {
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
	if err != nil || result.Attempts != 2 || !result.Result.Outcome.Success || out.String() != f.stream || time.Since(started) < time.Second {
		t.Fatalf("累计耗时不应中断备用流: %+v %v elapsed=%s", result, err, time.Since(started))
	}
	keys, _ := f.vendors.Keys(ctx, f.provider.ID)
	if keys[0].HealthScore != 8 || keys[1].HealthScore != 10 {
		t.Fatalf("备用成功不应扣健康分: %+v", keys)
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

// TestSchedulingUsesProviderPriorityMappings 验证映射优先级、执行前复核及清空后的默认名回退。
func TestSchedulingUsesProviderPriorityMappings(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	if err := f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: "model-1", Mappings: []providers.ProviderModelMapping{{RequestName: "later-upstream", Priority: 2}, {RequestName: "first-upstream", Priority: 1}}}}); err != nil {
		t.Fatal(err)
	}
	routes, err := f.vendors.ExecutionCandidates(ctx, "logical-model")
	if err != nil || len(routes) != 2 || routes[0].Model != "first-upstream" {
		t.Fatalf("未复用优先级: %+v %v", routes, err)
	}
	if _, err = f.vendors.ExecutionSecret(ctx, "logical-model", routes[0].Key.ID, routes[0].Version, "later-upstream"); err == nil {
		t.Fatal("非当前映射通过执行前复核")
	}
	if _, err = f.vendors.ExecutionSecret(ctx, "logical-model", routes[0].Key.ID, routes[0].Version, "first-upstream"); err != nil {
		t.Fatal(err)
	}
	if err = f.vendors.SaveMappings(ctx, f.provider.ID, []providers.ProviderModelMappingsDraft{{ModelID: "model-1"}}); err != nil {
		t.Fatal(err)
	}
	routes, err = f.vendors.ExecutionCandidates(ctx, "logical-model")
	if err != nil || len(routes) != 2 || routes[0].Model != "logical-model" {
		t.Fatalf("清空后没有回退默认名: %+v %v", routes, err)
	}
}

// TestExecutionGlobalProxyAndDirect 通过真实HTTP与加密全局代理验证调度出口；两级直连都不读取失效的全局凭据。
func TestExecutionGlobalProxyAndDirect(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	catalog, err := models.Open(filepath.Join(dir, "proxy.sqlite"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.ProtectProxy(secrets); err != nil {
		t.Fatal(err)
	}
	var proxied, direct atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); io.WriteString(w, "direct") }))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("global-user:global-password")) {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		proxied.Add(1)
		io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	parsed, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("global-user", "global-password")
	if err = catalog.SetProxy(ctx, models.Proxy{Enabled: true, URL: parsed.String()}); err != nil {
		t.Fatal(err)
	}
	execution := &SchedulingExecution{models: catalog, clients: map[string]*http.Client{}}
	t.Cleanup(func() {
		for _, client := range execution.clients {
			client.CloseIdleConnections()
		}
	})
	candidate := scheduling.Candidate{Provider: providers.Provider{Request: providers.RequestPolicy{Proxy: providers.ProxyChoice{Mode: providers.ProxyDirect}}}, Key: providers.ProviderKey{Proxy: providers.ProxyChoice{Mode: providers.ProxyGlobal}}}
	request := func(c scheduling.Candidate, want string) {
		t.Helper()
		client, err := execution.clientFor(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(upstream.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 || string(body) != want {
			t.Fatal("实际调度代理出口不匹配", err)
		}
	}
	request(candidate, "proxied")
	candidate.Key.Proxy.Mode = providers.ProxyProvider
	request(candidate, "proxied")
	candidate.Key.Proxy.Mode = ""
	request(candidate, "proxied")
	var document string
	if err = catalog.DB().QueryRow("SELECT document FROM settings WHERE name='proxy'").Scan(&document); err != nil || strings.Contains(document, "global-password") {
		t.Fatal("全局认证未加密", err)
	}
	// 全局模式每次读取最新设置，关闭全局代理后旧客户端不能继续被选中。
	if err = catalog.SetProxy(ctx, models.Proxy{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	request(candidate, "direct")
	if err = catalog.SetProxy(ctx, models.Proxy{Enabled: true, URL: parsed.String()}); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "credentials", "master.key")); err != nil {
		t.Fatal(err)
	}
	if _, err = execution.clientFor(ctx, candidate); err == nil {
		t.Fatal("全局凭据丢失仍静默出站")
	}
	candidate.Key.Proxy.Mode = providers.ProxyDirect
	request(candidate, "direct")
	candidate.Key.Proxy.Mode = providers.ProxyProvider
	candidate.Provider.Request.Proxy.Mode = providers.ProxyDirect
	if _, err = execution.clientFor(ctx, candidate); err == nil {
		t.Fatal("旧 provider 模式继承了供应商直连")
	}
	if proxied.Load() != 3 || direct.Load() != 2 {
		t.Fatal("继承或直连请求数量错误")
	}
}

// TestSchedulingLegacyTransferLimitsIgnored 验证旧配置的次数和耗时限制不再阻断第三把 Key 的真实 HTTP 调用。
func TestSchedulingLegacyTransferLimitsIgnored(t *testing.T) {
	f := newExecutionFixture(t)
	ctx := context.Background()
	multiplier := 1.0
	third, err := f.vendors.CreateKey(ctx, f.provider.ID, providers.ProviderKeyDraft{Name: "third", Secret: "key-c", Multiplier: &multiplier, Priority: 3, CacheMinutes: 5, ProbeMinutes: 10, Proxy: providers.ProxyChoice{Mode: providers.ProxyProvider}})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		mu.Lock()
		calls = append(calls, key)
		mu.Unlock()
		if key != "Bearer key-c" {
			// 前两次失败累计超过旧版一秒限制，保留原故障触发条件。
			timer := time.NewTimer(550 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	request := map[string]any{"attempts": 1, "maxTransfers": 1, "maxTransferSeconds": 1, "proxy": map[string]string{"mode": "direct"}}
	raw, err := json.Marshal(map[string]any{"url": upstream.URL, "request": request})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.DB().Exec("UPDATE providers SET document=? WHERE id=?", string(raw), f.provider.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := f.execution.Executor.Execute(ctx, f.request(), &output)
	if err != nil || result.Attempts != 3 || result.KeyID != third.ID || !bytes.Contains(output.Bytes(), []byte("ok")) {
		t.Fatalf("旧转移限制阻断候选: %+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"Bearer key-a", "Bearer key-b", "Bearer key-c"}) {
		t.Fatalf("实际出站顺序: %v", calls)
	}
}

// TestSchedulingConnectionFailureBudget 验证历史false不再阻止真实连接故障转移，尝试预算和不安全/不可重试边界继续生效。
func TestSchedulingConnectionFailureBudget(t *testing.T) {
	for _, tc := range []struct {
		// name 用例说明；attempts 是每Key预算，safe 是入口确认的重放安全性。
		name     string
		attempts int
		safe     bool
		// status 非零时第一Key返回不可重试状态，否则真实关闭连接。
		status int
		// wantAttempts 与 success 为预期实际出站次数及最终成功状态。
		wantAttempts int
		success      bool
	}{{"retryable-budget-two", 2, true, 0, 3, true}, {"retryable-budget-one", 1, true, 0, 2, true}, {"unsafe-stops", 2, false, 0, 1, false}, {"nonretryable-stops", 2, true, 400, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") == "Bearer key-a" {
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						io.WriteString(w, `{"error":{"type":"invalid_request_error"}}`)
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			request := f.provider.Request
			request.Attempts = tc.attempts
			// 注入旧版真实存储JSON；decoder须兼容历史false，但执行不得再受它控制。
			raw, err := json.Marshal(map[string]any{"website": "", "url": upstream.URL, "request": request})
			if err != nil {
				t.Fatal(err)
			}
			var legacy map[string]any
			if err = json.Unmarshal(raw, &legacy); err != nil {
				t.Fatal(err)
			}
			legacy["request"].(map[string]any)["transportFailover"] = false
			raw, err = json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.catalog.DB().Exec("UPDATE providers SET document=? WHERE id=?", string(raw), f.provider.ID); err != nil {
				t.Fatal(err)
			}
			req := f.request()
			req.RetrySafe = tc.safe
			var output bytes.Buffer
			result, err := f.execution.Executor.Execute(context.Background(), req, &output)
			if (err == nil) != tc.success || result.Attempts != tc.wantAttempts || int(calls.Load()) != tc.wantAttempts {
				t.Fatalf("实际故障预算: %+v err=%v calls=%d", result, err, calls.Load())
			}
			if tc.success && result.KeyID != f.keys[1].ID {
				t.Fatal("未转移到下一候选")
			}
		})
	}
}
