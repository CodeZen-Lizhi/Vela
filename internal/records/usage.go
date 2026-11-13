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
	u.mediaCached = u.CacheRead > 0 && u.ImageInput+u.AudioInput > 0
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
	a.mediaCached = a.mediaCached || b.mediaCached
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
	a.mediaCached = a.mediaCached || b.mediaCached
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

// estimate 使用请求开始时固定价格、真实完整上下文与倍率计算估算美元；缺少必需项则未知。
func estimate(p models.Pricing, u *Usage, multiplier *float64, tier string, started time.Time) (*float64, []Charge) {
	charges := []Charge{}
	if u == nil || multiplier == nil || u.mediaCached || u.ImageInput+u.AudioInput > u.Input || u.ImageOutput+u.AudioOutput > u.Output {
		return nil, charges
	}
	rates := p.Base
	tiers := p.Tiers
	factor := *multiplier
	if tier != "" && tier != "standard" && tier != "default" && tier != "auto" {
		found := false
		for _, mode := range p.Modes {
			if mode.Name == tier {
				found = true
				if mode.Multiplier != nil {
					factor *= *mode.Multiplier
				} else {
					rates = mode.Rates
					tiers = mode.Tiers
				}
				break
			}
		}
		if !found {
			return nil, charges
		}
	}
	for _, t := range tiers {
		if u.Context > int64(t.Above) {
			rates = t.Rates
		}
	}
	// 用量尚不能区分这些价格维度，不能把普通输入价或仅输出费用当作完整金额。
	for _, rate := range rates {
		if models.RequiresUnrecordedUsage(rate.Key) {
			return nil, charges
		}
	}
	for _, window := range p.Schedules {
		loc, err := time.LoadLocation(window.Timezone)
		if err != nil {
			return nil, charges
		}
		local := started.In(loc)
		minute := local.Hour()*60 + local.Minute()
		begin, err := time.Parse("15:04", window.Start)
		if err != nil {
			return nil, charges
		}
		end, err := time.Parse("15:04", window.End)
		endMinute := 1440
		if window.End != "24:00" {
			if err != nil {
				return nil, charges
			}
			endMinute = end.Hour()*60 + end.Minute()
		}
		if slices.Contains(window.Days, int(local.Weekday())) && !slices.Contains(window.ExcludedDates, local.Format("2006-01-02")) && minute >= begin.Hour()*60+begin.Minute() && minute < endMinute {
			factor *= window.Multiplier
			break
		}
	}
	counts := map[string]float64{"input": float64(u.Input - u.ImageInput - u.AudioInput), "output": float64(u.Output - u.ImageOutput - u.AudioOutput), "image_input": float64(u.ImageInput), "image_output": float64(u.ImageOutput), "audio_input": float64(u.AudioInput), "audio_output": float64(u.AudioOutput), "cache_read": float64(u.CacheRead), "cache_write": float64(u.CacheWrite), "cache_write_1h": float64(u.CacheWrite1h)}
	if u.Unit != "" {
		counts = map[string]float64{"per_" + u.Unit: u.Quantity}
	}
	// 按请求计价与 Token 计价是替代口径，不重复收取两份费用。
	for _, rate := range rates {
		if rate.Key == "per_request" {
			counts = map[string]float64{"per_request": 1}
			break
		}
	}
	total := new(big.Rat)
	for _, key := range []string{"input", "output", "image_input", "image_output", "audio_input", "audio_output", "cache_read", "cache_write", "cache_write_1h", "per_image", "per_second", "per_request"} {
		count := counts[key]
		if count == 0 {
			continue
		}
		var price *float64
		for _, rate := range rates {
			if rate.Key == key {
				value := rate.Price
				price = &value
				break
			}
		}
		if key == "per_image" {
			for _, image := range p.Images {
				if imageMatches(image, u) {
					value := image.Price
					price = &value
					break
				}
			}
		}
		if key == "per_second" {
			for _, video := range p.Videos {
				if video.Resolution == u.Specification {
					value := video.Price
					price = &value
					break
				}
			}
		}
		if price == nil {
			return nil, charges
		}
		amount := new(big.Rat).Mul(decimal(count), decimal(*price))
		amount.Mul(amount, decimal(factor))
		if strings.HasPrefix(key, "per_") == false {
			amount.Quo(amount, big.NewRat(1_000_000, 1))
		}
		total.Add(total, amount)
		value, _ := amount.Float64()
		charges = append(charges, Charge{Key: key, Quantity: count, Price: *price, Amount: value})
	}
	// 明确提供的零用量可以为零金额；不能由缺失 usage 推导零价。
	value, _ := total.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, charges
	}
	return &value, charges
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
