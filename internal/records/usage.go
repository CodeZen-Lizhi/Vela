package records

import (
	"encoding/json"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

// object 将已解码协议字段读取为对象，缺失时返回空引用而非伪造数据。
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }

// number 读取上游非负计数，非法值视为没有此项。
func number(m map[string]any, key string) int64 {
	v, _ := m[key].(float64)
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > float64(1<<53) {
		return 0
	}
	return int64(v)
}

// parseUsage 读取四协议累计用量与媒体计量；未提供 usage 时返回 nil。
func parseUsage(data []byte, protocol string) *Usage {
	protocol = strings.TrimPrefix(protocol, "openai:")
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return nil
	}
	if response := object(root["response"]); response != nil {
		root = response
	}
	if message := object(root["message"]); message != nil {
		root = message
	}
	if protocol == "gemini:interactions" {
		if interaction := object(root["interaction"]); object(interaction["usage"]) != nil {
			root = interaction
		}
	}
	usage := object(root["usage"])
	if usage == nil {
		usage = object(root["usageMetadata"])
	}
	if usage == nil {
		if protocol == "image" {
			if images, ok := root["data"].([]any); ok && len(images) > 0 {
				return &Usage{Quantity: float64(len(images)), Unit: "image"}
			}
		}
		if protocol == "video" && root["status"] == "completed" {
			duration, _ := root["seconds"].(float64)
			if text, ok := root["seconds"].(string); ok {
				duration, _ = strconv.ParseFloat(text, 64)
			}
			if duration > 0 && !math.IsInf(duration, 0) && !math.IsNaN(duration) {
				spec, _ := root["size"].(string)
				return &Usage{Quantity: duration, Unit: "second", Specification: spec}
			}
		}
		return nil
	}
	// Realtime 的明细字段使用单数 token，与 Responses 的 tokens 命名不同。
	if protocol == "realtime" {
		if object(usage["input_tokens_details"]) == nil {
			usage["input_tokens_details"] = usage["input_token_details"]
		}
		if object(usage["output_tokens_details"]) == nil {
			usage["output_tokens_details"] = usage["output_token_details"]
		}
	}
	u := &Usage{}
	if len(usage) == 0 {
		return nil
	}
	if protocol == "gemini:interactions" {
		_, u.inputKnown = usage["total_input_tokens"]
		_, u.outputKnown = usage["total_output_tokens"]
		u.Context = number(usage, "total_input_tokens")
		u.CacheRead = number(usage, "total_cached_tokens")
		u.Input = max(0, u.Context-u.CacheRead)
		u.Output = number(usage, "total_output_tokens") + number(usage, "total_thought_tokens")
	} else if _, ok := usage["promptTokenCount"]; ok {
		u.inputKnown = true
		_, u.outputKnown = usage["candidatesTokenCount"]
		raw := number(usage, "promptTokenCount")
		u.CacheRead = number(usage, "cachedContentTokenCount")
		u.Input = max(0, raw-u.CacheRead)
		u.Context = raw
		u.Output = number(usage, "candidatesTokenCount") + number(usage, "thoughtsTokenCount")
	} else if _, ok := usage["prompt_tokens"]; ok {
		u.inputKnown = true
		_, u.outputKnown = usage["completion_tokens"]
		if protocol == "embedding" || protocol == "rerank" {
			u.outputKnown = true
		}
		raw := number(usage, "prompt_tokens")
		u.CacheRead = number(object(usage["prompt_tokens_details"]), "cached_tokens")
		u.CacheWrite = openAICacheWrite(usage, number(usage, "cache_creation_input_tokens"))
		u.Input = max(0, raw-u.CacheRead-u.CacheWrite)
		u.Context = raw
		u.Output = number(usage, "completion_tokens")
	} else {
		_, u.inputKnown = usage["input_tokens"]
		_, u.outputKnown = usage["output_tokens"]
		raw := number(usage, "input_tokens")
		u.Output = number(usage, "output_tokens")
		u.CacheRead = number(usage, "cache_read_input_tokens")
		u.CacheWrite = number(usage, "cache_creation_input_tokens")
		cache := object(usage["cache_creation"])
		u.CacheWrite1h = number(cache, "ephemeral_1h_input_tokens")
		if u.CacheWrite == 0 {
			u.CacheWrite = number(cache, "ephemeral_5m_input_tokens") + u.CacheWrite1h
		}
		u.CacheWrite = max(0, u.CacheWrite-u.CacheWrite1h)
		if details := object(usage["input_tokens_details"]); details != nil {
			u.CacheRead = number(details, "cached_tokens")
			u.CacheWrite = max(0, openAICacheWrite(usage, u.CacheWrite+u.CacheWrite1h)-u.CacheWrite1h)
			u.Input = max(0, raw-u.CacheRead-u.CacheWrite-u.CacheWrite1h)
			u.Context = raw
		} else {
			u.Input = raw
			u.Context = raw + u.CacheRead + u.CacheWrite + u.CacheWrite1h
		}
	}
	inputDetails := object(usage["prompt_tokens_details"])
	if inputDetails == nil {
		inputDetails = object(usage["input_tokens_details"])
	}
	outputDetails := object(usage["completion_tokens_details"])
	if outputDetails == nil {
		outputDetails = object(usage["output_tokens_details"])
	}
	u.ImageInput = number(inputDetails, "image_tokens")
	u.AudioInput = number(inputDetails, "audio_tokens")
	u.ImageOutput = number(outputDetails, "image_tokens")
	u.AudioOutput = number(outputDetails, "audio_tokens")
	for _, detail := range []struct {
		key          string
		image, audio *int64
	}{{"promptTokensDetails", &u.ImageInput, &u.AudioInput}, {"candidatesTokensDetails", &u.ImageOutput, &u.AudioOutput}} {
		items, _ := usage[detail.key].([]any)
		for _, item := range items {
			m := object(item)
			switch m["modality"] {
			case "IMAGE":
				*detail.image += number(m, "tokenCount")
			case "AUDIO":
				*detail.audio += number(m, "tokenCount")
			}
		}
	}
	return u
}

// openAICacheWrite 沿 Aether 映射缓存写入：cache_write_tokens（包括显式零）优先于 cached_creation_tokens。
func openAICacheWrite(usage map[string]any, value int64) int64 {
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details := object(usage[key])
		if _, ok := details["cached_creation_tokens"]; ok {
			value = number(details, "cached_creation_tokens")
		}
	}
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		details := object(usage[key])
		if _, ok := details["cache_write_tokens"]; ok {
			return number(details, "cache_write_tokens")
		}
	}
	return value
}

// mergeUsage 按累计快照合并流事件，避免重复累加同一 usage。
func mergeUsage(a, b *Usage) *Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	a.inputKnown = a.inputKnown || b.inputKnown
	a.outputKnown = a.outputKnown || b.outputKnown
	a.Input = max(a.Input, b.Input)
	a.Output = max(a.Output, b.Output)
	a.CacheRead = max(a.CacheRead, b.CacheRead)
	a.CacheWrite = max(a.CacheWrite, b.CacheWrite)
	a.CacheWrite1h = max(a.CacheWrite1h, b.CacheWrite1h)
	a.Context = max(a.Context, b.Context)
	a.ImageInput = max(a.ImageInput, b.ImageInput)
	a.ImageOutput = max(a.ImageOutput, b.ImageOutput)
	a.AudioInput = max(a.AudioInput, b.AudioInput)
	a.AudioOutput = max(a.AudioOutput, b.AudioOutput)
	return a
}

// addUsage 汇总实际不同上游尝试的消耗，不能把失败尝试的真实用量丢弃。
func addUsage(a, b *Usage) {
	a.Input += b.Input
	a.Output += b.Output
	a.CacheRead += b.CacheRead
	a.CacheWrite += b.CacheWrite
	a.CacheWrite1h += b.CacheWrite1h
	a.Context += b.Context
	a.ImageInput += b.ImageInput
	a.ImageOutput += b.ImageOutput
	a.AudioInput += b.AudioInput
	a.AudioOutput += b.AudioOutput
	a.Quantity += b.Quantity
	if b.Unit != "" {
		a.Unit = b.Unit
		a.Specification = b.Specification
	}
}

// decimal 通过十进制有理数执行费用乘法，避免逐分项二进制误差放大。
func decimal(value float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	return r
}

// pricingPlan 是请求实际适用的价格规则、阶梯和倍率组合。
type pricingPlan struct {
	// rates 是当前模式及上下文阶梯选中的价格项。
	rates []models.Rate
	// tiers 是当前处理模式的上下文阶梯，空表示直接使用基础价格。
	tiers []models.Tier
	// factor 合并 Key、处理模式及分时倍率，零表示免费。
	factor float64
}

// selectPricingPlan 选择处理模式、上下文阶梯和分时倍率；未知模式或无效时段不能猜测默认价。
func selectPricingPlan(p models.Pricing, multiplier *float64, tier string, contextTokens int64, started time.Time) (pricingPlan, bool) {
	if multiplier == nil {
		return pricingPlan{}, false
	}
	plan := pricingPlan{rates: p.Base, tiers: p.Tiers, factor: *multiplier}
	if tier != "" && tier != "standard" && tier != "default" && tier != "auto" {
		found := false
		for _, mode := range p.Modes {
			if mode.Name != tier {
				continue
			}
			found = true
			if mode.Multiplier != nil {
				plan.factor *= *mode.Multiplier
			} else {
				plan.rates = mode.Rates
				plan.tiers = mode.Tiers
			}
			break
		}
		if !found {
			return pricingPlan{}, false
		}
	}
	for _, t := range plan.tiers {
		if contextTokens > int64(t.Above) {
			plan.rates = t.Rates
		}
	}
	for _, window := range p.Schedules {
		loc, err := time.LoadLocation(window.Timezone)
		if err != nil {
			return pricingPlan{}, false
		}
		begin, err := time.Parse("15:04", window.Start)
		if err != nil {
			return pricingPlan{}, false
		}
		endMinute := 1440
		if window.End != "24:00" {
			end, parseErr := time.Parse("15:04", window.End)
			if parseErr != nil {
				return pricingPlan{}, false
			}
			endMinute = end.Hour()*60 + end.Minute()
		}
		local := started.In(loc)
		minute := local.Hour()*60 + local.Minute()
		if slices.Contains(window.Days, int(local.Weekday())) &&
			!slices.Contains(window.ExcludedDates, local.Format("2006-01-02")) &&
			minute >= begin.Hour()*60+begin.Minute() && minute < endMinute {
			plan.factor *= window.Multiplier
			break
		}
	}
	return plan, true
}

// usageCounts 将归一化用量映射为价格维度数量，媒体 Token 已从普通输入输出中拆出。
func usageCounts(u *Usage) map[string]float64 {
	counts := map[string]float64{
		"input":          float64(u.Input - u.ImageInput - u.AudioInput),
		"output":         float64(u.Output - u.ImageOutput - u.AudioOutput),
		"image_input":    float64(u.ImageInput),
		"image_output":   float64(u.ImageOutput),
		"audio_input":    float64(u.AudioInput),
		"audio_output":   float64(u.AudioOutput),
		"cache_read":     float64(u.CacheRead),
		"cache_write":    float64(u.CacheWrite),
		"cache_write_1h": float64(u.CacheWrite1h),
	}
	if u.Unit != "" {
		counts = map[string]float64{"per_" + u.Unit: u.Quantity}
	}
	return counts
}

// unrecordedUsagePresent 判断不可区分的计价维度本次是否确实产生用量；未发生不能阻断整笔费用。
func unrecordedUsagePresent(key string, u *Usage) bool {
	switch key {
	case "cache_write_12h", "cache_write_30m":
		return u.CacheWrite > 0
	case "cache_write_1h":
		return u.CacheWrite1h > 0
	case "cache_read_audio":
		return u.CacheRead > 0 && u.AudioInput > 0
	case "cache_read_image":
		return u.CacheRead > 0 && u.ImageInput > 0
	case "cache_write_audio":
		return u.CacheWrite > 0 && u.AudioInput > 0
	case "video_input":
		return u.Input > 0
	case "video_output":
		return u.Output > 0
	case "per_image_input":
		return u.ImageInput > 0 || u.Unit == "image" && u.Quantity > 0
	case "per_second_input":
		return u.Input > 0 || u.Unit == "second" && u.Quantity > 0
	default:
		// 新价格维度未接入用量判定时保持未知，避免静默少算。
		return true
	}
}

// hasUnrecordedUsage 只在实际发生不可区分用量时要求整笔费用保持未知。
func hasUnrecordedUsage(rates []models.Rate, u *Usage) bool {
	for _, rate := range rates {
		if models.RequiresUnrecordedUsage(rate.Key) && unrecordedUsagePresent(rate.Key, u) {
			return true
		}
	}
	return false
}

// hasMixedMediaCache 根据持久化用量判断缓存读取是否混有无法拆分的媒体 Token。
func hasMixedMediaCache(u *Usage) bool {
	return u != nil && u.CacheRead > 0 && u.ImageInput+u.AudioInput > 0
}

// ratePrice 返回某维度的显式价格；媒体档位按真实规格匹配，不能套用其他尺寸。
func ratePrice(key string, rates []models.Rate, p models.Pricing, u *Usage) (float64, bool) {
	for _, rate := range rates {
		if rate.Key == key {
			return rate.Price, true
		}
	}
	if key == "per_image" {
		for _, image := range p.Images {
			if imageMatches(image, u) {
				return image.Price, true
			}
		}
	}
	if key == "per_second" {
		for _, video := range p.Videos {
			if video.Resolution == u.Specification {
				return video.Price, true
			}
		}
	}
	return 0, false
}

// chargeUsage 按固定维度顺序汇总分项；任一非零用量缺价时整笔保持未知。
func chargeUsage(p models.Pricing, u *Usage, plan pricingPlan) (*float64, []Charge) {
	counts := usageCounts(u)
	// 按请求计价与 Token 计价是替代口径，不重复收取两份费用。
	for _, rate := range plan.rates {
		if rate.Key == "per_request" {
			counts = map[string]float64{"per_request": 1}
			break
		}
	}
	total := new(big.Rat)
	charges := []Charge{}
	for _, key := range []string{"input", "output", "image_input", "image_output", "audio_input", "audio_output", "cache_read", "cache_write", "cache_write_1h", "per_image", "per_second", "per_request"} {
		count := counts[key]
		if count == 0 {
			continue
		}
		price, ok := ratePrice(key, plan.rates, p, u)
		if !ok {
			return nil, charges
		}
		amount := new(big.Rat).Mul(decimal(count), decimal(price))
		amount.Mul(amount, decimal(plan.factor))
		if !strings.HasPrefix(key, "per_") {
			amount.Quo(amount, big.NewRat(1_000_000, 1))
		}
		total.Add(total, amount)
		value, _ := amount.Float64()
		charges = append(charges, Charge{Key: key, Quantity: count, Price: price, Amount: value})
	}
	value, _ := total.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, charges
	}
	return &value, charges
}

// estimate 使用请求开始时固定价格、真实完整上下文与倍率计算估算美元；缺少必需项则未知。
func estimate(p models.Pricing, u *Usage, multiplier *float64, tier string, started time.Time) (*float64, []Charge) {
	if u == nil || multiplier == nil || hasMixedMediaCache(u) || u.ImageInput+u.AudioInput > u.Input || u.ImageOutput+u.AudioOutput > u.Output {
		return nil, []Charge{}
	}
	plan, ok := selectPricingPlan(p, multiplier, tier, u.Context, started)
	if !ok || hasUnrecordedUsage(plan.rates, u) {
		return nil, []Charge{}
	}
	return chargeUsage(p, u, plan)
}

// imageMatches 优先匹配明确尺寸或像素区间，质量不符时不能套用该档。
func imageMatches(price models.ImagePrice, u *Usage) bool {
	if price.Quality != "" && price.Quality != u.Quality {
		return false
	}
	if price.Size != "" {
		return price.Size == u.Specification
	}
	width, height, ok := strings.Cut(strings.ToLower(u.Specification), "x")
	if !ok {
		return false
	}
	w, e1 := strconv.ParseInt(width, 10, 32)
	h, e2 := strconv.ParseInt(height, 10, 32)
	if e1 != nil || e2 != nil || w <= 0 || h <= 0 {
		return false
	}
	pixels := w * h
	return pixels >= int64(price.MinPixels) && (price.MaxPixels == 0 || pixels <= int64(price.MaxPixels))
}
