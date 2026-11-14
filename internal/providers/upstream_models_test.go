package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestUpstreamModelsAggregationCache 经真实 HTTP 验证启用 Key 聚合、部分失败、强刷保留缓存及凭据隔离。
func TestUpstreamModelsAggregationCache(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() || (r.Header.Get("Authorization") == "Bearer bad" || r.Header.Get("x-api-key") == "bad" || r.Header.Get("x-goog-api-key") == "bad") {
			http.Error(w, "secret must not escape", 401)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"alpha"}]}`)
	}))
	defer server.Close()
	s, closeStore := openStore(t, filepath.Join(t.TempDir(), "test.db"), &memoryKeys{})
	defer closeStore()
	ctx := context.Background()
	draft := validDraft("upstream")
	draft.URL = server.URL
	p, err := s.Create(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.CreateKey(ctx, p.ID, keyDraft("good", "good", 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateKey(ctx, p.ID, keyDraft("bad", "bad", 0))
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := s.CreateKey(ctx, p.ID, keyDraft("disabled", "disabled", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetKeyEnabled(ctx, disabled.ID, false); err != nil {
		t.Fatal(err)
	}
	clientFor := func(context.Context, Provider, ProviderKey) (*http.Client, error) { return server.Client(), nil }
	result, err := s.UpstreamModels(ctx, p.ID, false, clientFor)
	if err != nil || !reflect.DeepEqual(result.ModelNames, []string{"alpha", "zeta"}) || result.Warning == "" || result.FromCache {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	initial := calls.Load()
	if initial != 4 {
		t.Fatalf("calls=%d", initial)
	}
	result.ModelNames[0] = "changed"
	result, err = s.UpstreamModels(ctx, p.ID, false, clientFor)
	if err != nil || !result.FromCache || result.ModelNames[0] != "alpha" || calls.Load() != initial {
		t.Fatalf("cache=%+v err=%v", result, err)
	}
	fail.Store(true)
	if _, err = s.UpstreamModels(ctx, p.ID, true, clientFor); err == nil {
		t.Fatal("强刷全失败应返回错误")
	}
	result, err = s.UpstreamModels(ctx, p.ID, false, clientFor)
	if err != nil || !result.FromCache {
		t.Fatalf("旧缓存未保留: %+v %v", result, err)
	}
	if _, err = s.UpdateKey(ctx, good.ID, keyDraft("good", "new-secret", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpstreamModels(ctx, p.ID, false, clientFor); err == nil {
		t.Fatal("更换密钥后不能命中旧缓存")
	}
}

// TestUpstreamModelProtocols 验证 Anthropic 与 Gemini 的真实鉴权、分页和 Gemini 请求名归一化。
func TestUpstreamModelProtocols(t *testing.T) {
	for _, protocol := range []string{"anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if protocol == "anthropic" {
					if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
						t.Error("缺少 Anthropic 认证")
					}
					if r.URL.Query().Get("after_id") == "next" {
						fmt.Fprint(w, `{"data":[{"id":"two"}],"has_more":false}`)
					} else {
						fmt.Fprint(w, `{"data":[{"id":"one"}],"has_more":true,"last_id":"next"}`)
					}
				} else {
					if r.Header.Get("x-goog-api-key") != "secret" {
						t.Error("缺少 Gemini 认证")
					}
					if r.URL.Query().Get("pageToken") == "next" {
						fmt.Fprint(w, `{"models":[{"name":"models/two"}]}`)
					} else {
						fmt.Fprint(w, `{"models":[{"name":"models/one"}],"nextPageToken":"next"}`)
					}
				}
			}))
			defer server.Close()
			names, err := fetchUpstreamModelPages(context.Background(), server.Client(), server.URL, "secret", protocol)
			if err != nil || !reflect.DeepEqual(names, []string{"one", "two"}) {
				t.Fatalf("names=%v err=%v", names, err)
			}
		})
	}
}

// TestUpstreamModelResponseBoundaries 验证格式、分页循环、重定向和取消不会返回伪成功或泄露凭据。
func TestUpstreamModelResponseBoundaries(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":[{}]}`, `{"data":[],"has_more":true}`, `{"data":[],"has_more":true,"last_id":"same"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := fetchUpstreamModelPages(context.Background(), server.Client(), server.URL, "secret", "openai")
		server.Close()
		if err == nil {
			t.Fatalf("应拒绝 %s", body)
		}
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	if _, err := fetchUpstreamModelPages(context.Background(), redirect.Client(), redirect.URL, "secret", "openai"); err == nil || leaked.Load() {
		t.Fatal("不应跟随重定向")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchUpstreamModels(ctx, redirect.Client(), Provider{URL: redirect.URL}, "secret"); err == nil {
		t.Fatal("取消应失败")
	}
}

// TestUpstreamModelsConfigurationInvalidation 经真实 HTTP 验证 Key 集合及地址变化立即淘汰旧候选缓存。
func TestUpstreamModelsConfigurationInvalidation(t *testing.T) {
	var calls atomic.Int32
	handler := func(prefix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			name := prefix + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, name)
		}
	}
	first := httptest.NewServer(handler("first-"))
	defer first.Close()
	second := httptest.NewServer(handler("second-"))
	defer second.Close()
	s, closeStore := openStore(t, filepath.Join(t.TempDir(), "test.db"), &memoryKeys{})
	defer closeStore()
	ctx := context.Background()
	draft := validDraft("cache-change")
	draft.URL = first.URL
	p, err := s.Create(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateKey(ctx, p.ID, keyDraft("a", "a", 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateKey(ctx, p.ID, keyDraft("b", "b", 0))
	if err != nil {
		t.Fatal(err)
	}
	clientFor := func(context.Context, Provider, ProviderKey) (*http.Client, error) { return first.Client(), nil }
	check := func(want []string) {
		t.Helper()
		before := calls.Load()
		result, err := s.UpstreamModels(ctx, p.ID, false, clientFor)
		if err != nil || result.FromCache || !reflect.DeepEqual(result.ModelNames, want) || calls.Load() == before {
			t.Fatalf("未失效或候选错误: %+v %v", result, err)
		}
		before = calls.Load()
		result, err = s.UpstreamModels(ctx, p.ID, false, clientFor)
		if err != nil || !result.FromCache || calls.Load() != before {
			t.Fatalf("新结果未缓存: %+v %v", result, err)
		}
	}
	check([]string{"first-a", "first-b"})
	if err = s.SetKeyEnabled(ctx, b.ID, false); err != nil {
		t.Fatal(err)
	}
	check([]string{"first-a"})
	if err = s.SetKeyEnabled(ctx, b.ID, true); err != nil {
		t.Fatal(err)
	}
	check([]string{"first-a", "first-b"})
	if err = s.DeleteKey(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	check([]string{"first-a"})
	if _, err = s.CreateKey(ctx, p.ID, keyDraft("c", "c", 0)); err != nil {
		t.Fatal(err)
	}
	check([]string{"first-a", "first-c"})
	draft.URL = second.URL
	if _, err = s.Update(ctx, p.ID, draft); err != nil {
		t.Fatal(err)
	}
	check([]string{"second-a", "second-c"})
	if err = s.SetKeyEnabled(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	check([]string{"second-c"})
}

// TestUpstreamModelsTotalTimeout 验证多 Key 共用总预算，超时不继续下一把 Key，也不写入成功缓存。
func TestUpstreamModelsTotalTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer server.Close()
	s, closeStore := openStore(t, filepath.Join(t.TempDir(), "test.db"), &memoryKeys{})
	defer closeStore()
	ctx := context.Background()
	draft := validDraft("timeout")
	draft.URL = server.URL
	draft.Request.RequestSeconds = 1
	p, err := s.Create(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err = s.CreateKey(ctx, p.ID, keyDraft(name, name, 0)); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	_, err = s.UpstreamModels(ctx, p.ID, false, func(context.Context, Provider, ProviderKey) (*http.Client, error) { return server.Client(), nil })
	if err == nil || !strings.Contains(err.Error(), "超时") || calls.Load() != 1 || time.Since(started) > 2500*time.Millisecond {
		t.Fatalf("总预算未生效: calls=%d elapsed=%v err=%v", calls.Load(), time.Since(started), err)
	}
	if _, ok := s.upstreamCache[p.ID]; ok {
		t.Fatal("超时不应写入成功缓存")
	}
}
