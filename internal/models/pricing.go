package models

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

// groups 定义独立修改保护的规则组，关联阶梯永远作为整体。
var groups = []string{"base", "tiers", "modes", "schedules", "images", "videos"}

// RequiresUnrecordedUsage 标记当前用量记录尚不能区分的计价维度；调用方须结合实际用量，不能仅因价格存在就判整笔未知。
func RequiresUnrecordedUsage(key string) bool {
	switch key {
	case "cache_write_12h", "cache_write_30m", "cache_read_audio", "cache_read_image", "cache_write_audio", "video_input", "video_output", "per_image_input", "per_second_input":
		return true
	default:
		return false
	}
}

// removeVectorOutput 清除误分类为对话模型时导入的输出价，其余价格及完整规则组保持不变。
func removeVectorOutput(p *Pricing) {
	remove := func(rates []Rate) []Rate {
		return slices.DeleteFunc(rates, func(rate Rate) bool { return rate.Key == "output" })
	}
	p.Base = remove(p.Base)
	for i := range p.Tiers {
		p.Tiers[i].Rates = remove(p.Tiers[i].Rates)
	}
	for i := range p.Modes {
		p.Modes[i].Rates = remove(p.Modes[i].Rates)
		for j := range p.Modes[i].Tiers {
			p.Modes[i].Tiers[j].Rates = remove(p.Modes[i].Tiers[j].Rates)
		}
	}
}

// newID 为模型创建稳定身份，同名删除重建时不会重用历史身份。
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// contains 判断字符串集合，用于白名单和修改保护。
func contains(values []string, value string) bool { return slices.Contains(values, value) }

// sameTiers 比较完整阶梯，空切片与 nil 均表示没有来源规则。
func sameTiers(a, b []Tier) bool {
	return slices.EqualFunc(a, b, func(x, y Tier) bool { return x.Above == y.Above && slices.Equal(x.Rates, y.Rates) })
}

// sameGroup 按规则语义比较，避免 IPC 将 nil 集合编码为空数组后误判为本地修改。
func sameGroup(a, b Pricing, g string) bool {
	switch g {
	case "base":
		return slices.Equal(a.Base, b.Base)
	case "tiers":
		return sameTiers(a.Tiers, b.Tiers)
	case "modes":
		return slices.EqualFunc(a.Modes, b.Modes, func(x, y Mode) bool {
			multiplierEqual := x.Multiplier == nil && y.Multiplier == nil || x.Multiplier != nil && y.Multiplier != nil && *x.Multiplier == *y.Multiplier
			return x.Name == y.Name && multiplierEqual && slices.Equal(x.Rates, y.Rates) && sameTiers(x.Tiers, y.Tiers)
		})
	case "schedules":
		return a.Baseline == b.Baseline && slices.EqualFunc(a.Schedules, b.Schedules, func(x, y Schedule) bool {
			return x.Timezone == y.Timezone && x.Start == y.Start && x.End == y.End && x.Multiplier == y.Multiplier && slices.Equal(x.Days, y.Days) && slices.Equal(x.ExcludedDates, y.ExcludedDates)
		})
	case "images":
		return slices.Equal(a.Images, b.Images)
	case "videos":
		return slices.Equal(a.Videos, b.Videos)
	}
	return false
}

// hasGroup 表达来源是否提供可恢复的完整组，不将空组解释成免费。
func hasGroup(p Pricing, g string) bool {
	switch g {
	case "base":
		return len(p.Base) > 0
	case "tiers":
		return len(p.Tiers) > 0
	case "modes":
		return len(p.Modes) > 0
	case "schedules":
		return len(p.Schedules) > 0
	case "images":
		return len(p.Images) > 0
	case "videos":
		return len(p.Videos) > 0
	}
	return false
}

// copyGroup 替换整个组，避免将本地与来源的关联规则拼接。
func copyGroup(dst *Pricing, src Pricing, g string) {
	switch g {
	case "base":
		dst.Base = src.Base
	case "tiers":
		dst.Tiers = src.Tiers
	case "modes":
		dst.Modes = src.Modes
	case "schedules":
		dst.Schedules = src.Schedules
		dst.Baseline = src.Baseline
	case "images":
		dst.Images = src.Images
	case "videos":
		dst.Videos = src.Videos
	}
}

// validPrice 只允许非负有限单价或倍率，倍率必须大于零。
func validPrice(v float64, multiplier bool) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && (!multiplier || v > 0)
}

// validateRates 校验公开输入的计费单位和重复维度，避免错误单价被持久化。
func validateRates(rates []Rate) error {
	seen := map[string]bool{}
	for _, r := range rates {
		if !contains([]string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "cache_write_12h", "cache_write_30m", "cache_read_audio", "cache_read_image", "cache_write_audio", "image_input", "image_output", "audio_input", "audio_output", "video_input", "video_output", "per_image", "per_image_input", "per_second", "per_second_input", "per_request"}, r.Key) || !contains([]string{"million_tokens", "image", "second", "request"}, r.Unit) || !validPrice(r.Price, false) || seen[r.Key] {
			return errors.New("价格维度、单位或金额无效，且同组不能重复")
		}
		expected := "million_tokens"
		switch r.Key {
		case "per_image", "per_image_input":
			expected = "image"
		case "per_second", "per_second_input":
			expected = "second"
		case "per_request":
			expected = "request"
		}
		if r.Unit != expected {
			return errors.New("计费维度与价格单位不一致")
		}
		seen[r.Key] = true
	}
	return nil
}

// validateTiers 要求阈值递增且每档有完整的显式价格，阈值语义为超过该输入量。
func validateTiers(tiers []Tier) error {
	last := 0
	for _, t := range tiers {
		if t.Above <= last || len(t.Rates) == 0 {
			return errors.New("上下文阶梯阈值须为递增正整数，每档至少填写一项价格")
		}
		if err := validateRates(t.Rates); err != nil {
			return err
		}
		last = t.Above
	}
	return nil
}

// minute 将本地日内时刻解析成分钟，24:00 只作为时段终点。
func minute(value string, end bool) (int, error) {
	if end && value == "24:00" {
		return 1440, nil
	}
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, errors.New("时段使用 HH:MM 格式")
	}
	return t.Hour()*60 + t.Minute(), nil
}

// validatePricing 在持久化入口校验价格组合，常用分时窗口用同一天内的不重叠时段表达。
func validatePricing(p Pricing) error {
	if err := validateRates(p.Base); err != nil {
		return err
	}
	if err := validateTiers(p.Tiers); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range p.Modes {
		if !contains([]string{"fast", "priority", "ultrafast"}, m.Name) || seen[m.Name] {
			return errors.New("处理模式仅支持 fast、priority 或 ultrafast，不能重复")
		}
		seen[m.Name] = true
		if m.Multiplier != nil {
			if !validPrice(*m.Multiplier, true) || len(m.Rates) > 0 || len(m.Tiers) > 0 {
				return errors.New("模式倍率与显式价格只能选择一种")
			}
		} else if len(m.Rates) == 0 && len(m.Tiers) == 0 {
			return errors.New("处理模式需要倍率或显式价格")
		}
		if err := validateRates(m.Rates); err != nil {
			return err
		}
		if err := validateTiers(m.Tiers); err != nil {
			return err
		}
	}
	if len(p.Schedules) > 0 && strings.TrimSpace(p.Baseline) == "" {
		return errors.New("请填写分时价格的基准口径")
	}
	for i, w := range p.Schedules {
		if _, err := time.LoadLocation(w.Timezone); err != nil {
			return errors.New("请输入有效 IANA 时区，例如 Asia/Shanghai")
		}
		a, err := minute(w.Start, false)
		if err != nil {
			return err
		}
		b, err := minute(w.End, true)
		if err != nil {
			return err
		}
		if a >= b || !validPrice(w.Multiplier, true) || len(w.Days) == 0 {
			return errors.New("分时窗口须在同一天内，终点晚于起点，并选择工作日及有效倍率；跨午夜请拆为两段")
		}
		days := map[int]bool{}
		for _, d := range w.Days {
			if d < 0 || d > 6 || days[d] {
				return errors.New("分时星期需为不重复的 0～6")
			}
			days[d] = true
		}
		for _, date := range w.ExcludedDates {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return errors.New("例外日期使用 YYYY-MM-DD 格式")
			}
		}
		for _, other := range p.Schedules[:i] {
			if other.Timezone != w.Timezone {
				return errors.New("同一模型的分时窗口使用同一时区")
			}
			c, _ := minute(other.Start, false)
			d, _ := minute(other.End, true)
			for _, day := range other.Days {
				if days[day] && a < d && c < b {
					return errors.New("同一天的分时窗口不能重叠")
				}
			}
		}
	}
	for _, v := range p.Images {
		if !validPrice(v.Price, false) || v.MinPixels < 0 || v.MaxPixels < 0 || (v.MaxPixels > 0 && v.MaxPixels < v.MinPixels) || (v.Size == "" && v.MaxPixels == 0) {
			return errors.New("图片价格需要尺寸或有效像素区间及非负价格")
		}
	}
	for _, v := range p.Videos {
		if strings.TrimSpace(v.Resolution) == "" || !validPrice(v.Price, false) {
			return errors.New("视频价格需要分辨率及非负每秒价格")
		}
	}
	return nil
}
