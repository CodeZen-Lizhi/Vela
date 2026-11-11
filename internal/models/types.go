// Package models 管理统一模型、来源目录与本地价格规则，不依赖桌面运行时。
package models

import (
	"encoding/json"
	"errors"
)

// UnmarshalJSON 拒绝遗漏或 null 的单价，保留明确填写的零价及现有存储格式。
func (r *Rate) UnmarshalJSON(data []byte) error {
	type plain Rate
	var value struct {
		plain
		// Price 仅在输入边界区分缺失值与明确零价。
		Price *float64 `json:"price"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.Price == nil {
		return errors.New("请填写价格，未填写不能作为零价保存")
	}
	value.plain.Price = *value.Price
	*r = Rate(value.plain)
	return nil
}

// UnmarshalJSON 对图片每张单价执行与基础单价相同的必填校验。
func (r *ImagePrice) UnmarshalJSON(data []byte) error {
	type plain ImagePrice
	var value struct {
		plain
		// Price 仅在输入边界区分缺失值与明确零价。
		Price *float64 `json:"price"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.Price == nil {
		return errors.New("请填写图片价格，未填写不能作为零价保存")
	}
	value.plain.Price = *value.Price
	*r = ImagePrice(value.plain)
	return nil
}

// UnmarshalJSON 对视频每秒单价执行与基础单价相同的必填校验。
func (r *VideoPrice) UnmarshalJSON(data []byte) error {
	type plain VideoPrice
	var value struct {
		plain
		// Price 仅在输入边界区分缺失值与明确零价。
		Price *float64 `json:"price"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.Price == nil {
		return errors.New("请填写视频价格，未填写不能作为零价保存")
	}
	value.plain.Price = *value.Price
	*r = VideoPrice(value.plain)
	return nil
}

// Rate 是一项美元单价；Key 指定计费维度，Unit 为 million_tokens、image、second 或 request。
type Rate struct {
	// Key 计费维度键，与价格单位一起决定含义。
	Key string `json:"key"`
	// Price 非负美元单价，零明确表示免费。
	Price float64 `json:"price"`
	// Unit 价格单位，取 million_tokens、image、second 或 request。
	Unit string `json:"unit"`
}

// Tier 在输入上下文超过 Above 个 Token 时应用整套价格，不混合来源档位。
type Tier struct {
	// Above 输入上下文严格超过此 Token 数时进入该档。
	Above int `json:"above"`
	// Rates 当前规则的显式单价；空集合表示未提供。
	Rates []Rate `json:"rates"`
}

// Mode 表示 fast 或 priority 模式；倍率与显式价格二选一。
type Mode struct {
	// Name 模型显示名称或处理模式标识，按所属类型解释。
	Name string `json:"name"`
	// Multiplier 相对 Standard 的正倍率；为空时使用显式价格。
	Multiplier *float64 `json:"multiplier"`
	// Rates 当前规则的显式单价；空集合表示未提供。
	Rates []Rate `json:"rates"`
	// Tiers 按输入量递增的整套上下文阶梯。
	Tiers []Tier `json:"tiers"`
}

// Schedule 描述左闭右开的每日时段；Days 使用 0～6 表示周日到周六，ExcludedDates 为当地日期。
type Schedule struct {
	// Timezone IANA 时区名，用于解释日内时段和日期。
	Timezone string `json:"timezone"`
	// Days 生效星期，0 表示周日，6 表示周六。
	Days []int `json:"days"`
	// Start 包含的当地开始时刻，格式 HH:MM。
	Start string `json:"start"`
	// End 不包含的当地结束时刻，可用 24:00。
	End string `json:"end"`
	// Multiplier 分时窗口内相对基准口径的正倍率，必须大于零。
	Multiplier float64 `json:"multiplier"`
	// ExcludedDates 排除的当地日期，格式 YYYY-MM-DD。
	ExcludedDates []string `json:"excludedDates"`
}

// ImagePrice 按尺寸和质量给出每张美元价格；像素区间通过 MinPixels/MaxPixels 表达。
type ImagePrice struct {
	// Size 来源或用户明确配置的图片尺寸，区间计价时可为空。
	Size string `json:"size"`
	// Quality 来源或用户配置的图片质量档位。
	Quality string `json:"quality"`
	// MinPixels 像素回退区间下界，零表示未限定。
	MinPixels int `json:"minPixels"`
	// MaxPixels 像素回退区间上界，零表示未使用区间。
	MaxPixels int `json:"maxPixels"`
	// Price 非负美元单价，零明确表示免费。
	Price float64 `json:"price"`
}

// VideoPrice 给出指定分辨率每秒的美元价格。
type VideoPrice struct {
	// Resolution 来源或用户明确配置的分辨率档位。
	Resolution string `json:"resolution"`
	// Price 非负美元单价，零明确表示免费。
	Price float64 `json:"price"`
}

// Pricing 将可独立编辑和恢复的价格规则分组；空集合表示未知，不代表免费。
type Pricing struct {
	// Base 基础单价集合；缺失维度保持未知。
	Base []Rate `json:"base"`
	// Tiers 按输入量递增的整套上下文阶梯。
	Tiers []Tier `json:"tiers"`
	// Modes Fast 和 priority 模式的倍率或显式价格。
	Modes []Mode `json:"modes"`
	// Schedules 同一时区内的分时窗口，窗口不能重叠。
	Schedules []Schedule `json:"schedules"`
	// Baseline 分时倍率所依赖的基础价格口径。
	Baseline string `json:"baseline"`
	// Images 按尺寸、质量或像素区间给出的每张价。
	Images []ImagePrice `json:"images"`
	// Videos 按分辨率给出的每秒价。
	Videos []VideoPrice `json:"videos"`
}

// Model 保持本地稳定身份与来源身份分离；SourceAvailable 记录最近目录是否仍包含此来源模型。
type Model struct {
	// ID 本地稳定身份，删除重建时不复用。
	ID string `json:"id"`
	// Revision 每次保存或同步后递增；旧记录缺失时为零，用于拒绝过期编辑草稿。
	Revision int64 `json:"revision"`
	// ModelID 统一模型名称或来源候选名称。
	ModelID string `json:"modelId"`
	// Name 模型显示名称或处理模式标识，按所属类型解释。
	Name string `json:"name"`
	// Family 来源声明的模型系列；空值表示未知。
	Family string `json:"family"`
	// Kind chat、image、video 或 vector 类型。
	Kind string `json:"kind"`
	// Context 来源声明的上下文 Token 上限；零表示未知。
	Context int `json:"context"`
	// Capabilities 来源声明的输入类型、思考与工具能力。
	Capabilities []string `json:"capabilities"`
	// ProviderID 价格来源供应商的稳定标识，不是用户中转站配置。
	ProviderID string `json:"providerId"`
	// ProviderName 价格来源供应商显示名称。
	ProviderName string `json:"providerName"`
	// SourceModelID 在选定来源供应商内用于同步的模型名称。
	SourceModelID string `json:"sourceModelId"`
	// CanonicalID 来源声明的规范模型身份，缺失时不根据名称猜测。
	CanonicalID string `json:"canonicalId"`
	// Official 只有来源规范身份归属与供应商一致时标为官方。
	Official bool `json:"official"`
	// SourceAvailable 最近成功读取的目录是否仍存在该来源模型。
	SourceAvailable bool `json:"sourceAvailable"`
	// SyncedAt 最近成功保存来源价格的 UTC RFC 3339 时间。
	SyncedAt string `json:"syncedAt"`
	// Pricing 当前生效的完整本地价格规则。
	Pricing Pricing `json:"pricing"`
	// SourcePricing 最近成功读取的来源规则，不含本地覆盖。
	SourcePricing Pricing `json:"sourcePricing"`
	// Overrides 受到本地修改保护的价格规则组。
	Overrides []string `json:"overrides"`
}

// CatalogEntry 是搜索目录候选；非官方来源必须由用户主动选择。
type CatalogEntry struct {
	// ProviderID 价格来源供应商的稳定标识，不是用户中转站配置。
	ProviderID string `json:"providerId"`
	// ProviderName 价格来源供应商显示名称。
	ProviderName string `json:"providerName"`
	// ModelID 统一模型名称或来源候选名称。
	ModelID string `json:"modelId"`
	// CanonicalID 来源声明的规范模型身份，缺失时不根据名称猜测。
	CanonicalID string `json:"canonicalId"`
	// Name 模型显示名称或处理模式标识，按所属类型解释。
	Name string `json:"name"`
	// Family 来源声明的模型系列；空值表示未知。
	Family string `json:"family"`
	// Kind chat、image、video 或 vector 类型。
	Kind string `json:"kind"`
	// Context 来源声明的上下文 Token 上限；零表示未知。
	Context int `json:"context"`
	// Capabilities 来源声明的输入类型、思考与工具能力。
	Capabilities []string `json:"capabilities"`
	// Official 只有来源规范身份归属与供应商一致时标为官方。
	Official bool `json:"official"`
	// Pricing 当前生效的完整本地价格规则。
	Pricing Pricing `json:"pricing"`
	// Problem 该来源候选无法导入的原因；空值表示有效。
	Problem string `json:"problem"`
}

// Edit 仅开放显示名称及价格，不接受调用方改写来源、身份或修改保护标记。
type Edit struct {
	// ID 本地稳定身份，删除重建时不复用。
	ID string `json:"id"`
	// Revision 打开编辑时读取的模型版本，须与当前保存版本一致。
	Revision int64 `json:"revision"`
	// Name 模型显示名称或处理模式标识，按所属类型解释。
	Name string `json:"name"`
	// Pricing 当前生效的完整本地价格规则。
	Pricing Pricing `json:"pricing"`
	// RestoreGroups 指定本次保存时恢复来源并解除保护的规则组。
	RestoreGroups []string `json:"restoreGroups"`
}

// SyncResult 返回按模型提交的同步结果；Failures 同时包含保留原配置的原因。
type SyncResult struct {
	// Updated 本次已成功保存来源价格的模型数量。
	Updated int `json:"updated"`
	// Failures 未更新模型及原因，失败模型保留已有配置。
	Failures []string `json:"failures"`
}

// Proxy 是全局目录网络设置；关闭时直连，开启时使用指定 HTTP/SOCKS 地址。
type Proxy struct {
	// Enabled 是否启用唯一全局代理，关闭时明确直连。
	Enabled bool `json:"enabled"`
	// URL 全局代理地址，只有开启时参与目录连接。
	URL string `json:"url"`
}

// Create 提交创建草稿，目录资料由服务端读取，空来源表示手动创建。
type Create struct {
	// ProviderID 精确目录来源；为空时不关联任何目录。
	ProviderID string `json:"providerId"`
	// ModelID 目录候选 ID 或手动填写的真实调用名称。
	ModelID string `json:"modelId"`
	// Name 用户确认的显示名称，1～80 个字符。
	Name string `json:"name"`
	// Kind 仅用于手动模型，取 chat、image、video 或 vector。
	Kind string `json:"kind"`
	// Pricing 本地有效价格；空规则表示未知。
	Pricing Pricing `json:"pricing"`
}
