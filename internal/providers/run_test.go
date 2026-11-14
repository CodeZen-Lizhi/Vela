package providers

import (
	"context"
	"errors"
	"fmt"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestAccountRunRecoversFromErrors 通过真实定时循环、SQLite 故障与 HTTP 验证设置/查询恢复，使用时钟通道推进分钟周期。
func TestAccountRunRecoversFromErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{"remaining":7}`) }))
	defer server.Close()
	a, p, _, _ := accountFixture(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := saveAndRefreshProviderAccount(a, ctx, p.ID, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "fixture-run"}}); err != nil {
		t.Fatal(err)
	}
	requests.Store(0)
	var document string
	if err := a.store.db.QueryRow("SELECT document FROM provider_accounts WHERE provider_id=?", p.ID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec("UPDATE provider_accounts SET document='{' WHERE provider_id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	settingsRead := make(chan struct{}, 1)
	stopped := make(chan struct{})
	result := make(chan error, 1)
	var mode atomic.Int32
	go func() {
		defer close(stopped)
		result <- a.run(ctx, func(context.Context) (settings.AccountRefresh, error) {
			current := mode.Load()
			settingsRead <- struct{}{}
			switch current {
			case 0:
				return settings.AccountRefresh{}, errors.New("fixture settings failure")
			case 1:
				return settings.AccountRefresh{Enabled: true, Minutes: 0, KeyMinutes: 30}, nil
			case 2:
				return settings.AccountRefresh{Enabled: true, Minutes: 1, KeyMinutes: 30}, nil
			default:
				return settings.AccountRefresh{Minutes: 1, KeyMinutes: 30}, nil
			}
		}, ticks)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("run did not stop on cancellation")
		}
	})
	now := time.Now()
	// 每次推送下一拍意味着上一拍已完整完成；设置回调确认本拍已开始。
	tick := func(seconds int) {
		t.Helper()
		select {
		case ticks <- now.Add(time.Duration(seconds) * time.Second):
		case <-stopped:
			t.Fatal("run stopped after recoverable error")
		case <-time.After(2 * time.Second):
			t.Fatal("run did not read tick")
		}
		select {
		case <-settingsRead:
		case <-stopped:
			t.Fatal("run stopped before settings read")
		case <-time.After(2 * time.Second):
			t.Fatal("settings not read")
		}
	}
	tick(0) // readSettings 错误。
	mode.Store(1)
	tick(1) // 非法分钟间隔。
	mode.Store(2)
	tick(2)  // 设置恢复，首次周期在第 62 秒到期。
	tick(62) // 真实 SQLite 文档损坏导致本轮失败。
	tick(63) // 确认失败已经结束且没有永久退出或每秒重试。
	if requests.Load() != 0 {
		t.Fatal("query ignored invalid persisted state")
	}
	if _, err := a.store.db.Exec("UPDATE provider_accounts SET document=? WHERE provider_id=?", document, p.ID); err != nil {
		t.Fatal(err)
	}
	tick(122)
	tick(123)
	if requests.Load() != 1 {
		t.Fatalf("recovered cycle requests=%d", requests.Load())
	}
	account, err := a.ProviderAccount(ctx, p.ID)
	if err != nil || account.Balance.Amount == nil || *account.Balance.Amount != 7 || account.Balance.Status.State != "success" {
		t.Fatalf("recovery result %+v %v", account, err)
	}
	tick(182)
	tick(183)
	if requests.Load() != 2 {
		t.Fatal("later cycle did not continue")
	}
	mode.Store(3)
	tick(184)
	tick(240)
	if requests.Load() != 2 {
		t.Fatal("disabled timer queried upstream")
	}
	mode.Store(2)
	tick(241)
	tick(301)
	tick(302)
	if requests.Load() != 3 {
		t.Fatal("reenabled timer did not resume")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not terminate run")
	}
}
