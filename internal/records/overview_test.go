package records

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// overviewSums 分开合计逻辑请求与实际 Key 尝试，避免测试将两个口径混加。
func overviewSums(t *testing.T, s *Store) (OverviewSnapshot, OverviewMetrics, OverviewMetrics) {
	t.Helper()
	snapshot, err := s.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var logical, keys OverviewMetrics
	for _, r := range snapshot.Rows {
		if r.KeyID == "" {
			logical.add(r.Metrics)
		} else {
			keys.add(r.Metrics)
		}
	}
	return snapshot, logical, keys
}

// TestOverviewHTTPSettlement 验证真实 Begin/HTTP 重试结算、零倍率标准价格、活动实时计数、清理及重启持久化。
func TestOverviewHTTPSettlement(t *testing.T) {
	s, catalog := fixture(t)
	ctx := context.Background()
	zone := time.FixedZone("+05:30", 19800)
	s.zone = zone
	now := time.Date(2026, 11, 13, 12, 45, 0, 0, zone)
	s.now = func() time.Time { return now }
	if _, err := catalog.DB().Exec(`UPDATE settings SET document='+05:30' WHERE name='records-timezone'`); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/failed" {
			w.WriteHeader(500)
		}
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer server.Close()
	if _, err := catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.pricing.tiers',json('[{"above":150,"rates":[{"key":"input","price":20,"unit":"million_tokens"},{"key":"output","price":80,"unit":"million_tokens"},{"key":"cache_read","price":5,"unit":"million_tokens"}]}]'))`); err != nil {
		t.Fatal(err)
	}
	run, finish, err := s.Begin(ctx, Input{ModelID: "m1", Protocol: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, active, _ := overviewSums(t, s); len(snapshot.Rows) != 1 || active.Active != 1 || active.Requests != 1 || active.Tokens != 0 {
		t.Fatal("活动请求未正确实时计数", snapshot)
	}
	zero := 0.0
	client := &http.Client{Transport: &Transport{Base: http.DefaultTransport}}
	for _, path := range []string{"/failed", "/success"} {
		target := WithTarget(run, Target{ProviderID: "p1", ProviderName: "供应商", KeyID: path, KeyName: path, Multiplier: &zero})
		req, err := http.NewRequestWithContext(target, "POST", server.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	snapshot, logical, keys := overviewSums(t, s)
	if logical.Requests != 1 || logical.Success != 1 || logical.Failed != 0 || logical.DetailsKnown != 1 || logical.Tokens != 240 || logical.Input != 120 || logical.Cached != 80 || logical.Cost != 0 || math.Abs(logical.StandardCost-0.0006) > 1e-12 || logical.UnknownStandard != 0 {
		t.Fatalf("逻辑汇总错误: %+v", logical)
	}
	if keys.Requests != 2 || keys.Success != 1 || keys.Failed != 1 || keys.Tokens != 240 || keys.FirstByteCount != 2 || logical.FirstByteCount != 1 || keys.Cost != 0 || math.Abs(keys.StandardCost-logical.StandardCost) > 1e-12 {
		t.Fatalf("Key 消耗错误: %+v", keys)
	}
	if snapshot.Timezone != "+05:30" || snapshot.FirstDay != "2026-11-13" {
		t.Fatal(snapshot)
	}
	for _, row := range snapshot.Rows {
		if row.Time != time.Date(2026, 11, 13, 12, 0, 0, 0, zone).UnixMilli() || !strings.Contains(row.ProviderName, "已删除") {
			t.Fatal("小时或删除标记错误", row)
		}
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.name','改名模型','$.pricing.base[0].price',200)`); err != nil {
		t.Fatal(err)
	}
	renamed, logicalAfter, _ := overviewSums(t, s)
	if logicalAfter != logical {
		t.Fatal("价格受现价影响", logicalAfter)
	}
	for _, r := range renamed.Rows {
		if r.ModelName != "改名模型" {
			t.Fatal("未读取现名", r)
		}
	}
	if count, err := s.Cleanup(ctx, now.UnixMilli()); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	after, _, _ := overviewSums(t, s)
	if !reflect.DeepEqual(after, renamed) {
		t.Fatal("清理影响长期统计")
	}
	restarted, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	again, _, _ := overviewSums(t, restarted)
	if !reflect.DeepEqual(after, again) {
		t.Fatal("重启重复回填", again)
	}
}

// TestOverviewLegacyBackfill 验证存续正文与已清理日累计同维度扣减，未知状态不补成功且重启不重复。
func TestOverviewLegacyBackfill(t *testing.T) {
	s, catalog := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer server.Close()
	record, _ := exchange(t, s, server.URL, "chat", false)
	// 模拟升级前只存日累计：其中两次逻辑调用、三次 Key 尝试已丢失正文。
	for _, query := range []string{
		`DELETE FROM settings WHERE name='records-overview-v1'`,
		`DELETE FROM request_overview`,
		`UPDATE request_totals SET requests=requests+2,tokens=tokens+50,cost=cost+0.25,unknown_usage=unknown_usage+1,unknown_cost=unknown_cost+1`,
		`UPDATE request_key_totals SET attempts=attempts+3,tokens=tokens+60,cost=cost+0.5,unknown_usage=unknown_usage+2,unknown_cost=unknown_cost+2`,
	} {
		if _, err := catalog.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	migrated, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, logical, keys := overviewSums(t, migrated)
	if logical.Requests != 3 || logical.DetailsKnown != 1 || logical.Success != 1 || logical.Failed != 0 || logical.Tokens != 170 || math.Abs(logical.Cost-(*record.Cost+0.25)) > 1e-12 || logical.UnknownUsage != 1 || logical.UnknownCost != 1 || logical.UnknownStandard != 2 {
		t.Fatalf("历史逻辑累计重复或伪造: %+v", logical)
	}
	if keys.Requests != 4 || keys.DetailsKnown != 1 || keys.Success != 1 || keys.Tokens != 180 || math.Abs(keys.Cost-(*record.Cost+0.5)) > 1e-12 || keys.UnknownUsage != 2 || keys.UnknownCost != 2 || keys.UnknownStandard != 3 {
		t.Fatalf("历史 Key 累计重复或伪造: %+v", keys)
	}
	restarted, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	again, _, _ := overviewSums(t, restarted)
	if !reflect.DeepEqual(snapshot, again) {
		t.Fatal("重启重复累计")
	}
}

// TestOverviewEmptyAndInvalidData 验证空数组契约、当前配置解析错误和累计 JSON 损坏不会返回伪造数据。
func TestOverviewEmptyAndInvalidData(t *testing.T) {
	s, catalog := fixture(t)
	snapshot, _, _ := overviewSums(t, s)
	raw, err := json.Marshal(snapshot)
	if err != nil || snapshot.FirstDay != "" || !strings.Contains(string(raw), `"rows":[]`) {
		t.Fatal(string(raw), err)
	}
	r := Record{ID: "unknown", StartedAt: s.now().UnixMilli(), EndedAt: s.now().UnixMilli(), ModelID: "m1", State: "unrecognized"}
	if err = s.save(context.Background(), r, true); err != nil {
		t.Fatal(err)
	}
	_, logical, _ := overviewSums(t, s)
	if logical.Success != 0 || logical.Failed != 0 || logical.UnknownUsage != 1 || logical.UnknownCost != 1 || logical.UnknownStandard != 1 {
		t.Fatal("未知状态或费用被补齐", logical)
	}
	if _, err = catalog.DB().Exec(`UPDATE request_overview SET metrics='[]'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Overview(context.Background()); err == nil {
		t.Fatal("损坏累计被静默吞掉")
	}
}

// TestOverviewMigrationRollback 验证无效历史正文使整次回填回滚，修复后可安全重新迁移。
func TestOverviewMigrationRollback(t *testing.T) {
	s, catalog := fixture(t)
	r := Record{ID: "bad", StartedAt: s.now().UnixMilli(), EndedAt: s.now().UnixMilli(), ModelID: "m1", State: "failed"}
	if err := s.save(context.Background(), r, true); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM settings WHERE name='records-overview-v1'`, `DELETE FROM request_overview`, `UPDATE request_records SET document='invalid'`} {
		if _, err := catalog.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(catalog.DB()); err == nil {
		t.Fatal("损坏正文应阻止迁移")
	}
	var count int
	if err := catalog.DB().QueryRow(`SELECT COUNT(*) FROM settings WHERE name='records-overview-v1'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("错误提交迁移标记", count, err)
	}
	raw, _ := json.Marshal(r)
	if _, err := catalog.DB().Exec(`UPDATE request_records SET document=?`, string(raw)); err != nil {
		t.Fatal(err)
	}
	migrated, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	_, logical, _ := overviewSums(t, migrated)
	if logical.Requests != 1 || logical.Failed != 1 {
		t.Fatal(logical)
	}
}

// TestOverviewSettlementRollback 验证后续日累计写入失败时，终态与小时累计也回滚。
func TestOverviewSettlementRollback(t *testing.T) {
	s, catalog := fixture(t)
	ctx := context.Background()
	_, finish, err := s.Begin(ctx, Input{ModelID: "m1", Protocol: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.DB().Exec(`CREATE TRIGGER fail_daily BEFORE INSERT ON request_totals BEGIN SELECT RAISE(ABORT,'test settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = finish(nil); err == nil {
		t.Fatal("应返回结算失败")
	}
	snapshot, active, _ := overviewSums(t, s)
	if len(snapshot.Rows) != 1 || active.Active != 1 || active.Success != 0 || active.Cost != 0 {
		t.Fatal("失败结算应只保留活动请求", snapshot)
	}
	var settled int
	if err = catalog.DB().QueryRow(`SELECT COUNT(*) FROM request_overview`).Scan(&settled); err != nil || settled != 0 {
		t.Fatal("小时累计未回滚", settled, err)
	}
	var state string
	if err = catalog.DB().QueryRow(`SELECT state FROM request_records`).Scan(&state); err != nil || state != "active" {
		t.Fatal("终态未回滚", state, err)
	}
	if _, err = catalog.DB().Exec(`DROP TRIGGER fail_daily`); err != nil {
		t.Fatal(err)
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	_, logical, _ := overviewSums(t, s)
	if logical.Requests != 1 || logical.DetailsKnown != 1 {
		t.Fatal(logical)
	}
}

// TestOverviewDimensionLimit 验证超出查询边界时显式失败，不能向调用者返回被截断的成功快照。
func TestOverviewDimensionLimit(t *testing.T) {
	s, catalog := fixture(t)
	metrics, err := json.Marshal(OverviewMetrics{Requests: 1, DetailsKnown: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.DB().Exec(`WITH RECURSIVE dimensions(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM dimensions WHERE n<100001)
      INSERT INTO request_overview SELECT n,'2026-11-13','','m1','chat','','','模型','',? FROM dimensions`, string(metrics))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Overview(context.Background())
	if err == nil || !strings.Contains(err.Error(), "超过十万") || len(snapshot.Rows) != 0 {
		t.Fatal("超限返回了不完整结果", len(snapshot.Rows), err)
	}
}
