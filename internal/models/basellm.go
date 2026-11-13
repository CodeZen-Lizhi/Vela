package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// inheritRates 将档位明确给出的价格覆盖到基准，未声明的维度继续继承。
func inheritRates(base, overrides []Rate) []Rate {
	p := Pricing{Base: append([]Rate{}, base...)}
	mergeSourceGroup(&p, Pricing{Base: overrides}, "base")
	return p.Base
}

// decodeBasellmPricing 使用 basellm 的结构化规则；汇率与长期缓存倍率均取自同源发布物。
func decodeBasellmPricing(raw json.RawMessage, modes map[string]struct {
	Cost json.RawMessage `json:"cost"`
}, kind string, exchangeRates map[string]float64, cache1h, cache12h float64) (Pricing, error) {
	normalized, err := normalizeBasellmCost(raw, exchangeRates, cache1h, cache12h)
	if err != nil {
		return Pricing{}, err
	}
	// 处理模式继承主价币种，模式自身声明币种时以自身为准。
	var baseFields map[string]json.RawMessage
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &baseFields); err != nil {
			return Pricing{}, err
		}
	}
	normalizedModes := make(map[string]struct {
		Cost json.RawMessage `json:"cost"`
	}, len(modes))
	for name, mode := range modes {
		modeRaw := mode.Cost
		if len(modeRaw) > 0 && string(modeRaw) != "null" && baseFields["currency"] != nil {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(modeRaw, &fields); err != nil {
				return Pricing{}, err
			}
			if fields["currency"] == nil {
				fields["currency"] = baseFields["currency"]
			}
			modeRaw, err = json.Marshal(fields)
			if err != nil {
				return Pricing{}, err
			}
		}
		cost, err := normalizeBasellmCost(modeRaw, exchangeRates, cache1h, cache12h)
		if err != nil {
			return Pricing{}, err
		}
		normalizedModes[name] = struct {
			Cost json.RawMessage `json:"cost"`
		}{Cost: cost}
	}
	p, err := decodePricing(normalized, normalizedModes, kind)
	if err != nil {
		return p, err
	}
	var fields map[string]json.RawMessage
	if len(normalized) == 0 || string(normalized) == "null" {
		return p, nil
	}
	if err = json.Unmarshal(normalized, &fields); err != nil {
		return p, err
	}
	if fields["context_over_200k"] != nil && fields["tiers"] == nil {
		return p, errors.New("来源上下文价格没有明确阈值，保留本地配置")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.HasPrefix(key, "per_second_") {
			continue
		}
		var price float64
		if json.Unmarshal(fields[key], &price) != nil || !validPrice(price, false) {
			return p, fmt.Errorf("来源 %s 单价无效", key)
		}
		p.Videos = append(p.Videos, VideoPrice{Resolution: strings.TrimPrefix(key, "per_second_"), Price: price})
	}
	if schedule, ok := fields["schedule"]; ok {
		if err = decodeBasellmSchedules(schedule, &p); err != nil {
			return p, err
		}
	}
	return p, validatePricing(p)
}

// normalizeBasellmCost 将本币价格换算为美元，并展开来源明确声明的长期缓存价格；不导入其他币种备选价目表。
func normalizeBasellmCost(raw json.RawMessage, exchangeRates map[string]float64, cache1h, cache12h float64) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("来源价格格式无效")
	}
	originalCacheWrite := fields["cache_write"]
	var currency string
	if value, ok := fields["currency"]; ok && json.Unmarshal(value, &currency) != nil {
		return nil, errors.New("来源币种格式无效")
	}
	divisor := 1.0
	if currency != "" && currency != "USD" {
		divisor = exchangeRates[currency]
		if !validPrice(divisor, true) {
			return nil, fmt.Errorf("来源 %s 价格缺少有效美元汇率，保留本地配置", currency)
		}
	}
	// 仅价格数值参与换算，阈值、时段、星期和备选币种不变。
	for key, value := range fields {
		if !contains([]string{"input", "output", "reasoning", "cache_read", "cache_write", "cache_write_1h", "cache_write_12h", "input_audio", "output_audio", "input_image", "output_image", "per_image", "per_second", "per_request"}, key) && !strings.HasPrefix(key, "per_second_") {
			continue
		}
		var price float64
		if string(value) == "null" {
			continue
		}
		if json.Unmarshal(value, &price) != nil || !validPrice(price, false) {
			return nil, fmt.Errorf("来源 %s 单价无效", key)
		}
		encoded, err := json.Marshal(price / divisor)
		if err != nil {
			return nil, err
		}
		fields[key] = encoded
	}
	// 阶梯及窗口没有独立币种时继承本层币种；每个子规则仍独立校验。
	for _, key := range []string{"tiers", "context_over_200k"} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		var children []json.RawMessage
		if key == "tiers" {
			if json.Unmarshal(value, &children) != nil {
				return nil, errors.New("来源阶梯格式无效")
			}
		} else {
			children = []json.RawMessage{value}
		}
		for i, child := range children {
			var f map[string]json.RawMessage
			if json.Unmarshal(child, &f) != nil || f == nil {
				return nil, errors.New("来源阶梯格式无效")
			}
			if _, ok := f["currency"]; !ok && currency != "" {
				f["currency"], _ = json.Marshal(currency)
			}
			// 长期缓存倍率以各档输入价为基准，缓存写入维度由主价声明。
			if _, ok := fields["cache_write"]; ok && f["cache_write"] == nil {
				f["cache_write"] = originalCacheWrite
			}
			encoded, err := json.Marshal(f)
			if err != nil {
				return nil, err
			}
			children[i], err = normalizeBasellmCost(encoded, exchangeRates, cache1h, cache12h)
			if err != nil {
				return nil, err
			}
		}
		var err error
		if key == "tiers" {
			fields[key], err = json.Marshal(children)
		} else {
			fields[key] = children[0]
		}
		if err != nil {
			return nil, err
		}
	}
	// 缓存持久时间倍率是供应商声明；没有缓存写入价时不虚构该能力。
	if fields["cache_write"] != nil && string(fields["cache_write"]) != "null" && fields["input"] != nil && string(fields["input"]) != "null" {
		var input float64
		if err := json.Unmarshal(fields["input"], &input); err != nil {
			return nil, err
		}
		for key, multiplier := range map[string]float64{"cache_write_1h": cache1h, "cache_write_12h": cache12h} {
			if multiplier == 0 || fields[key] != nil {
				continue
			}
			if !validPrice(multiplier, true) {
				return nil, errors.New("来源长期缓存倍率无效")
			}
			encoded, err := json.Marshal(input * multiplier)
			if err != nil {
				return nil, err
			}
			fields[key] = encoded
		}
	}
	if value, ok := fields["schedule"]; ok {
		var schedule map[string]json.RawMessage
		if json.Unmarshal(value, &schedule) != nil || schedule == nil {
			return nil, errors.New("来源分时规则格式无效")
		}
		var windows []json.RawMessage
		if json.Unmarshal(schedule["windows"], &windows) != nil {
			return nil, errors.New("来源分时窗口格式无效")
		}
		for i, window := range windows {
			var f map[string]json.RawMessage
			if json.Unmarshal(window, &f) != nil || f == nil {
				return nil, errors.New("来源分时窗口格式无效")
			}
			if f["currency"] == nil && currency != "" {
				f["currency"], _ = json.Marshal(currency)
			}
			encoded, err := json.Marshal(f)
			if err != nil {
				return nil, err
			}
			windows[i], err = normalizeBasellmCost(encoded, exchangeRates, cache1h, cache12h)
			if err != nil {
				return nil, err
			}
		}
		encoded, err := json.Marshal(windows)
		if err != nil {
			return nil, err
		}
		schedule["windows"] = encoded
		fields["schedule"], err = json.Marshal(schedule)
		if err != nil {
			return nil, err
		}
	}
	delete(fields, "currency")
	return json.Marshal(fields)
}

// decodeBasellmSchedules 只在所有价格维度可用同一倍率准确表示时导入分时窗口，避免部分价格被错误缩放。
func decodeBasellmSchedules(raw json.RawMessage, p *Pricing) error {
	var source struct {
		// Timezone 和 Fallback 指明窗口时区与基础价的时段口径。
		Timezone string `json:"timezone"`
		Fallback string `json:"fallback"`
		// Windows 保存 ISO 星期、时段以及覆盖价格。
		Windows []json.RawMessage `json:"windows"`
	}
	if json.Unmarshal(raw, &source) != nil || len(source.Windows) == 0 || source.Timezone == "" || source.Fallback == "" {
		return errors.New("来源分时规则缺少时区、基准或窗口")
	}
	if len(p.Tiers) > 0 || len(p.Modes) > 0 || len(p.Images) > 0 || len(p.Videos) > 0 {
		return errors.New("来源分时与阶梯、模式或媒体组合无法用共同倍率表达")
	}
	for _, window := range source.Windows {
		var w struct {
			// Weekdays 使用 ISO 1～7，Hours 为同一天内的左闭右开时段。
			Weekdays []int    `json:"weekdays"`
			Hours    []string `json:"hours"`
		}
		if json.Unmarshal(window, &w) != nil || len(w.Hours) == 0 {
			return errors.New("来源分时窗口缺少时段")
		}
		rates, err := decodeRates(window, "chat")
		if err != nil {
			return err
		}
		merged := inheritRates(p.Base, rates)
		ratio := -1.0
		for _, base := range p.Base {
			var price float64
			for _, rate := range merged {
				if rate.Key == base.Key {
					price = rate.Price
					break
				}
			}
			if base.Price == 0 {
				if price != 0 {
					return errors.New("来源分时不能由零基准换算倍率")
				}
				continue
			}
			next := price / base.Price
			if !validPrice(next, true) || ratio >= 0 && math.Abs(next-ratio) > 1e-9*math.Max(next, ratio) {
				return errors.New("来源分时不同价格维度的倍率不一致，保留本地配置")
			}
			ratio = next
		}
		if ratio < 0 || len(merged) != len(p.Base) {
			return errors.New("来源分时缺少可计算共同倍率的基准")
		}
		days := []int{}
		if w.Weekdays == nil {
			w.Weekdays = []int{1, 2, 3, 4, 5, 6, 7}
		}
		for _, day := range w.Weekdays {
			if day < 1 || day > 7 {
				return errors.New("来源分时星期须为 ISO 1～7")
			}
			days = append(days, day%7)
		}
		for _, span := range w.Hours {
			parts := strings.Split(span, "-")
			if len(parts) != 2 {
				return errors.New("来源分时时段格式无效")
			}
			p.Schedules = append(p.Schedules, Schedule{Timezone: source.Timezone, Days: append([]int{}, days...), Start: parts[0], End: parts[1], Multiplier: ratio, ExcludedDates: []string{}})
		}
	}
	p.Baseline = source.Fallback
	switch source.Fallback {
	case "off_peak":
		p.Baseline = "低谷价格"
	case "peak":
		p.Baseline = "高峰价格"
	case "standard":
		p.Baseline = "标准价格"
	}
	return validatePricing(*p)
}
