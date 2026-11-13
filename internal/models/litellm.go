package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// liteProviders 仅按来源明确的适配器身份映射 UI 厂商；国际 zai 与国内 bigmodel 分离。
var liteProviders = map[string]string{
	"anthropic": "anthropic", "openai": "openai", "gemini": "google", "google": "google", "xai": "xai", "meta": "meta", "meta_llama": "meta", "mistral": "mistral", "deepseek": "deepseek", "dashscope": "alibaba", "qwen_ai_platform": "alibaba", "moonshot": "moonshotai", "bigmodel": "zhipuai", "zai": "zai", "minimax": "minimax", "volcengine": "volcengine", "tencent": "tencent-tokenhub", "stepfun": "stepfun", "xiaomi_mimo": "xiaomi",
}

// liteProviderNames 沿用模型目录现有厂商名称；其他来源保留其适配器标识。
var liteProviderNames = map[string]string{
	"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google", "xai": "xAI", "meta": "Meta", "mistral": "Mistral", "deepseek": "DeepSeek", "alibaba": "阿里通义", "moonshotai": "月之暗面", "zhipuai": "智谱", "zai": "Z.AI", "minimax": "MiniMax", "volcengine": "火山方舟", "tencent-tokenhub": "腾讯混元", "stepfun": "阶跃星辰", "xiaomi": "小米",
}

// liteRateOrder 保持输入、输出、缓存及媒体维度的业务展示顺序。
var liteRateOrder = []string{"input", "output", "cache_read", "cache_write", "cache_write_30m", "cache_write_1h", "cache_write_12h", "cache_read_image", "cache_read_audio", "cache_write_audio", "image_input", "image_output", "audio_input", "audio_output", "video_input", "video_output", "per_image_input", "per_image", "per_second_input", "per_second", "per_request"}

// decodeLiteCatalog 将 LiteLLM 目录转为候选；整份 JSON 非法返回错误，单条非法保留 Problem 供调用方隔离。
func decodeLiteCatalog(data []byte) ([]CatalogEntry, error) {
	var source map[string]json.RawMessage
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("LiteLLM 目录格式错误: %w", err)
	}
	if len(source) == 0 {
		return nil, errors.New("LiteLLM 目录为空")
	}
	keys := make([]string, 0, len(source))
	for k := range source {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := []CatalogEntry{}
	positions := map[string]int{}
	for _, key := range keys {
		if key == "sample_spec" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(source[key], &raw); err != nil || raw == nil {
			entries = append(entries, CatalogEntry{ModelID: key, Name: key, SourceKey: key, Problem: "LiteLLM 条目格式无效"})
			continue
		}
		provider := liteString(raw, "litellm_provider")
		if provider == "" {
			continue
		} // 无适配器身份的辅助条目不能建立稳定来源。
		switch liteString(raw, "mode") {
		case "", "chat", "completion", "responses", "embedding", "rerank", "reranking", "image_generation", "image", "image_edit", "video_generation", "video":
		default:
			continue // 音频、搜索等没有对应本地类型，不能误导为可调用的对话模型。
		}
		pid, official := liteProviders[provider]
		if !official {
			pid = provider
		}
		mid := key
		if official {
			mid = strings.TrimPrefix(mid, provider+"/")
		}
		entry := CatalogEntry{ProviderID: pid, ProviderName: pid, ModelID: mid, Name: liteDisplayName(mid), Official: official, SourceKey: key, SourceURL: liteString(raw, "source"), Metadata: append(json.RawMessage(nil), source[key]...), Capabilities: []string{}, Warnings: []string{}}
		if name, ok := liteProviderNames[pid]; ok {
			entry.ProviderName = name
		}
		entry.CanonicalID = liteString(raw, "canonical_model_id")
		if entry.CanonicalID == "" && official {
			entry.CanonicalID = pid + "/" + mid
		}
		if entry.CanonicalID != "" && strings.SplitN(entry.CanonicalID, "/", 2)[0] != pid {
			entry.Official = false
		}
		if name := liteString(raw, "name"); name != "" {
			entry.Name = name
		}
		entry.Family = liteString(raw, "family")
		entry.Deprecated = liteString(raw, "status") == "deprecated" || liteBool(raw, "deprecated")
		if date, err := time.Parse(time.DateOnly, liteString(raw, "deprecation_date")); err == nil && !date.After(time.Now().UTC()) {
			entry.Deprecated = true
		}
		entry.Kind = "chat"
		switch liteString(raw, "mode") {
		case "embedding", "rerank", "reranking":
			entry.Kind = "vector"
		case "image_generation", "image", "image_edit":
			entry.Kind = "image"
		case "video_generation", "video":
			entry.Kind = "video"
		}
		for _, k := range []string{"max_input_tokens", "max_tokens"} {
			if v, ok := raw[k]; ok {
				if err := json.Unmarshal(v, &entry.Context); err != nil || entry.Context < 0 {
					entry.Problem = "来源上下文上限无效"
				}
				break
			}
		}
		for _, field := range []string{"supported_modalities", "input_modalities"} {
			var values []string
			if json.Unmarshal(raw[field], &values) == nil {
				for _, v := range values {
					if !slices.Contains(entry.Capabilities, v) {
						entry.Capabilities = append(entry.Capabilities, v)
					}
				}
			}
		}
		for _, pair := range [][2]string{{"supports_vision", "image"}, {"supports_audio_input", "audio"}, {"supports_video_input", "video"}, {"supports_reasoning", "reasoning"}, {"supports_function_calling", "tools"}} {
			if liteBool(raw, pair[0]) && !slices.Contains(entry.Capabilities, pair[1]) {
				entry.Capabilities = append(entry.Capabilities, pair[1])
			}
		}
		pricing, warnings, err := decodeLitePricing(raw, entry.Kind)
		entry.Pricing = pricing
		entry.Warnings = warnings
		if err != nil {
			entry.Problem = err.Error()
		}
		identity := pid + "\x00" + mid
		if pos, ok := positions[identity]; ok { // 同身份优先保留带适配器前缀的条目，通常拥有更完整元数据。
			if strings.HasPrefix(key, provider+"/") {
				entries[pos] = entry
			}
			continue
		}
		positions[identity] = len(entries)
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, errors.New("LiteLLM 目录没有可识别来源")
	}
	return entries, nil
}

// liteString 读取字符串元数据；完整原值另存 Metadata，不以强转猜测来源内容。
func liteString(raw map[string]json.RawMessage, key string) string {
	var v string
	_ = json.Unmarshal(raw[key], &v)
	return v
}

// liteBool 只接受来源显式提供的布尔能力标记。
func liteBool(raw map[string]json.RawMessage, key string) bool {
	var v bool
	_ = json.Unmarshal(raw[key], &v)
	return v
}

// liteRateFields 声明美元每 Token 的精确映射；所有 Token 单价统一转为美元每百万 Token。
var liteRateFields = map[string]string{
	"input_cost_per_token": "input", "output_cost_per_token": "output", "cache_read_input_token_cost": "cache_read", "input_cost_per_token_cache_hit": "cache_read", "cache_creation_input_token_cost": "cache_write", "cache_creation_input_token_cost_above_1hr": "cache_write_1h", "cache_creation_input_token_cost_above_12hr": "cache_write_12h", "cache_creation_input_token_cost_above_30min": "cache_write_30m", "cache_creation_input_token_cost_above_30m": "cache_write_30m",
	"input_cost_per_image_token": "image_input", "output_cost_per_image_token": "image_output", "input_cost_per_audio_token": "audio_input", "output_cost_per_audio_token": "audio_output", "input_cost_per_video_token": "video_input", "output_cost_per_video_token": "video_output", "cache_read_input_audio_token_cost": "cache_read_audio", "cache_read_input_image_token_cost": "cache_read_image", "cache_creation_input_audio_token_cost": "cache_write_audio",
	"input_cost_per_request": "per_request", "input_cost_per_image": "per_image_input", "output_cost_per_image": "per_image",
	"input_cost_per_video_per_second": "per_second_input", "input_cost_per_second": "per_second_input", "output_cost_per_video_per_second": "per_second", "output_cost_per_second": "per_second", "cost_per_second": "per_second",
}

// liteThreshold 识别输入上下文阈值后缀，k 使用来源约定的十进制千 Token。
var liteThreshold = regexp.MustCompile(`_above_([0-9]+)(k?)_tokens$`)

// litePrice 拒绝 null、负数、字符串及换算溢出；缺失字段由上层保留未知。
func litePrice(data json.RawMessage, scale float64) (float64, error) {
	var n float64
	if string(data) == "null" || json.Unmarshal(data, &n) != nil || !validPrice(n, false) || !validPrice(n*scale, false) {
		return 0, errors.New("来源单价必须为非负有限数字")
	}
	return n * scale, nil
}

// liteRates 将按上下文的覆盖项补全为完整档位，每档继承前一档未改变的已知维度。
func liteRates(levels map[int]map[string]Rate) ([]Rate, []Tier) {
	bounds := make([]int, 0, len(levels))
	for n := range levels {
		bounds = append(bounds, n)
	}
	sort.Ints(bounds)
	current := map[string]Rate{}
	base := []Rate{}
	tiers := []Tier{}
	for _, n := range bounds {
		for k, r := range levels[n] {
			current[k] = r
		}
		keys := make([]string, 0, len(current))
		for k := range current {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return slices.Index(liteRateOrder, keys[i]) < slices.Index(liteRateOrder, keys[j])
		})
		rates := []Rate{}
		for _, k := range keys {
			rates = append(rates, current[k])
		}
		if n == 0 {
			base = rates
		} else {
			tiers = append(tiers, Tier{Above: n, Rates: rates})
		}
	}
	return base, tiers
}

// decodeLitePricing 解析明确价格、上下文阶梯和处理模式；未支持字段以告警保留且不猜测金额。
func decodeLitePricing(raw map[string]json.RawMessage, kind string) (Pricing, []string, error) {
	p := Pricing{Base: []Rate{}, Tiers: []Tier{}, Modes: []Mode{}, Schedules: []Schedule{}, Images: []ImagePrice{}, Videos: []VideoPrice{}}
	warnings := []string{}
	levels := map[string]map[int]map[string]Rate{"": {0: {}}}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "off_peak_pricing" || key == "tiered_pricing" {
			continue
		}
		field := key
		mode := ""
		for _, name := range []string{"fast", "priority", "ultrafast"} {
			if strings.HasSuffix(field, "_"+name) {
				mode = name
				field = strings.TrimSuffix(field, "_"+name)
				break
			}
		}
		above := 0
		if m := liteThreshold.FindStringSubmatch(field); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return p, warnings, fmt.Errorf("%s 阈值无效", key)
			}
			if m[2] == "k" {
				if n > math.MaxInt/1000 {
					return p, warnings, fmt.Errorf("%s 阈值溢出", key)
				}
				n *= 1000
			}
			if n <= 0 {
				return p, warnings, fmt.Errorf("%s 阈值无效", key)
			}
			above = n
			field = strings.TrimSuffix(field, m[0])
		}
		rateKey, ok := liteRateFields[field]
		if ok {
			unit, scale := liteRateUnit(rateKey)
			v, err := litePrice(raw[key], scale)
			if err != nil {
				return p, warnings, fmt.Errorf("%s: %w", key, err)
			}
			if kind == "vector" && rateKey == "output" {
				continue
			}
			if levels[mode] == nil {
				levels[mode] = map[int]map[string]Rate{0: {}}
			}
			if levels[mode][above] == nil {
				levels[mode][above] = map[string]Rate{}
			}
			r := Rate{Key: rateKey, Price: v, Unit: unit}
			if old, exists := levels[mode][above][rateKey]; exists && old != r {
				return p, warnings, fmt.Errorf("%s 对同一维度给出冲突价格", key)
			}
			levels[mode][above][rateKey] = r
			continue
		}
		if mode == "" && above == 0 && strings.HasPrefix(field, "output_cost_per_image_") {
			v, err := litePrice(raw[key], 1)
			if err != nil {
				return p, warnings, fmt.Errorf("%s: %w", key, err)
			}
			size := strings.TrimPrefix(field, "output_cost_per_image")
			size = strings.TrimPrefix(size, "_")
			p.Images = append(p.Images, ImagePrice{Size: size, Price: v})
			continue
		}
		if mode == "" && above == 0 && strings.HasPrefix(field, "output_cost_per_second_") {
			v, err := litePrice(raw[key], 1)
			if err != nil {
				return p, warnings, fmt.Errorf("%s: %w", key, err)
			}
			resolution := ""
			if strings.HasPrefix(field, "output_cost_per_second_") {
				resolution = strings.TrimPrefix(field, "output_cost_per_second_")
			}
			p.Videos = append(p.Videos, VideoPrice{Resolution: resolution, Price: v})
			continue
		}
		if strings.Contains(key, "cost") || strings.Contains(key, "pricing") {
			warning := litePricingWarning(key)
			if !slices.Contains(warnings, warning) {
				warnings = append(warnings, warning)
			}
		}
	}
	if err := liteExplicitTiers(raw, levels[""], kind); err != nil {
		warnings = append(warnings, "来源上下文阶梯含未转换计价，未知部分未补零，完整来源已保留")
		return p, warnings, err
	}
	for _, mode := range levels {
		for _, rates := range mode {
			for key := range rates {
				if RequiresUnrecordedUsage(key) {
					warning := "部分媒体或缓存时长价格已保留；当前用量记录无法完整区分，涉及这些规则时费用显示未知"
					if !slices.Contains(warnings, warning) {
						warnings = append(warnings, warning)
					}
				}
			}
		}
	}
	p.Base, p.Tiers = liteRates(levels[""])
	for _, name := range []string{"fast", "priority", "ultrafast"} {
		if l, ok := levels[name]; ok {
			r, t := liteRates(l)
			p.Modes = append(p.Modes, Mode{Name: name, Rates: r, Tiers: t})
		}
	}
	if value, ok := raw["provider_specific_entry"]; ok {
		var specific map[string]json.RawMessage
		if json.Unmarshal(value, &specific) != nil || specific == nil {
			return p, warnings, errors.New("provider_specific_entry 格式无效")
		}
		for key, v := range specific {
			if key != "fast" {
				warning := "来源另含地域或供应商专属倍率，未转换部分保持未知"
				if !slices.Contains(warnings, warning) {
					warnings = append(warnings, warning)
				}
				continue
			}
			mult, err := litePrice(v, 1)
			if err != nil || mult <= 0 {
				return p, warnings, errors.New("fast 倍率无效")
			}
			if _, exists := levels["fast"]; exists {
				return p, warnings, errors.New("fast 倍率与显式价格同时存在")
			}
			p.Modes = append(p.Modes, Mode{Name: "fast", Multiplier: &mult})
		}
	}
	if value, ok := raw["off_peak_pricing"]; ok {
		if err := liteSchedules(value, &p); err != nil {
			return p, warnings, err
		}
	}
	sort.Strings(warnings)
	return p, warnings, validatePricing(p)
}

// liteExplicitTiers 接收按 range 声明的完整连续输入档位，拒绝缺口和不能表达的下界。
func liteExplicitTiers(raw map[string]json.RawMessage, levels map[int]map[string]Rate, kind string) error {
	value, ok := raw["tiered_pricing"]
	if !ok {
		return nil
	}
	var tiers []map[string]json.RawMessage
	if json.Unmarshal(value, &tiers) != nil || len(tiers) == 0 {
		return errors.New("tiered_pricing 格式无效")
	}
	last := 0
	for _, tier := range tiers {
		var bounds []float64
		if json.Unmarshal(tier["range"], &bounds) != nil || len(bounds) != 2 || bounds[0] != float64(last) || !validPrice(bounds[1], false) || bounds[1] <= bounds[0] || bounds[1] >= float64(math.MaxInt) || math.Trunc(bounds[1]) != bounds[1] {
			return errors.New("tiered_pricing 区间必须从零起连续且为整数")
		}
		at := last
		last = int(bounds[1])
		if levels[at] == nil {
			levels[at] = map[string]Rate{}
		}
		for key, v := range tier {
			if key == "range" {
				continue
			}
			mapped, ok := liteRateFields[key]
			if !ok {
				return fmt.Errorf("tiered_pricing 不支持字段 %s", key)
			}
			if kind == "vector" && mapped == "output" {
				continue
			}
			unit, scale := liteRateUnit(mapped)
			price, err := litePrice(v, scale)
			if err != nil {
				return fmt.Errorf("tiered_pricing %s: %w", key, err)
			}
			levels[at][mapped] = Rate{Key: mapped, Price: price, Unit: unit}
		}
	}
	return nil
}

// litePricingWarning 按业务维度聚合未支持计价，原始字段完整保留在 Metadata 中。
func litePricingWarning(key string) string {
	category := "附加服务"
	switch {
	case strings.Contains(key, "batch") || strings.Contains(key, "flex") || strings.Contains(key, "balanced") || strings.Contains(key, "decisions"):
		category = "其他处理模式"
	case strings.Contains(key, "image") || strings.Contains(key, "pixel") || strings.Contains(key, "video"):
		category = "复杂图片或视频"
	case strings.Contains(key, "audio") || strings.Contains(key, "character"):
		category = "音频或字符"
	case strings.Contains(key, "reasoning"):
		category = "独立推理"
	}
	return "来源另含" + category + "计价，未转换部分保持未知"
}

// liteRateUnit 由规范计费维度确定单位及来源换算倍率。
func liteRateUnit(key string) (string, float64) {
	switch key {
	case "per_image", "per_image_input":
		return "image", 1
	case "per_second", "per_second_input":
		return "second", 1
	case "per_request":
		return "request", 1
	default:
		return "million_tokens", 1e6
	}
}

// liteDisplayName 仅对来源 ID 的分隔符和词首作展示格式化，不推断型号、版本或规范身份。
func liteDisplayName(id string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(id))
	for i, word := range words {
		letters := []rune(word)
		if len(letters) > 0 {
			letters[0] = unicode.ToUpper(letters[0])
			words[i] = string(letters)
		}
	}
	return strings.Join(words, " ")
}
