package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// revalidationKeyDraft 复用当前 Key 的业务配置，仅修改本次测试需要的密钥或优先级。
func revalidationKeyDraft(key providers.ProviderKey, secret string, priority int) providers.ProviderKeyDraft {
	return providers.ProviderKeyDraft{Name: key.Name, Note: key.Note, Secret: secret, Multiplier: key.Multiplier, Priority: priority, RPMLimit: key.RPMLimit, ConcurrencyLimit: key.ConcurrencyLimit, CacheMinutes: key.CacheMinutes, ProbeMinutes: key.ProbeMinutes, Proxy: key.Proxy}
}

// TestVideoPollRevalidatesOriginalTarget 通过正式 HTTP 入口验证创建后或轮询中撤权、停用和替换凭据会停止后续真实出站。
func TestVideoPollRevalidatesOriginalTarget(t *testing.T) {
	for _, tc := range []struct {
		// name 标识配置变化；afterPoll 表示第一轮查询后才失效，change 调用实际供应商配置操作。
		name      string
		afterPoll bool
		change    func(context.Context, *executionFixture) error
	}{
		{"disable-after-create", false, func(ctx context.Context, f *executionFixture) error {
			return f.vendors.SetKeyEnabled(ctx, f.keys[0].ID, false)
		}},
		{"revoke-after-first-poll", true, func(ctx context.Context, f *executionFixture) error {
			return f.vendors.SaveModelPermissions(ctx, f.keys[0].ID, providers.ProviderModelPermissions{AllModels: false})
		}},
		{"replace-after-first-poll", true, func(ctx context.Context, f *executionFixture) error {
			_, err := f.vendors.UpdateKey(ctx, f.keys[0].ID, revalidationKeyDraft(f.keys[0], "replacement-secret", f.keys[0].Priority))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var creates, polls, otherKeys atomic.Int32
			var f *executionFixture
			f = nativeFixture(t, "video", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key-a" {
					otherKeys.Add(1)
				}
				if r.Method == http.MethodPost {
					creates.Add(1)
					if !tc.afterPoll {
						if err := tc.change(r.Context(), f); err != nil {
							t.Error(err)
						}
					}
					_, _ = io.WriteString(w, `{"id":"video-revalidate","status":"queued"}`)
					return
				}
				number := polls.Add(1)
				if tc.afterPoll && number == 1 {
					if err := tc.change(r.Context(), f); err != nil {
						t.Error(err)
					}
					_, _ = io.WriteString(w, `{"id":"video-revalidate","status":"in_progress"}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"video-revalidate","status":"completed","seconds":"4","size":"1280x720"}`)
			})
			routes, err := f.vendors.ExecutionCandidatesFor(context.Background(), "logical-model", "video")
			if err != nil || len(routes) != 2 {
				t.Fatalf("routes=%+v err=%v", routes, err)
			}
			status, body := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/videos", `{"model":"logical-model","prompt":"test","seconds":"4","size":"1280x720"}`, nil)
			wantPolls := int32(0)
			if tc.afterPoll {
				wantPolls = 1
			}
			if status != 409 || creates.Load() != 1 || polls.Load() != wantPolls || otherKeys.Load() != 0 {
				t.Fatalf("status=%d creates=%d polls=%d otherKeys=%d body=%s", status, creates.Load(), polls.Load(), otherKeys.Load(), body)
			}
			health := f.execution.Executor.Runtime.Health(f.keys[0].ID, routes[0].Version)
			if health.Score != 10 || health.Inflight != 0 {
				t.Fatalf("本地复核失败改变健康或泄漏资格: %+v", health)
			}
			page, err := f.execution.Executor.Records.Query(context.Background(), records.Query{})
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("records=%+v err=%v", page, err)
			}
			record, err := f.execution.Executor.Records.Get(context.Background(), page.Records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.State != "failed" || len(record.Attempts) != int(1+wantPolls) || record.Execution != "direct" || record.Attempts[len(record.Attempts)-1].Status != 200 || record.Attempts[len(record.Attempts)-1].Error == "" || !strings.Contains(record.Client.ResponseBody, "原上游绑定") {
				t.Fatalf("真实出站和本地终止记录错误: %+v", record)
			}
		})
	}
}

// fileBindingRequest 以实际网关认证读取或删除具名文件，保留真实资源绑定和调度路径。
func fileBindingRequest(t *testing.T, base, path, method, key string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Goog-Api-Key", key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

// TestFileListBindsEveryResource 验证列表中各资源绑定本次实际 Key，后续查询和删除不受新排序影响，失效或其他主体不能切备用。
func TestFileListBindsEveryResource(t *testing.T) {
	var calls atomic.Int32
	f := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Goog-Api-Key") != "key-a" {
			t.Errorf("列表资源切换 Key: %v", r.Header)
		}
		switch r.URL.Path {
		case "/v1beta/files":
			_, _ = io.WriteString(w, `{"files":[{"name":"files/listed-a","state":"ACTIVE"},{"name":"files/listed-b","state":"ACTIVE"}]}`)
		case "/v1beta/files/listed-a":
			_, _ = io.WriteString(w, `{"name":"files/listed-a","state":"ACTIVE"}`)
		case "/v1beta/files/listed-b":
			if r.Method != http.MethodDelete {
				t.Error("未保留文件删除方法")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("意外文件路径 %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	base := startCompatibilityGateway(t, f)
	status, body := fileBindingRequest(t, base, "/v1beta/files", http.MethodGet, "gateway-test-key")
	if status != 200 || !strings.Contains(string(body), "listed-a") || !strings.Contains(string(body), "listed-b") {
		t.Fatalf("文件列表失败: %d %s", status, body)
	}
	history := f.execution.Executor.Records
	page, err := history.Query(context.Background(), records.Query{})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("列表记录=%+v err=%v", page, err)
	}
	record := page.Records[0]
	if record.State != "success" || record.Usage == nil || record.Usage.Input != 0 || record.Usage.Output != 0 || record.Cost == nil || *record.Cost != 0 || record.UnknownCosts != 0 || len(record.Attempts) != 1 || record.Attempts[0].Cost == nil || *record.Attempts[0].Cost != 0 {
		t.Fatalf("无续接 ID 的列表查询产生模型消耗或未知费用: %+v", record)
	}
	totals, err := history.Totals(context.Background(), records.Query{})
	if err != nil || len(totals) != 1 || totals[0].Requests != 1 || totals[0].Tokens != 0 || totals[0].Cost != 0 || totals[0].UnknownUsage != 0 || totals[0].UnknownCost != 0 {
		t.Fatalf("列表长期累计=%+v err=%v", totals, err)
	}
	keyTotals, err := history.KeyTotals(context.Background(), records.Query{})
	if err != nil || len(keyTotals) != 1 || keyTotals[0].Tokens != 0 || keyTotals[0].Cost != 0 || keyTotals[0].UnknownUsage != 0 || keyTotals[0].UnknownCost != 0 {
		t.Fatalf("列表 Key 累计=%+v err=%v", keyTotals, err)
	}
	if _, err := f.vendors.UpdateKey(context.Background(), f.keys[1].ID, revalidationKeyDraft(f.keys[1], "", 0)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		// path、method 和 status 验证两条独立资源绑定和未知资源的零出站边界。
		path, method string
		status       int
	}{{"/v1beta/files/listed-a", http.MethodGet, 200}, {"/v1beta/files/listed-b", http.MethodDelete, 204}, {"/v1beta/files/unknown", http.MethodGet, 409}} {
		status, body := fileBindingRequest(t, base, tc.path, tc.method, "gateway-test-key")
		if status != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, status, body)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("未知资源出站: calls=%d", calls.Load())
	}
	f.gateway.SetAccessKey("other-principal")
	status, _ = fileBindingRequest(t, base, "/v1beta/files/listed-a", http.MethodGet, "other-principal")
	if status != 409 || calls.Load() != 3 {
		t.Fatalf("不同主体使用原资源: status=%d calls=%d", status, calls.Load())
	}
	f.gateway.SetAccessKey("gateway-test-key")
	if err := f.vendors.SetKeyEnabled(context.Background(), f.keys[0].ID, false); err != nil {
		t.Fatal(err)
	}
	status, _ = fileBindingRequest(t, base, "/v1beta/files/listed-a", http.MethodGet, "gateway-test-key")
	if status != 409 || calls.Load() != 3 {
		t.Fatalf("原 Key 停用后切备用: status=%d calls=%d", status, calls.Load())
	}
}
