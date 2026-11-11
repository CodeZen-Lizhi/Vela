package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Status    string `json:"status"`
	ToolCall  bool   `json:"tool_call"`
	Reasoning bool   `json:"reasoning"`
	Limit     struct {
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

// rawProvider 是 models.dev api.json 的供应商条目，不使用其推理地址。
type rawProvider struct {
	// Name 为来源名称，Models 按来源模型 ID 保存原始条目。
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

// fetchCatalog 有界读取来源并按发布日期倒序排列；includeDeprecated 控制是否保留已弃用条目供同步使用。
func (s *Store) fetchCatalog(ctx context.Context, includeDeprecated bool) ([]CatalogEntry, error) {
	p, err := s.GetProxy(ctx)
	if err != nil {
		return nil, err
	}
	transport, err := proxyTransport(p)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	client := *s.client
	client.Transport = transport
	if client.Timeout == 0 {
		client.Timeout = 45 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.catalogURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法读取模型目录: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("模型目录返回 HTTP %d", response.StatusCode)
	}
	const limit = 32 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("模型目录超过 32 MB")
	}
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
			entry := CatalogEntry{ProviderID: pid, ProviderName: p.Name, ModelID: mid}
			var m rawModel
			if err = json.Unmarshal(raw, &m); err != nil {
				entry.Name = mid
				entry.Problem = "模型来源格式无效"
				entries = append(entries, entry)
				continue
			}
			if !includeDeprecated && m.Status == "deprecated" {
				deprecatedCount++
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
			case m.Type == "embedding" || m.Type == "rerank":
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
			if m.ToolCall {
				entry.Capabilities = append(entry.Capabilities, "tools")
			}
			entry.Official = m.CanonicalID != "" && strings.SplitN(m.CanonicalID, "/", 2)[0] == pid
			// 无 canonical 字段时只能确认来源身份，不能凭名称把中转站判断为官方。
			entry.Pricing, err = decodePricing(m.Cost, m.Experimental.Modes, entry.Kind)
			if err != nil {
				entry.Problem = err.Error()
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

// decodeRates 只映射 API 已声明的 Token 价格，不从 input/output 猜测图片或秒价。
func decodeRates(raw json.RawMessage, kind string) ([]Rate, error) {
	result := []Rate{}
	if len(raw) == 0 || string(raw) == "null" {
		return result, nil
	}
	var costs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &costs); err != nil {
		return nil, errors.New("来源价格格式无效")
	}
	for _, key := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "input_audio", "output_audio"} {
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
		result = append(result, Rate{Key: normalized, Price: price, Unit: "million_tokens"})
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

// Catalog 重新读取未弃用的目录候选，添加时从该结果取值而不信任前端传来的价格。
func (s *Store) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	entries, err := s.fetchCatalog(ctx, false)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.catalog = entries
	s.mu.Unlock()
	return entries, nil
}

// Sync 全量读取来源后按模型更新；来源缺失和非法模型保留原价格，成功模型独立提交。
func (s *Store) Sync(ctx context.Context) (SyncResult, error) {
	result := SyncResult{Failures: []string{}}
	entries, err := s.fetchCatalog(ctx, true)
	if err != nil {
		return result, err
	}
	lookup := map[string]CatalogEntry{}
	for _, e := range entries {
		lookup[e.ProviderID+"\x00"+e.ModelID] = e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = entries
	list, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	for _, m := range list {
		// 手动模型不自动关联同名目录，也不报告来源下架。
		if m.ProviderID == "" {
			continue
		}
		e, ok := lookup[m.ProviderID+"\x00"+m.SourceModelID]
		if !ok {
			m.SourceAvailable = false
			if err = s.save(ctx, m); err != nil {
				return result, err
			}
			result.Failures = append(result.Failures, m.ModelID+"：来源已下架，保留本地配置")
			continue
		}
		if e.Problem != "" {
			result.Failures = append(result.Failures, m.ModelID+"："+e.Problem)
			continue
		}
		for _, g := range groups {
			if hasGroup(e.Pricing, g) && !contains(m.Overrides, g) {
				if g == "base" {
					// 来源移除单个价格字段时保留本地已知值，仍允许其他来源字段更新。
					merged := append([]Rate{}, m.Pricing.Base...)
					for _, rate := range e.Pricing.Base {
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
					m.Pricing.Base = merged
				} else {
					copyGroup(&m.Pricing, e.Pricing, g)
				}
			}
		}
		m.SourcePricing = e.Pricing
		m.SourceAvailable = true
		m.SyncedAt = time.Now().UTC().Format(time.RFC3339)
		if err = s.save(ctx, m); err != nil {
			return result, fmt.Errorf("%s 同步保存失败: %w", m.ModelID, err)
		}
		result.Updated++
	}
	return result, nil
}
