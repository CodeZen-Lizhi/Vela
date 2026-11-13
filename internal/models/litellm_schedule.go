package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// liteWindow 保留来源 UTC 小时范围及 ISO 星期；星期可使用独立时区解释。
type liteWindow struct {
	// Hours 接受单个或多个 HH:MM-HH:MM 的 UTC 时间段。
	Hours json.RawMessage `json:"hours_utc"`
	// Days 使用 ISO 1～7 的数字或字符串；缺失表示每天。
	Days json.RawMessage `json:"weekdays"`
}

// liteSchedules 验证共同倍率并转换为 UTC 每周窗口；不同维度倍率与夏令时星期无法静默近似。
func liteSchedules(data json.RawMessage, p *Pricing) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return errors.New("off_peak_pricing 格式无效")
	}
	rates := map[string]float64{}
	for key, v := range raw {
		if key == "windows" || key == "weekday_timezone" || key == "hours_utc" || key == "weekdays" {
			continue
		}
		mapped, ok := liteRateFields[key]
		if !ok {
			return fmt.Errorf("off_peak_pricing 不支持字段 %s", key)
		}
		_, scale := liteRateUnit(mapped)
		n, err := litePrice(v, scale)
		if err != nil {
			return fmt.Errorf("off_peak_pricing %s: %w", key, err)
		}
		if old, exists := rates[mapped]; exists && old != n {
			return errors.New("off_peak_pricing 同维度价格冲突")
		}
		rates[mapped] = n
	}
	base := map[string]float64{}
	for _, r := range p.Base {
		base[r.Key] = r.Price
	}
	ratio := -1.0
	for key, n := range rates {
		v, ok := base[key]
		if !ok {
			return fmt.Errorf("off_peak_pricing %s 缺少基准价格", key)
		}
		if v == 0 {
			if n != 0 {
				return errors.New("off_peak_pricing 无法由零基准转换倍率")
			}
			continue
		}
		r := n / v
		if !validPrice(r, true) {
			return errors.New("off_peak_pricing 倍率必须为正数")
		}
		if ratio >= 0 && math.Abs(r-ratio) > 1e-9*math.Max(r, ratio) {
			return errors.New("off_peak_pricing 不同维度倍率无法表达")
		}
		ratio = r
	}
	if ratio < 0 {
		return errors.New("off_peak_pricing 缺少可计算倍率的非零基准")
	}
	for key, n := range base {
		if _, ok := rates[key]; !ok && n != 0 && math.Abs(ratio-1) > 1e-9 {
			return fmt.Errorf("off_peak_pricing 未覆盖非零维度 %s，无法表达共同倍率", key)
		}
	}
	if len(p.Tiers) > 0 || len(p.Modes) > 0 || len(p.Images) > 0 || len(p.Videos) > 0 {
		return errors.New("off_peak_pricing 与阶梯、模式或媒体价格组合的倍率语义无法确认")
	}
	zone := liteString(raw, "weekday_timezone")
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return errors.New("off_peak_pricing weekday_timezone 无效")
	}
	// Schedule 只能使用单一时区；具有夏令时的独立星期时区不能转换为固定 UTC 周表。
	_, janOffset := time.Date(2026, 1, 5, 0, 0, 0, 0, loc).Zone()
	_, julOffset := time.Date(2026, 7, 6, 0, 0, 0, 0, loc).Zone()
	if janOffset != julOffset {
		return errors.New("off_peak_pricing weekday_timezone 含夏令时，无法表示为固定 UTC 窗口")
	}
	var windows []liteWindow
	if value, ok := raw["windows"]; ok {
		if json.Unmarshal(value, &windows) != nil || len(windows) == 0 {
			return errors.New("off_peak_pricing 缺少有效 windows")
		}
		if _, exists := raw["hours_utc"]; exists {
			return errors.New("off_peak_pricing 不能同时提供 windows 与平铺时段")
		}
	} else {
		windows = []liteWindow{{Hours: raw["hours_utc"], Days: raw["weekdays"]}}
	}
	// 一周仅 10080 个分钟；按交集压缩避免跨午夜和不同时区星期的边界误差。
	active := [7][1440]bool{}
	monday := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for _, w := range windows {
		days := w.Days
		if len(days) == 0 {
			days = raw["weekdays"]
		}
		weekdays, err := liteWeekdays(days)
		if err != nil {
			return err
		}
		var hours []string
		if json.Unmarshal(w.Hours, &hours) != nil {
			var one string
			if json.Unmarshal(w.Hours, &one) != nil {
				return errors.New("off_peak_pricing hours_utc 格式无效")
			}
			hours = []string{one}
		}
		if len(hours) == 0 {
			return errors.New("off_peak_pricing hours_utc 不能为空")
		}
		for _, span := range hours {
			parts := strings.Split(span, "-")
			if len(parts) != 2 {
				return errors.New("off_peak_pricing 时间段格式无效")
			}
			start, e := minute(parts[0], false)
			if e != nil {
				return e
			}
			end, e := minute(parts[1], true)
			if e != nil {
				return e
			}
			for day := 0; day < 7; day++ {
				for m := 0; m < 1440; m++ {
					inside := start == end || start < end && m >= start && m < end || start > end && (m >= start || m < end)
					if !inside {
						continue
					}
					instant := monday.Add(time.Duration(day*1440+m) * time.Minute)
					iso := int(instant.In(loc).Weekday())
					if iso == 0 {
						iso = 7
					}
					if weekdays[iso] {
						active[int(instant.Weekday())][m] = true
					}
				}
			}
		}
	}
	// 本次转换的时区和倍率固定相同，按起止时刻合并星期，保持每分钟覆盖完全等价。
	positions := map[[2]string]int{}
	for iso := 1; iso <= 7; iso++ {
		day := iso % 7
		for m := 0; m < 1440; {
			if !active[day][m] {
				m++
				continue
			}
			start := m
			for m < 1440 && active[day][m] {
				m++
			}
			begin, end := fmt.Sprintf("%02d:%02d", start/60, start%60), fmt.Sprintf("%02d:%02d", m/60, m%60)
			key := [2]string{begin, end}
			if pos, ok := positions[key]; ok {
				p.Schedules[pos].Days = append(p.Schedules[pos].Days, day)
			} else {
				positions[key] = len(p.Schedules)
				p.Schedules = append(p.Schedules, Schedule{Timezone: "UTC", Days: []int{day}, Start: begin, End: end, Multiplier: ratio, ExcludedDates: []string{}})
			}
		}
	}
	if len(p.Schedules) == 0 {
		return errors.New("off_peak_pricing 没有生效时段")
	}
	p.Baseline = "standard"
	return nil
}

// liteWeekdays 接受 ISO 数字、数字字符串数组或逗号分隔字符串；缺失表示每天，空列表和非法值拒绝。
func liteWeekdays(data json.RawMessage) (map[int]bool, error) {
	days := map[int]bool{}
	if len(data) == 0 {
		for day := 1; day <= 7; day++ {
			days[day] = true
		}
		return days, nil
	}
	var values []json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		var text string
		if json.Unmarshal(data, &text) != nil {
			return nil, errors.New("off_peak_pricing 星期格式无效")
		}
		for _, v := range strings.Split(text, ",") {
			encoded, err := json.Marshal(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
			values = append(values, encoded)
		}
	}
	for _, value := range values {
		var day int
		if json.Unmarshal(value, &day) != nil {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, errors.New("off_peak_pricing 星期必须为 ISO 1～7")
			}
			n, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil {
				return nil, errors.New("off_peak_pricing 星期必须为 ISO 1～7")
			}
			day = n
		}
		if day < 1 || day > 7 || days[day] {
			return nil, errors.New("off_peak_pricing 星期必须为不重复的 ISO 1～7")
		}
		days[day] = true
	}
	if len(days) == 0 {
		return nil, errors.New("off_peak_pricing 星期不能为空")
	}
	return days, nil
}
