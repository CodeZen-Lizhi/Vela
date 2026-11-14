package app

import (
	"context"
	"fmt"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestProviderUpstreamModelsUnavailable 验证未初始化的服务不伪造候选或成功结果。
func TestProviderUpstreamModelsUnavailable(t *testing.T) {
	service := &VelaService{}
	if _, err := service.ProviderUpstreamModels(context.Background(), "missing", false); err == nil {
		t.Fatal("缺少供应商存储时应失败")
	}
}

// TestProviderUpstreamModelsRealHTTP 经应用接口复用真实调度连接池，并确认查询不改变关联和映射。
func TestProviderUpstreamModelsRealHTTP(t *testing.T) {
	f := newExecutionFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") == "" {
			t.Error("请求地址或鉴权错误")
		}
		fmt.Fprint(w, `{"data":[{"id":"available-model"}]}`)
	}))
	defer server.Close()
	ctx := context.Background()
	if _, err := f.vendors.Update(ctx, f.provider.ID, providers.ProviderDraft{Name: f.provider.Name, URL: server.URL, Request: f.provider.Request}); err != nil {
		t.Fatal(err)
	}
	before, err := f.vendors.Models(ctx, f.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := &VelaService{providers: f.vendors, execution: f.execution}
	result, err := service.ProviderUpstreamModels(ctx, f.provider.ID, false)
	if err != nil || !reflect.DeepEqual(result.ModelNames, []string{"available-model"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = service.ProviderUpstreamModels(ctx, f.provider.ID, false)
	if err != nil || !result.FromCache {
		t.Fatalf("cache=%+v err=%v", result, err)
	}
	after, err := f.vendors.Models(ctx, f.provider.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("查询改变了关联/映射: %v", err)
	}
}
