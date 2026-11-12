package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/settings"
)

// TestIndependentRefreshCycles 通过真实 HTTP 与 SQLite 验证独立周期、手动字段保护、静默失败和主动失败反馈。
func TestIndependentRefreshCycles(t *testing.T) {
	var balances, keys atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/usage" {
			balances.Add(1)
		} else if r.URL.Path == "/v1/sub2api/billing" {
			keys.Add(1)
		} else {
			w.WriteHeader(404)
			return
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/v1/usage" {
			fmt.Fprint(w, `{"remaining":8}`)
		} else {
			fmt.Fprint(w, `{"effective_rate_multiplier":0.75}`)
		}
	}))
	defer server.Close()
	a, p := combinedFixture(t, server.URL)
	ctx := context.Background()
	if _, err := a.SaveProviderAccount(ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	k, err := a.store.CreateKey(ctx, p.ID, keyDraft("manual-name", "fixture-key", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "manual", MultiplierSource: "upstream"}); err != nil {
		t.Fatal(err)
	}
	balances.Store(0)
	keys.Store(0)
	ticks := make(chan time.Time)
	read := make(chan struct{}, 1)
	done := make(chan error, 1)
	config := settings.AccountRefresh{Enabled: true, Minutes: 1, KeyEnabled: true, KeyMinutes: 3}
	go func() {
		done <- a.run(ctx, func(context.Context) (settings.AccountRefresh, error) { read <- struct{}{}; return config, nil }, ticks)
	}()
	now := time.Now()
	tick := func(seconds int) {
		t.Helper()
		select {
		case ticks <- now.Add(time.Duration(seconds) * time.Second):
		case <-time.After(3 * time.Second):
			t.Fatal("调度未响应")
		}
		<-read
	}
	tick(0)
	tick(60)
	tick(61)
	if balances.Load() != 1 || keys.Load() != 0 {
		t.Fatalf("余额周期误查Key: %d/%d", balances.Load(), keys.Load())
	}
	tick(180)
	tick(181)
	if balances.Load() != 2 || keys.Load() != 1 {
		t.Fatalf("独立周期失效: %d/%d", balances.Load(), keys.Load())
	}
	close(ticks)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	beforeBalance, err := a.ProviderAccount(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeKey, err := a.ProviderKeyQuery(ctx, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if err = a.RefreshBackground(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	afterBalance, _ := a.ProviderAccount(ctx, p.ID)
	afterKey, _ := a.ProviderKeyQuery(ctx, k.ID)
	if !reflect.DeepEqual(beforeBalance.Balance, afterBalance.Balance) || !reflect.DeepEqual(beforeKey, afterKey) {
		t.Fatal("后台失败改变有效值或失败状态")
	}
	list, err := a.store.Keys(ctx, p.ID)
	if err != nil || list[0].Name != "manual-name" || *list[0].Multiplier != 0.75 {
		t.Fatal("后台失败覆盖Key", err)
	}
	if _, err = a.RefreshProviderAccounts(ctx, ""); err != nil {
		t.Fatal(err)
	}
	manual, _ := a.ProviderAccount(ctx, p.ID)
	manualKey, _ := a.ProviderKeyQuery(ctx, k.ID)
	if manual.Balance.Status.State != "failed" || manualKey.MultiplierStatus.State != "failed" {
		t.Fatal("主动刷新未反馈失败")
	}
}
