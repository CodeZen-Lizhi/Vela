package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/outbound"
)

// rawModel 仅接收来源明确提供的字段；价格保留 RawMessage 以便单个模型非法时仍更新其他模型。
type rawModel struct {
	// 来源字段保留其身份、能力、上下文、价格和处理模式语义；Cost 留待逐模型校验。
	ID          string `json:"id"`
	Name        string `json:"name"`
	Family      string `json:"family"`
	Type        string `json:"type"`
	CanonicalID string `json:"canonical_model_id"`
	// ReleaseDate 是来源声明的发布日期，支持 YYYY-MM-DD 或仅有年月的 YYYY-MM。
	ReleaseDate string `json:"release_date"`
	// Status 为来源生命周期标记；deprecated 只从在线添加候选中排除，不影响已有模型同步。
	Status           string `json:"status"`
	StructuredOutput bool   `json:"structured_output"`
	ToolCall         bool   `json:"tool_call"`
	Reasoning        bool   `json:"reasoning"`
	Limit            struct {
		Context int `json:"context"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost         json.RawMessage `json:"cost"`
	Experimental struct {
		Modes map[string]struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"modes"`
	} `json:"experimental"`
}

// rawProvider 接收 basellm 的供应商与计费元数据，不使用其推理地址。
type rawProvider struct {
	// ID 为原生厂商身份，Doc 是公开价格文档地址。
	ID  string `json:"id"`
	Doc string `json:"doc"`
	// Subscription 标记预付套餐，来源的零 Token 价不能解释为免费。
	Subscription bool `json:"subscription"`
	// Billing 仅使用来源明确提供的长期缓存倍率。
	Billing struct {
		// CacheWrite1h、CacheWrite12h 分别表示一小时与十二小时缓存相对输入价的倍率。
		CacheWrite1h  float64 `json:"cacheWrite1h"`
		CacheWrite12h float64 `json:"cacheWrite12h"`
	} `json:"billing"`
	// Name 是显示名称，Models 按来源模型 ID 保存完整原始条目。
	Name   string                     `json:"name"`
	Models map[string]json.RawMessage `json:"models"`
}

// proxyTransport 显式直连或使用全局代理，避免关闭开关后继续隐式使用环境代理；代理规则由 outbound 统一定义。
func proxyTransport(p Proxy) (*http.Transport, error) {
	if !p.Enabled {
		return outbound.Transport(nil), nil
	}
	u, err := outbound.ParseProxy(p.URL)
	if err != nil {
		return nil, err
	}
	return outbound.Transport(u), nil
}

// decodeProviderCatalog 将 basellm 原生厂商目录映射为本地候选，弃用条目仍保留供已有模型同步。
func decodeProviderCatalog(data []byte, exchangeRates map[string]float64) ([]CatalogEntry, error) {
	var err error
	var providers map[string]rawProvider
	if err = json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("模型目录格式错误: %w", err)
	}
	if len(providers) == 0 {
		return nil, errors.New("模型目录为空，已保留本地配置")
	}
	entries := []CatalogEntry{}
	// 区分全部被弃用过滤的正常空结果与来源本身没有任何模型。
	deprecatedCount := 0
	// 发布日期只用于本轮目录排序，不改变持久模型或桌面绑定契约；键为供应商 ID 和模型 ID。
	releaseDates := make(map[[2]string]time.Time)
	for pid, p := range providers {
		for mid, raw := range p.Models {
			entry := CatalogEntry{ProviderID: pid, ProviderName: p.Name, ModelID: mid, SourceKey: pid + "/" + mid, SourceURL: p.Doc, Metadata: append(json.RawMessage(nil), raw...), Capabilities: []string{}, Warnings: []string{}}
			var m rawModel
			if err = json.Unmarshal(raw, &m); err != nil {
				entry.Name = mid
				entry.Problem = "模型来源格式无效"
				entries = append(entries, entry)
				continue
			}

			entry.Deprecated = m.Status == "deprecated"
			if entry.Deprecated {
				deprecatedCount++
			}
			if m.Type != "" && !contains([]string{"chat", "image", "video", "embedding", "rerank", "reranking"}, m.Type) {
				continue
			}
			entry.Name = m.Name
			releaseDates[[2]string{pid, mid}] = catalogReleaseDate(m.ReleaseDate)
			if entry.Name == "" {
				entry.Name = mid
			}
			entry.Family = m.Family
			entry.CanonicalID = m.CanonicalID
			entry.Context = m.Limit.Context
			entry.Kind = "chat"
			switch {
			case m.Type == "embedding" || m.Type == "rerank" || m.Type == "reranking":
				entry.Kind = "vector"
			case m.Type == "video" || contains(m.Modalities.Output, "video"):
				entry.Kind = "video"
			case m.Type == "image" || contains(m.Modalities.Output, "image"):
				entry.Kind = "image"
			}
			entry.Capabilities = append([]string{}, m.Modalities.Input...)
			if m.Reasoning {
				entry.Capabilities = append(entry.Capabilities, "reasoning")
			}
			if m.StructuredOutput {
				entry.Capabilities = append(entry.Capabilities, "structured")
			}
			if m.ToolCall {
				entry.Capabilities = append(entry.Capabilities, "tools")
			}
			entry.Official = m.CanonicalID != "" && strings.SplitN(m.CanonicalID, "/", 2)[0] == pid || m.CanonicalID == "" && p.ID == pid
			// 规范身份归属优先；原生厂商托管的外部模型不能误标为该模型的官方来源。
			entry.Pricing, err = decodeBasellmPricing(m.Cost, m.Experimental.Modes, entry.Kind, exchangeRates, p.Billing.CacheWrite1h, p.Billing.CacheWrite12h)
			if err != nil {
				entry.Problem = err.Error()
			}
			if p.Subscription {
				entry.Pricing = Pricing{Base: []Rate{}, Tiers: []Tier{}, Modes: []Mode{}, Schedules: []Schedule{}, Images: []ImagePrice{}, Videos: []VideoPrice{}}
				entry.Problem = ""
				entry.Warnings = append(entry.Warnings, "来源为预付套餐，未提供按量单价；价格保持未知")
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		ad, bd := releaseDates[[2]string{a.ProviderID, a.ModelID}], releaseDates[[2]string{b.ProviderID, b.ModelID}]
		if !ad.Equal(bd) {
			return ad.After(bd)
		}
		if a.Official != b.Official {
			return a.Official
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.ProviderID != b.ProviderID {
			return a.ProviderID < b.ProviderID
		}
		return a.ModelID < b.ModelID
	})
	if len(entries) == 0 && deprecatedCount == 0 {
		return nil, errors.New("目录没有模型，已保留本地配置")
	}
	return entries, nil
}

// catalogReleaseDate 解析来源发布日期；仅有年月时按月初排序，缺失或非法值排在已知日期之后。
func catalogReleaseDate(value string) time.Time {
	for _, layout := range []string{time.DateOnly, "2006-01"} {
		if date, err := time.Parse(layout, value); err == nil {
			return date
		}
	}
	return time.Time{}
}

// decodeRates 映射明确的 Token 价格；独立推理单价无法表达时拒绝导入，不从 Token 价猜测图片或秒价。
func decodeRates(raw json.RawMessage, kind string) ([]Rate, error) {
	result := []Rate{}
	if len(raw) == 0 || string(raw) == "null" {
		return result, nil
	}
	var costs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &costs); err != nil {
		return nil, errors.New("来源价格格式无效")
	}
	// reasoning 与普通输出同价时可共用 output；不同价、缺少输出价或非法值不能静默丢弃。
	if value, ok := costs["reasoning"]; ok {
		var reasoning, output float64
		if string(value) == "null" || json.Unmarshal(value, &reasoning) != nil || !validPrice(reasoning, false) ||
			len(costs["output"]) == 0 || string(costs["output"]) == "null" || json.Unmarshal(costs["output"], &output) != nil || !validPrice(output, false) || reasoning != output {
			return nil, errors.New("来源 reasoning 单价无法独立表达，已保留本地配置")
		}
	}
	for _, key := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "cache_write_12h", "input_audio", "output_audio", "input_image", "output_image", "per_image", "per_second", "per_request"} {
		value, ok := costs[key]
		if !ok || string(value) == "null" {
			continue
		}
		if kind == "vector" && key == "output" {
			continue
		}
		var price float64
		if err := json.Unmarshal(value, &price); err != nil || !validPrice(price, false) {
			return nil, fmt.Errorf("来源 %s 单价无效", key)
		}
		normalized := key
		if key == "input_audio" {
			normalized = "audio_input"
		}
		if key == "output_audio" {
			normalized = "audio_output"
		}
		if key == "input_image" {
			normalized = "image_input"
		}
		if key == "output_image" {
			normalized = "image_output"
		}
		unit, _ := liteRateUnit(normalized)
		result = append(result, Rate{Key: normalized, Price: price, Unit: unit})
	}
	return result, nil
}

// decodePricing 读取明确上下文阈值和 Fast 显式价格；其余未知维度留空供用户补充。
func decodePricing(raw json.RawMessage, modes map[string]struct {
	Cost json.RawMessage `json:"cost"`
}, kind string) (Pricing, error) {
	p := Pricing{Base: []Rate{}, Tiers: []Tier{}, Modes: []Mode{}, Schedules: []Schedule{}, Images: []ImagePrice{}, Videos: []VideoPrice{}}
	var err error
	p.Base, err = decodeRates(raw, kind)
	if err != nil {
		return p, err
	}
	if len(raw) > 0 && string(raw) != "null" {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			return p, err
		}
		if value, ok := fields["tiers"]; ok {
			var tiers []json.RawMessage
			if err = json.Unmarshal(value, &tiers); err != nil {
				return p, errors.New("来源阶梯格式无效")
			}
			for _, r := range tiers {
				var t struct {
					Tier struct {
						Type string `json:"type"`
						Size int    `json:"size"`
					} `json:"tier"`
				}
				if err = json.Unmarshal(r, &t); err != nil || t.Tier.Type != "context" || t.Tier.Size <= 0 {
					return p, errors.New("来源上下文阶梯缺少明确边界")
				}
				rates, e := decodeRates(r, kind)
				if e != nil {
					return p, e
				}
				p.Tiers = append(p.Tiers, Tier{Above: t.Tier.Size, Rates: rates})
			}
			sort.Slice(p.Tiers, func(i, j int) bool { return p.Tiers[i].Above < p.Tiers[j].Above })
			previous := p.Base
			for i := range p.Tiers {
				p.Tiers[i].Rates = inheritRates(previous, p.Tiers[i].Rates)
				previous = p.Tiers[i].Rates
			}
		}
	}
	for _, name := range []string{"fast", "priority"} {
		if m, ok := modes[name]; ok {
			modePricing, e := decodePricing(m.Cost, nil, kind)
			if e != nil {
				return p, e
			}
			if len(modePricing.Base) > 0 || len(modePricing.Tiers) > 0 {
				p.Modes = append(p.Modes, Mode{Name: name, Rates: modePricing.Base, Tiers: modePricing.Tiers})
			}
		}
	}
	return p, validatePricing(p)
}

// syncEntries 在写入锁内按精确来源身份合并价格；无效或缺失条目保留已有配置。
func (s *Store) syncEntries(ctx context.Context, entries []CatalogEntry, syncedAt string) (SyncResult, error) {
	result := SyncResult{Failures: []string{}}
	lookup := map[string]CatalogEntry{}
	for _, e := range entries {
		lookup[e.ProviderID+"\x00"+e.ModelID] = e
	}
	list, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	for _, m := range list {
		if m.ProviderID == "" {
			continue
		}
		e, ok := lookup[m.ProviderID+"\x00"+m.SourceModelID]
		if !ok {
			if m.SourceAvailable {
				m.SourceAvailable = false
				if _, err = s.save(ctx, m); err != nil {
					return result, err
				}
			}
			result.Failures = append(result.Failures, m.ModelID+"：新来源没有对应条目，保留本地配置")
			continue
		}
		if e.Problem != "" {
			result.Failures = append(result.Failures, m.ModelID+"："+e.Problem)
			continue
		}
		previous, _ := json.Marshal(m)
		if e.Kind == "vector" && m.Kind != "vector" {
			m.Kind = "vector"
			removeVectorOutput(&m.Pricing)
		}
		for _, g := range groups {
			if hasGroup(e.Pricing, g) && !contains(m.Overrides, g) {
				mergeSourceGroup(&m.Pricing, e.Pricing, g)
			}
		}
		m.SourcePricing = e.Pricing
		m.SourceAvailable = true
		m.SyncedAt = syncedAt
		if e.SourceKey != "" {
			m.Source = catalogSource
			m.SourceKey = e.SourceKey
		}
		m.Warnings = e.Warnings
		current, _ := json.Marshal(m)
		if string(previous) == string(current) {
			result.Updated++
			continue
		}
		// 旧模型的本地 ID 和 canonical 身份仍保留，精确 provider/source ID 足以迁移。
		if _, err = s.save(ctx, m); err != nil {
			return result, fmt.Errorf("%s 同步保存失败: %w", m.ModelID, err)
		}
		result.Updated++
	}
	return result, nil
}

// mergeSourceGroup 按规则组更新来源；基础价格缺失项保持已知值，其余关联规则整体替换。
func mergeSourceGroup(dst *Pricing, src Pricing, group string) {
	if group != "base" {
		copyGroup(dst, src, group)
		return
	}
	merged := append([]Rate{}, dst.Base...)
	for _, rate := range src.Base {
		found := false
		for i := range merged {
			if merged[i].Key == rate.Key {
				merged[i] = rate
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, rate)
		}
	}
	dst.Base = merged
}
