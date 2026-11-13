package models

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// liteTestEntry 调用真实目录解析入口并要求恰好返回一个候选。
func liteTestEntry(t *testing.T, data string) CatalogEntry {
	t.Helper()
	entries, err := decodeLiteCatalog([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d: %+v", len(entries), entries)
	}
	return entries[0]
}

// liteTestRate 验证真实解析结果中的维度和单价，缺失维度直接失败。
func liteTestRate(t *testing.T, rates []Rate, key string, want float64) {
	t.Helper()
	for _, r := range rates {
		if r.Key == key {
			if math.Abs(r.Price-want) > 1e-10 {
				t.Fatalf("%s=%g want %g", key, r.Price, want)
			}
			return
		}
	}
	t.Fatalf("missing %s: %+v", key, rates)
}

// TestLiteDeepSeek 验证实际来源结构的别名去重、百万 Token 换算、零缓存和跨午夜分时。
func TestLiteDeepSeek(t *testing.T) {
	const row = `{"litellm_provider":"deepseek","mode":"chat","max_input_tokens":1000000,"max_output_tokens":393216,"input_cost_per_token":0.0000003,"output_cost_per_token":0.0000012,"cache_read_input_token_cost":0.000000006,"input_cost_per_token_cache_hit":0.000000006,"cache_creation_input_token_cost":0,"source":"https://api-docs.deepseek.com/quick_start/pricing","supports_reasoning":true,"off_peak_pricing":{"input_cost_per_token":0.00000015,"output_cost_per_token":0.0000006,"cache_read_input_token_cost":0.000000003,"windows":[{"hours_utc":["00:00-01:00","04:00-06:00","10:00-00:00"],"weekdays":[1,2,3,4,5]},{"hours_utc":"00:00-00:00","weekdays":[6,7]}]}}`
	entry := liteTestEntry(t, `{"deepseek-flash":`+row+`,"deepseek/deepseek-flash":`+row+`}`)
	if entry.Problem != "" {
		t.Fatal(entry.Problem)
	}
	if entry.ModelID != "deepseek-flash" || entry.SourceKey != "deepseek/deepseek-flash" || entry.CanonicalID != "deepseek/deepseek-flash" || !entry.Official {
		t.Fatalf("identity: %+v", entry)
	}
	liteTestRate(t, entry.Pricing.Base, "input", 0.3)
	liteTestRate(t, entry.Pricing.Base, "cache_read", 0.006)
	liteTestRate(t, entry.Pricing.Base, "cache_write", 0)
	if len(entry.Pricing.Base) != 4 || len(entry.Pricing.Schedules) != 4 {
		t.Fatalf("pricing: %+v", entry.Pricing)
	}
	if !slices.Contains(entry.Capabilities, "reasoning") || len(entry.Metadata) == 0 {
		t.Fatalf("metadata: %+v", entry)
	}
	for _, s := range entry.Pricing.Schedules {
		if s.Multiplier != 0.5 || s.Timezone != "UTC" {
			t.Fatalf("schedule: %+v", s)
		}
	}
	windows := [][2]string{{"00:00", "01:00"}, {"04:00", "06:00"}, {"10:00", "24:00"}, {"00:00", "24:00"}}
	for i, window := range windows {
		s := entry.Pricing.Schedules[i]
		days := []int{1, 2, 3, 4, 5}
		if i == 3 {
			days = []int{6, 0}
		}
		if s.Start != window[0] || s.End != window[1] || !slices.Equal(s.Days, days) {
			t.Fatalf("合并窗口与来源时段不等价: %+v", s)
		}
	}
}

// TestLiteTiersAndModes 验证多个阈值整档继承、长期缓存及三个处理模式的真实字段结构。
func TestLiteTiersAndModes(t *testing.T) {
	e := liteTestEntry(t, `{"openai/m":{"litellm_provider":"openai","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"cache_creation_input_token_cost_above_12hr":0.000003,"cache_creation_input_token_cost_above_30m":0.000004,"cache_creation_input_token_cost_above_1hr":0.000005,"input_cost_per_token_above_100k_tokens":0.000002,"output_cost_per_token_above_200k_tokens":0.000006,"input_cost_per_token_fast":0.00001,"output_cost_per_token_fast":0.00002,"input_cost_per_token_above_100k_tokens_fast":0.00003,"input_cost_per_token_priority":0.00004,"input_cost_per_token_ultrafast":0.00005}}`)
	if e.Problem != "" {
		t.Fatal(e.Problem)
	}
	if len(e.Pricing.Tiers) != 2 || e.Pricing.Tiers[0].Above != 100000 || e.Pricing.Tiers[1].Above != 200000 {
		t.Fatal(e.Pricing.Tiers)
	}
	liteTestRate(t, e.Pricing.Tiers[1].Rates, "input", 2)
	liteTestRate(t, e.Pricing.Tiers[1].Rates, "output", 6)
	liteTestRate(t, e.Pricing.Tiers[1].Rates, "cache_write_12h", 3)
	liteTestRate(t, e.Pricing.Base, "cache_write_30m", 4)
	if len(e.Pricing.Modes) != 3 {
		t.Fatal(e.Pricing.Modes)
	}
	liteTestRate(t, e.Pricing.Modes[0].Tiers[0].Rates, "output", 20)
	f := liteTestEntry(t, `{"claude":{"litellm_provider":"anthropic","input_cost_per_token":0.000001,"provider_specific_entry":{"fast":2,"us":1.1}}}`)
	if f.Problem != "" || len(f.Pricing.Modes) != 1 || *f.Pricing.Modes[0].Multiplier != 2 || len(f.Warnings) != 1 {
		t.Fatalf("fast: %+v", f)
	}
}

// TestLiteExplicitTiers 验证快照中的 range 阶梯结构以及非法区间隔离。
func TestLiteExplicitTiers(t *testing.T) {
	e := liteTestEntry(t, `{"dashscope/qwen":{"litellm_provider":"dashscope","tiered_pricing":[{"range":[0,256000],"input_cost_per_token":0.00000005,"output_cost_per_token":0.0000004},{"range":[256000,1000000],"input_cost_per_token":0.00000025,"output_cost_per_token":0.000002}]}}`)
	if e.Problem != "" || e.ProviderID != "alibaba" || len(e.Pricing.Tiers) != 1 {
		t.Fatalf("tier: %+v", e)
	}
	liteTestRate(t, e.Pricing.Base, "input", 0.05)
	liteTestRate(t, e.Pricing.Tiers[0].Rates, "input", 0.25)
	bad := liteTestEntry(t, `{"x":{"litellm_provider":"openai","tiered_pricing":[{"range":[1,100],"input_cost_per_token":1}]}}`)
	if bad.Problem == "" {
		t.Fatal("accepted tier gap")
	}
}

// TestLiteMediaAndVector 验证图片每张与 Token、视频分辨率每秒及向量原始维度保留。
func TestLiteMediaAndVector(t *testing.T) {
	image := liteTestEntry(t, `{"openai/img":{"litellm_provider":"openai","mode":"image_generation","input_cost_per_image_token":0.000002,"output_cost_per_image_token":0.000004,"input_cost_per_image":0.01,"output_cost_per_image_1024":0.04,"output_cost_per_image_2K":0.08}}`)
	if image.Problem != "" || image.Kind != "image" || len(image.Pricing.Images) != 2 {
		t.Fatalf("image: %+v", image)
	}
	liteTestRate(t, image.Pricing.Base, "image_input", 2)
	liteTestRate(t, image.Pricing.Base, "image_output", 4)
	liteTestRate(t, image.Pricing.Base, "per_image_input", 0.01)
	video := liteTestEntry(t, `{"v":{"litellm_provider":"openai","mode":"video_generation","output_cost_per_second_720p":0.1,"output_cost_per_second_1080p":0.2}}`)
	if video.Problem != "" || video.Kind != "video" || len(video.Pricing.Videos) != 2 {
		t.Fatalf("video: %+v", video)
	}
	vector := liteTestEntry(t, `{"gemini/e":{"litellm_provider":"gemini","mode":"embedding","output_vector_size":3072,"input_cost_per_token":0,"output_cost_per_token":0.1}}`)
	if vector.Problem != "" || vector.Kind != "vector" || vector.ProviderID != "google" || len(vector.Pricing.Base) != 1 || !strings.Contains(string(vector.Metadata), `"output_vector_size":3072`) {
		t.Fatalf("vector: %+v", vector)
	}
}

// TestLiteIdentityAndWarnings 验证适配器精确映射、国际国内分离、中转身份及未知计价告警。
func TestLiteIdentityAndWarnings(t *testing.T) {
	for provider, want := range map[string]string{"moonshot": "moonshotai", "bigmodel": "zhipuai", "zai": "zai", "xiaomi_mimo": "xiaomi", "tencent": "tencent-tokenhub", "qwen_ai_platform": "alibaba"} {
		e := liteTestEntry(t, `{"`+provider+`/m":{"litellm_provider":"`+provider+`"}}`)
		if e.ProviderID != want || !e.Official || e.ModelID != "m" {
			t.Fatalf("identity: %+v", e)
		}
	}
	e := liteTestEntry(t, `{"bedrock/anthropic.claude":{"litellm_provider":"bedrock","input_cost_per_token":0.000001,"search_context_cost_per_query":{"high":0.1},"input_cost_per_pixel":1,"status":"deprecated"}}`)
	if e.Official || e.CanonicalID != "" || !e.Deprecated || len(e.Warnings) != 2 || e.ModelID != "bedrock/anthropic.claude" {
		t.Fatalf("relay: %+v", e)
	}
	empty := liteTestEntry(t, `{"m":{"litellm_provider":"openai"}}`)
	if len(empty.Pricing.Base) != 0 {
		t.Fatal("missing price became zero")
	}
}

// TestLiteMalformed 验证整份目录与单条价格错误的隔离，不接受字符串、null、负价和溢出。
func TestLiteMalformed(t *testing.T) {
	for _, data := range []string{`null`, `[]`, `{}`, `{"x":`, `{"sample_spec":{}}`} {
		if _, err := decodeLiteCatalog([]byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	for _, price := range []string{`null`, `"0"`, `-1`, `1e309`, `1e308`, `{}`} {
		e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":`+price+`}}`)
		if e.Problem == "" {
			t.Fatalf("accepted price %s", price)
		}
	}
	e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","cache_read_input_token_cost":1,"input_cost_per_token_cache_hit":2}}`)
	if e.Problem == "" {
		t.Fatal("accepted conflicting cache alias")
	}
}

// TestLiteScheduleTimezone 验证 UTC 小时与上海星期求交、跨午夜拆段及不可表达倍率拒绝。
func TestLiteScheduleTimezone(t *testing.T) {
	e := liteTestEntry(t, `{"tencent/m":{"litellm_provider":"tencent","input_cost_per_token":0.000002,"off_peak_pricing":{"input_cost_per_token":0.000001,"weekday_timezone":"Asia/Shanghai","windows":[{"hours_utc":"16:00-18:00","weekdays":[1]}]}}}`)
	if e.Problem != "" || len(e.Pricing.Schedules) != 1 {
		t.Fatalf("schedule: %+v", e)
	}
	s := e.Pricing.Schedules[0]
	if !slices.Equal(s.Days, []int{0}) || s.Start != "16:00" || s.End != "18:00" {
		t.Fatalf("timezone: %+v", s)
	}
	for _, off := range []string{
		`{"input_cost_per_token":0.000001,"output_cost_per_token":0.000001,"windows":[{"hours_utc":"00:00-01:00","weekdays":[1]}]}`,
		`{"input_cost_per_token":0.000001,"windows":[{"hours_utc":"00:00-01:00","weekdays":[0]}]}`,
		`{"input_cost_per_token":0.000001,"weekday_timezone":"America/New_York","windows":[{"hours_utc":"00:00-01:00","weekdays":[1]}]}`,
	} {
		e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":0.000002,"output_cost_per_token":0.000004,"off_peak_pricing":`+off+`}}`)
		if e.Problem == "" {
			t.Fatal("accepted unrepresentable off peak")
		}
	}
	cross := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":1,"off_peak_pricing":{"input_cost_per_token":0.5,"windows":[{"hours_utc":"22:00-02:00","weekdays":[1]}]}}}`)
	if cross.Problem != "" || len(cross.Pricing.Schedules) != 2 {
		t.Fatalf("cross: %+v", cross)
	}
}

// TestLiteActualSnapshot 使用已授权本机快照验证真实来源，其他环境可只运行可移植的固定样本。
func TestLiteActualSnapshot(t *testing.T) {
	data, err := os.ReadFile("/tmp/vela-pricing-audit-20261113/litellm.json")
	if os.IsNotExist(err) {
		t.Skip("本机审计快照未提供")
	}
	if err != nil {
		t.Fatal(err)
	}
	entries, err := decodeLiteCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	problems := 0
	problemCounts := map[string]int{}
	for _, e := range entries {
		if e.Problem != "" {
			problems++
			problemCounts[e.Problem]++
		}
		if e.ProviderID == "deepseek" && e.ModelID == "deepseek-flash" {
			found = true
			if e.Problem != "" {
				t.Fatal(e.Problem)
			}
			liteTestRate(t, e.Pricing.Base, "input", 0.3)
			liteTestRate(t, e.Pricing.Base, "output", 1.2)
		}
		if e.Problem == "" {
			if err := validatePricing(e.Pricing); err != nil {
				t.Fatalf("%s: %v", e.SourceKey, err)
			}
		}
	}
	if !found {
		t.Fatal("missing official deepseek-flash")
	}
	t.Logf("真实快照: %d 条候选，%d 条明确标记 Problem；%s", len(entries), problems, time.Now().UTC().Format(time.RFC3339))
	t.Logf("来源拒绝原因: %v", problemCounts)
	// 确保完整元数据仍能通过 JSON 边界往返。
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []CatalogEntry
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
}

// TestLitePresentationContract 验证厂商展示名、价格业务顺序、弃用日期和不支持类型过滤。
func TestLitePresentationContract(t *testing.T) {
	today := time.Now().UTC().Format(time.DateOnly)
	e := liteTestEntry(t, `{"gemini/m":{"litellm_provider":"gemini","deprecation_date":"`+today+`","input_cost_per_token":1,"output_cost_per_token":2,"cache_read_input_token_cost":3,"cache_creation_input_token_cost":4}}`)
	if e.ProviderName != "Google" || !e.Deprecated {
		t.Fatalf("presentation: %+v", e)
	}
	keys := []string{}
	for _, r := range e.Pricing.Base {
		keys = append(keys, r.Key)
	}
	if !slices.Equal(keys, []string{"input", "output", "cache_read", "cache_write"}) {
		t.Fatal(keys)
	}
	future := liteTestEntry(t, `{"m":{"litellm_provider":"openai","deprecation_date":"2999-01-01"}}`)
	if future.Deprecated {
		t.Fatal("future deprecation filtered early")
	}
	entries, err := decodeLiteCatalog([]byte(`{"chat":{"litellm_provider":"openai","mode":"chat"},"audio":{"litellm_provider":"openai","mode":"audio_speech"},"search":{"litellm_provider":"openai","mode":"search"},"realtime":{"litellm_provider":"openai","mode":"realtime"}}`))
	if err != nil || len(entries) != 1 || entries[0].ModelID != "chat" {
		t.Fatalf("unsupported modes: %+v %v", entries, err)
	}
}

// TestLiteDefaultMediaPrices 验证没有尺寸或分辨率的来源每张、每秒价格保持原始单位。
func TestLiteDefaultMediaPrices(t *testing.T) {
	e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","mode":"image_generation","input_cost_per_image":0.01,"output_cost_per_image":0.04}}`)
	if e.Problem != "" || len(e.Pricing.Images) != 0 {
		t.Fatalf("default image: %+v", e)
	}
	liteTestRate(t, e.Pricing.Base, "per_image", 0.04)
	liteTestRate(t, e.Pricing.Base, "per_image_input", 0.01)
	for _, r := range e.Pricing.Base {
		if r.Unit != "image" {
			t.Fatal(r)
		}
	}
	v := liteTestEntry(t, `{"v":{"litellm_provider":"openai","mode":"video_generation","input_cost_per_video_per_second":0.03,"output_cost_per_video_per_second":0.1}}`)
	if v.Problem != "" || len(v.Pricing.Videos) != 0 {
		t.Fatalf("default video: %+v", v)
	}
	liteTestRate(t, v.Pricing.Base, "per_second", 0.1)
	liteTestRate(t, v.Pricing.Base, "per_second_input", 0.03)
	for _, r := range v.Pricing.Base {
		if r.Unit != "second" {
			t.Fatal(r)
		}
	}
}

// TestLiteFlatSchedules 验证平铺时段、缺失星期默认每天，以及 ISO 数字字符串兼容。
func TestLiteFlatSchedules(t *testing.T) {
	for _, days := range []string{``, `,"weekdays":["1","2","3","4","5","6","7"]`, `,"weekdays":"1,2,3,4,5,6,7"`} {
		e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":1,"off_peak_pricing":{"input_cost_per_token":0.5,"hours_utc":"22:00-02:00"`+days+`}}}`)
		if e.Problem != "" || len(e.Pricing.Schedules) != 2 {
			t.Fatalf("flat schedule %s: %+v", days, e)
		}
	}
	for _, days := range []string{`[]`, `["0"]`, `["8"]`, `["1",1]`, `null`, `["monday"]`} {
		e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":1,"off_peak_pricing":{"input_cost_per_token":0.5,"hours_utc":"22:00-02:00","weekdays":`+days+`}}}`)
		if e.Problem == "" {
			t.Fatalf("accepted bad ISO weekdays %s", days)
		}
	}
}

// TestLiteWarningsGrouped 验证未知模式和附加服务不会逐字段淹没表单，完整原始字段仍可追溯。
func TestLiteWarningsGrouped(t *testing.T) {
	e := liteTestEntry(t, `{"m":{"litellm_provider":"openai","input_cost_per_token":1,"input_cost_per_token_batches":0.5,"output_cost_per_token_batches":1,"input_cost_per_token_flex":0.5,"output_cost_per_token_flex":1,"search_context_cost_per_query":{"high":1},"code_interpreter_cost_per_session":1}}`)
	if e.Problem != "" || len(e.Warnings) != 2 {
		t.Fatalf("warnings: %+v", e)
	}
	for _, w := range e.Warnings {
		if strings.Contains(w, "_cost_") || !strings.Contains(w, "保持未知") {
			t.Fatal(w)
		}
	}
	if !strings.Contains(string(e.Metadata), "output_cost_per_token_flex") {
		t.Fatal("lost raw metadata")
	}
}

// TestLiteDisplayName 验证来源名称优先，ID 格式化仅影响展示，来源和规范身份保持不变。
func TestLiteDisplayName(t *testing.T) {
	e := liteTestEntry(t, `{"deepseek/deepseek-flash":{"litellm_provider":"deepseek","mode":"chat"}}`)
	if e.Name != "Deepseek Flash" || e.ModelID != "deepseek-flash" || e.SourceKey != "deepseek/deepseek-flash" || e.CanonicalID != "deepseek/deepseek-flash" {
		t.Fatalf("display changed identity: %+v", e)
	}
	named := liteTestEntry(t, `{"openai/model-v1":{"litellm_provider":"openai","name":"来源完整名称"}}`)
	if named.Name != "来源完整名称" {
		t.Fatal(named.Name)
	}
}
