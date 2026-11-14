package records

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testVault 为隔离测试提供内存主密钥，不访问用户凭据。
type testVault struct{ value []byte }

// Load 读取当前测试主密钥。
func (v *testVault) Load() ([]byte, error) {
	if v.value == nil {
		return nil, providers.ErrNoMasterKey
	}
	return v.value, nil
}

// Save 保存当前测试主密钥。
func (v *testVault) Save(data []byte) error { v.value = data; return nil }

// fixture 创建真实 SQLite 与独立价格，不访问日常数据库或用户凭据。
func fixture(t *testing.T) (*Store, *models.Store) {
	t.Helper()
	catalog, err := models.Open(filepath.Join(t.TempDir(), "records.sqlite"), http.DefaultClient, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	m := models.Model{ID: "m1", ModelID: "logical", Name: "测试模型", Pricing: models.Pricing{Base: []models.Rate{{Key: "input", Price: 2, Unit: "million_tokens"}, {Key: "output", Price: 8, Unit: "million_tokens"}, {Key: "cache_read", Price: 0.5, Unit: "million_tokens"}}}}
	raw, _ := json.Marshal(m)
	if _, err = catalog.DB().Exec(`INSERT INTO models VALUES('m1','logical',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := providers.New(catalog.DB(), &testVault{}); err != nil {
		t.Fatal(err)
	}
	s, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	return s, catalog
}

// exchange 通过真实 HTTP 传输写入一次调用，不在测试复制采集逻辑。
func exchange(t *testing.T, s *Store, endpoint, protocol string, stream bool) (Record, func(error) error) {
	t.Helper()
	ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: protocol, Type: "chat", Stream: stream, Body: []byte(`{"model":"logical","messages":[{"role":"user","content":"hello"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	one := 1.0
	ctx = WithTarget(ctx, Target{ProviderID: "p1", ProviderName: "原供应商", KeyID: "k1", KeyName: "原Key", Multiplier: &one})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"model":"upstream","api_key":"test-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	client := &http.Client{Transport: &Transport{Base: http.DefaultTransport}}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	page, err := s.Query(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(context.Background(), page.Records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, finish
}

// TestKeyNameSurvivesRedaction 验证名称与客户端凭据重合时，收尾仍保留配置身份且诊断内容继续脱敏。
func TestKeyNameSurvivesRedaction(t *testing.T) {
	for name, cause := range map[string]error{"success": nil, "failed": errors.New("magpie upstream-secret")} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t)
			ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "chat", Headers: http.Header{"Authorization": {"Bearer magpie"}}})
			if err != nil {
				t.Fatal(err)
			}
			target := Target{ProviderID: "p-magpie", ProviderName: "Magpie", KeyID: "k-magpie", KeyName: "magpie"}
			if err := Candidates(ctx, []Target{target}); err != nil {
				t.Fatal(err)
			}
			ctx = WithTarget(ctx, target)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"message":"magpie upstream-secret"}`)
			}))
			defer upstream.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer upstream-secret")
			client := &http.Client{Transport: &Transport{Base: http.DefaultTransport}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("读取上游响应失败: %v %v", readErr, closeErr)
			}
			page, err := s.Query(context.Background(), Query{})
			if err != nil || len(page.Records) != 1 || page.Records[0].KeyName != target.KeyName {
				t.Fatalf("进行中名称错误: %+v %v", page, err)
			}
			id := page.Records[0].ID
			if err := finish(cause); err != nil {
				t.Fatal(err)
			}
			page, err = s.Query(context.Background(), Query{})
			if err != nil || len(page.Records) != 1 || page.Records[0].KeyName != target.KeyName {
				t.Fatalf("结束后名称被脱敏: %+v %v", page, err)
			}
			r, err := s.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Attempts) != 1 || len(r.Candidates) != 1 || r.Attempts[0].Payload == nil {
				t.Fatal("缺少尝试、候选或响应快照")
			}
			for _, got := range []Target{r.Target, r.Attempts[0].Target, r.Candidates[0].Target} {
				if got.ProviderName != target.ProviderName || got.ProviderID != target.ProviderID || got.KeyID != target.KeyID || got.KeyName != target.KeyName {
					t.Fatalf("配置身份被脱敏改写: %+v", got)
				}
			}
			wantError := ""
			if cause != nil {
				wantError = "[REDACTED] [REDACTED]"
			}
			if r.Error != wantError || r.Attempts[0].Payload.ResponseBody != `{"message":"[REDACTED] [REDACTED]"}` {
				t.Fatalf("诊断内容未脱敏: %q %+v", r.Error, r.Attempts[0].Payload)
			}
		})
	}
}

// TestHTTPRecordSnapshot 验证实际收包、用量、价格快照、脱敏及幂等结算。
func TestHTTPRecordSnapshot(t *testing.T) {
	s, catalog := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("认证被记录层改变")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"test-secret"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer upstream.Close()
	r, finish := exchange(t, s, upstream.URL+"/v1/chat/completions", "chat", false)
	if r.State != "success" || r.Usage == nil || r.Usage.Input != 60 || r.Usage.Context != 100 || r.Usage.Output != 20 {
		t.Fatalf("记录不符: %+v", r)
	}
	want := 0.0003
	if r.Cost == nil || math.Abs(*r.Cost-want) > 1e-12 {
		t.Fatalf("费用 %v, want %v", r.Cost, want)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "test-secret") {
		t.Fatal("凭据泄漏")
	}
	if len(r.Attempts) != 1 || r.Attempts[0].Status != 200 || r.Attempts[0].Payload == nil {
		t.Fatal("缺少实际尝试")
	}
	if err := finish(nil); err != nil {
		t.Fatal(err)
	}
	var requests int
	if err := catalog.DB().QueryRow(`SELECT SUM(requests) FROM request_totals`).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("重复结算: %d %v", requests, err)
	}
	if _, err := catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.pricing.base[0].price',200)`); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(context.Background(), r.ID)
	if err != nil || again.Pricing.Base[0].Price != 2 {
		t.Fatal("历史价格被回算", err)
	}
}

// TestSSEUsage 验证多事件累计用量不重复相加，Anthropic 独立缓存上下文不遗漏。
func TestSSEUsage(t *testing.T) {
	s, _ := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":60,\"output_tokens\":1,\"cache_read_input_tokens\":40}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	r, _ := exchange(t, s, upstream.URL, "anthropic", true)
	if r.Usage == nil || r.Usage.Context != 100 || r.Usage.Input != 60 || r.Usage.Output != 20 {
		t.Fatalf("SSE 用量不符: %+v", r.Usage)
	}
}

// TestRetentionAndStablePagination 验证保留边界、清理不删累计和快照翻页不被新记录挤动。
func TestRetentionAndStablePagination(t *testing.T) {
	s, catalog := fixture(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	for i := 0; i < 25; i++ {
		_, finish, err := s.Begin(ctx, Input{ModelID: "m1", Protocol: "chat"})
		if err != nil {
			t.Fatal(err)
		}
		if err = finish(errors.New("没有可用上游")); err != nil {
			t.Fatal(err)
		}
		start = start.Add(time.Millisecond)
	}
	q := Query{Page: 1, Size: 10}
	first, err := s.Query(ctx, q)
	if err != nil || first.Summary.Total != 25 {
		t.Fatal(err, first.Summary)
	}
	q.Before = first.Before
	q.Page = 2
	before, err := s.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	start = start.Add(time.Second)
	_, finish, err := s.Begin(ctx, Input{ModelID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	after, err := s.Query(ctx, q)
	if err != nil || after.Records[0].ID != before.Records[0].ID || after.Summary.Total != 25 {
		t.Fatal("翻页被挤动", err)
	}
	id := first.Records[0].ID
	start = start.Add(8 * 24 * time.Hour)
	detail, err := s.Get(ctx, id)
	if err != nil || detail.Client.BodyState != "expired" || detail.Client.RequestBody != "" {
		t.Fatal("正文期限未生效", err)
	}
	if err = s.Expire(ctx); err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(ctx, 7)
	if err != nil || preview.Count != 26 {
		t.Fatal("预览不符", preview, err)
	}
	if count, err := s.Cleanup(ctx, preview.Before); err != nil || count != 26 {
		t.Fatal(count, err)
	}
	var total int
	if err := catalog.DB().QueryRow(`SELECT SUM(requests) FROM request_totals`).Scan(&total); err != nil || total != 26 {
		t.Fatal("清理破坏累计", total, err)
	}
	if _, err = s.Get(ctx, id); err == nil {
		t.Fatal("清理后仍可查询")
	}
}

// TestConcurrentRecords 验证并发真实请求各自隔离，不交叉身份或累计。
func TestConcurrentRecords(t *testing.T) {
	s, catalog := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); exchange(t, s, upstream.URL, "chat", false) }()
	}
	wg.Wait()
	var count int
	if err := catalog.DB().QueryRow(`SELECT SUM(requests) FROM request_totals`).Scan(&count); err != nil || count != 20 {
		t.Fatal(count, err)
	}
}

// TestMillionRecordQuery 在独立数据库验证百万条查询，显式环境开关避免日常测试重复造大数据。
func TestMillionRecordQuery(t *testing.T) {
	if os.Getenv("VELA_RECORDS_SCALE") != "1" {
		t.Skip("仅容量验收时生成百万条数据")
	}
	s, catalog := fixture(t)
	now := time.Now().UnixMilli()
	raw, _ := json.Marshal(Record{ModelID: "m1", Model: "logical", ModelName: "测试模型", State: "success", Source: "business", Protocol: "chat", Type: "chat", Usage: &Usage{Input: 60, Output: 20, Context: 100, CacheRead: 40}, Attempts: []Attempt{}, EndedAt: now, StartedAt: now})
	started := time.Now()
	_, err := catalog.DB().Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000000) INSERT INTO request_records(id,started_at,ended_at,model_id,provider_id,protocol,source,state,search,document,input_context,output_tokens,cache_read,cost,usage_known,duration_ms) SELECT printf('%032d',x),?,?,'m1','','chat','business','success','logical',json_set(?,'$.id',printf('%032d',x)),100,20,40,NULL,1,0 FROM n`, now, now, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("生成百万条耗时 %s", time.Since(started))
	started = time.Now()
	page, err := s.Query(context.Background(), Query{Size: 20, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Summary.Total != 1000000 || len(page.Records) != 20 || page.Summary.Tokens != 120000000 {
		t.Fatal("百万条分页或合计不符", page.Summary)
	}
	t.Logf("百万条整组摘要和分页耗时 %s，前端仅接收 %d 条", time.Since(started), len(page.Records))
}

// TestProtocolUsageAndPrivacy 验证四协议、未知字段、缓存与媒体脱敏口径，不从实现复制计算。
func TestProtocolUsageAndPrivacy(t *testing.T) {
	tests := []struct {
		name, protocol, body         string
		input, output, context, read int64
	}{
		{"responses", "responses", `{"response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40}}}}`, 60, 20, 100, 40},
		{"gemini", "gemini", `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":18,"thoughtsTokenCount":2,"cachedContentTokenCount":40}}`, 60, 20, 100, 40},
		{"anthropic", "anthropic", `{"usage":{"input_tokens":60,"output_tokens":20,"cache_read_input_tokens":40,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`, 60, 20, 130, 40},
		{"embedding", "embedding", `{"usage":{"prompt_tokens":100,"total_tokens":100}}`, 100, 0, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := parseUsage([]byte(tt.body), tt.protocol)
			if u == nil || u.Input != tt.input || u.Output != tt.output || u.Context != tt.context || u.CacheRead != tt.read || !u.inputKnown || !u.outputKnown {
				t.Fatalf("归一化不符: %+v", u)
			}
		})
	}
	if parseUsage([]byte(`{"usage":{}}`), "chat") != nil {
		t.Fatal("空用量被当作零")
	}
	if u := parseUsage([]byte(`{"usage":{"output_tokens":4}}`), "anthropic"); u == nil || u.inputKnown {
		t.Fatal("部分用量被当作完整")
	}
	raw := `{"data":[{"b64_json":"PRIVATE_MEDIA","size":"1024x1024"}],"inlineData":{"data":"OTHER_MEDIA","mimeType":"image/png"},"authorization":"Bearer secret-value"}`
	safe := sanitize(raw, []string{"secret-value"})
	if strings.Contains(safe, "PRIVATE_MEDIA") || strings.Contains(safe, "OTHER_MEDIA") || strings.Contains(safe, "secret-value") || !strings.Contains(safe, "1024x1024") {
		t.Fatal("媒体或认证脱敏错误", safe)
	}
}

// TestCancelledRequestAndCleanup 验证取消后仍保存终态，进行中不被清理或重复计数。
func TestCancelledRequestAndCleanup(t *testing.T) {
	s, catalog := fixture(t)
	ctx := context.Background()
	_, finish, err := s.Begin(ctx, Input{ModelID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(ctx, 0)
	if err != nil || preview.Count != 0 {
		t.Fatal("进行中进入清理", preview, err)
	}
	if err = finish(context.Canceled); err != nil {
		t.Fatal(err)
	}
	page, err := s.Query(ctx, Query{})
	if err != nil || page.Summary.Total != 1 || page.Records[0].State != "local" {
		t.Fatal("取消未收尾", err)
	}
	if _, err = New(catalog.DB()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = catalog.DB().QueryRow(`SELECT SUM(requests) FROM request_totals`).Scan(&count); err != nil || count != 1 {
		t.Fatal("重启重复结算", count, err)
	}
}

// TestMediaPricing 用明确预期金额核对媒体 Token 拆分、混合缓存未知及图片像素价格。
func TestMediaPricing(t *testing.T) {
	one := 1.0
	u := parseUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"audio_tokens":30},"completion_tokens_details":{"audio_tokens":10}}}`), "chat")
	p := models.Pricing{Base: []models.Rate{{Key: "input", Price: 2}, {Key: "output", Price: 8}, {Key: "audio_input", Price: 10}, {Key: "audio_output", Price: 20}}}
	cost, charges := estimate(p, u, &one, "", time.Now())
	if cost == nil || math.Abs(*cost-0.00072) > 1e-12 || len(charges) != 4 {
		t.Fatal("媒体拆分费用错误", cost, charges)
	}
	mixed := parseUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"audio_tokens":30,"cached_tokens":10}}}`), "chat")
	if cost, _ = estimate(p, mixed, &one, "", time.Now()); cost != nil {
		t.Fatal("混合媒体缓存不可猜测")
	}
	p = models.Pricing{Images: []models.ImagePrice{{MinPixels: 1000000, MaxPixels: 1100000, Quality: "high", Price: 0.04}}}
	cost, _ = estimate(p, &Usage{Unit: "image", Quantity: 2, Specification: "1024x1024", Quality: "high"}, &one, "", time.Now())
	if cost == nil || math.Abs(*cost-0.08) > 1e-12 {
		t.Fatal("图片像素档费用错误", cost)
	}
	p = models.Pricing{Base: []models.Rate{{Key: "per_request", Price: 0.02, Unit: "request"}}}
	cost, _ = estimate(p, u, &one, "", time.Now())
	if cost == nil || math.Abs(*cost-0.02) > 1e-12 {
		t.Fatal("按请求计费错误", cost)
	}
}

// TestEstimateInactiveCacheDuration 验证零缓存写入不受未记录的缓存时长价格影响，真实写入仍保持未知。
func TestEstimateInactiveCacheDuration(t *testing.T) {
	one := 1.0
	pricing := models.Pricing{Base: []models.Rate{
		{Key: "input", Price: 2, Unit: "million_tokens"},
		{Key: "output", Price: 10, Unit: "million_tokens"},
		{Key: "cache_read", Price: 0.1, Unit: "million_tokens"},
		{Key: "cache_write", Price: 2.5, Unit: "million_tokens"},
		{Key: "cache_write_12h", Price: 5, Unit: "million_tokens"},
	}}
	usage := &Usage{Input: 1938, Output: 938, CacheRead: 2458, Context: 4396}
	cost, _ := estimate(pricing, usage, &one, "", time.Now())
	if cost == nil || math.Abs(*cost-0.0135018) > 1e-12 {
		t.Fatalf("零缓存写入应正常计价: %v", cost)
	}
	usage.CacheWrite = 1
	if cost, _ = estimate(pricing, usage, &one, "", time.Now()); cost != nil {
		t.Fatal("缓存写入无法区分时长时应保持未知")
	}
}

// TestRecoveryAndKeyOwnership 验证重启未知终态及清理后按真实尝试保留 Key 消耗、身份和幂等性。
func TestRecoveryAndKeyOwnership(t *testing.T) {
	s, catalog := fixture(t)
	ctx := context.Background()
	pending, _, err := s.Begin(ctx, Input{ModelID: "m1", Protocol: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	one := 1.0
	run := pending.Value(sessionKey{}).(*session)
	run.record.Attempts = []Attempt{{Target: Target{ProviderID: "p1", KeyID: "k1", KeyName: "旧名", Multiplier: &one}, Number: 1}}
	if err = s.save(ctx, run.record, false); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	page, err := restarted.Query(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].State != "failed" || page.Records[0].Cost != nil || !strings.Contains(page.Records[0].Error, "未知") {
		t.Fatal("恢复冒充成功", page.Records)
	}
	if _, err = New(catalog.DB()); err != nil {
		t.Fatal(err)
	}
	p, err := s.Preview(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cleanup(ctx, p.Before); err != nil {
		t.Fatal(err)
	}
	keys, err := s.KeyTotals(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].KeyID != "k1" || keys[0].Attempts != 1 || keys[0].UnknownCost != 1 {
		t.Fatal("Key 归属或幂等错误", keys)
	}
}

// TestAttemptKeyAllocation 真实 HTTP 调用不同 Key，结算后各自保留消耗且逻辑请求只累计一次。
func TestAttemptKeyAllocation(t *testing.T) {
	s, _ := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	defer upstream.Close()
	ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &Transport{}}
	one := 1.0
	for _, key := range []string{"k1", "k2"} {
		target := WithTarget(ctx, Target{ProviderID: "p1", KeyID: key, KeyName: "同名Key", Multiplier: &one})
		req, _ := http.NewRequestWithContext(target, http.MethodPost, upstream.URL, strings.NewReader(`{"model":"upstream"}`))
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	keys, err := s.KeyTotals(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatal(keys)
	}
	for _, key := range keys {
		if key.Attempts != 1 || key.Tokens != 120 || math.Abs(key.Cost-0.00036) > 1e-12 {
			t.Fatal(key)
		}
	}
	page, err := s.Query(context.Background(), Query{})
	if err != nil || page.Summary.Total != 1 || page.Summary.Tokens != 240 {
		t.Fatal(page.Summary, err)
	}
}

// TestHTTPProtocols 通过真实 HTTP 与持久化详情验证协议用量及媒体计量，避免只测孤立解析器。
func TestHTTPProtocols(t *testing.T) {
	cases := []struct {
		protocol, body string
		tokens         int64
		unit           string
		quantity       float64
	}{
		{"chat", `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`, 120, "", 0},
		{"responses", `{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40}}}`, 120, "", 0},
		{"anthropic", `{"usage":{"input_tokens":60,"output_tokens":20,"cache_read_input_tokens":40}}`, 120, "", 0},
		{"gemini", `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":40}}`, 120, "", 0},
		{"image", `{"data":[{"b64_json":"PRIVATE_MEDIA","size":"1024x1024"}]}`, 0, "image", 1},
	}
	for _, tt := range cases {
		t.Run(tt.protocol, func(t *testing.T) {
			s, _ := fixture(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tt.body) }))
			defer upstream.Close()
			r, _ := exchange(t, s, upstream.URL, tt.protocol, false)
			if r.Usage == nil || r.Usage.Context+r.Usage.Output != tt.tokens || r.Usage.Unit != tt.unit || r.Usage.Quantity != tt.quantity {
				t.Fatal(r.Usage)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "PRIVATE_MEDIA") {
				t.Fatal("媒体二进制落库")
			}
		})
	}
}

// TestVideoPolling 不把任务轮询当成重试或重复收费，最终以真实完成时长结算。
func TestVideoPolling(t *testing.T) {
	s, catalog := fixture(t)
	if _, err := catalog.DB().Exec(`UPDATE models SET document=json_set(document,'$.pricing.base',json('[{"key":"per_second","price":0.1,"unit":"second"}]'))`); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"id":"job1","status":"queued","seconds":"4"}`)
		} else {
			io.WriteString(w, `{"id":"job1","status":"completed","seconds":"4","size":"720p"}`)
		}
	}))
	defer upstream.Close()
	ctx, finish, err := s.Begin(context.Background(), Input{ModelID: "m1", Protocol: "video", Type: "video"})
	if err != nil {
		t.Fatal(err)
	}
	one := 1.0
	ctx = WithTarget(ctx, Target{KeyID: "k1", Multiplier: &one})
	client := &http.Client{Transport: &Transport{}}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		req, _ := http.NewRequestWithContext(ctx, method, upstream.URL, strings.NewReader(`{"size":"720p"}`))
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if err = finish(nil); err != nil {
		t.Fatal(err)
	}
	page, err := s.Query(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	r := page.Records[0]
	if r.Execution != "direct" || r.Usage == nil || r.Usage.Quantity != 4 || r.Cost == nil || math.Abs(*r.Cost-0.4) > 1e-12 || !r.Attempts[0].BillingPending || !r.Attempts[1].Polling {
		t.Fatalf("轮询重复收费或误判重试: %+v", r)
	}
}
