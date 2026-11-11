// Package providers 管理上游供应商、上游 Key 及其凭据保护，不依赖桌面运行时。
//
// 导出类型会原样成为前端的类型名，因此统一带上 Provider 等前缀，避免与其他模块的类型重名。
package providers

// 请求配置的默认值；RequestPolicy 中对应字段为 0 时使用这里的值。
const (
	// defaultAttempts 是总尝试次数（含首次）。
	defaultAttempts = 1
	// defaultStreamTotalMillis 是流式请求总超时，单位毫秒。
	defaultStreamTotalMillis = 120_000
	// defaultFirstByteSeconds 是首次响应超时，单位秒。
	defaultFirstByteSeconds = 60
	// defaultRequestSeconds 是非流式请求超时，单位秒。
	defaultRequestSeconds = 300
)

// 新增上游 Key 时表单预填的默认值。
const (
	// defaultKeyMultiplier 是手动倍率。
	defaultKeyMultiplier = 1.0
	// defaultKeyCacheMinutes 是缓存 TTL，单位分钟。
	defaultKeyCacheMinutes = 5
	// defaultKeyProbeMinutes 是熔断探测间隔上限，单位分钟。
	defaultKeyProbeMinutes = 32
)

// initialKeyHealth 是 Key 的初始健康分，满分 10。
// 网关执行接入前没有调用结果可以结算，每把 Key 的健康分恒为该值；接入后由执行结果更新。
const initialKeyHealth = 10.0

// ProxyMode 是供应商或上游 Key 的出口方式。
type ProxyMode string

const (
	// ProxyGlobal 跟随“设置 → 网络代理”的全局代理。
	ProxyGlobal ProxyMode = "global"
	// ProxyDirect 不使用代理，直接连接。
	ProxyDirect ProxyMode = "direct"
	// ProxyCustom 使用 ProxyChoice.URL 指定的代理地址。
	ProxyCustom ProxyMode = "custom"
	// ProxyProvider 跟随所属供应商的出口方式，只用于上游 Key。
	ProxyProvider ProxyMode = "provider"
)

// ProxyChoice 是供应商或上游 Key 的局部出口选择。
type ProxyChoice struct {
	// Mode 出口方式；供应商可取 global、direct、custom，上游 Key 另可取 provider。
	Mode ProxyMode `json:"mode"`
	// URL 自定义代理地址，仅在 Mode 为 custom 时有值，协议为 http、https、socks5 或 socks5h。
	URL string `json:"url"`
}

// RequestPolicy 是供应商的请求配置。本模块只负责保存与校验，执行语义由网关与调度实现。
type RequestPolicy struct {
	// Attempts 总尝试次数（含首次），1～99；0 表示使用默认值。
	Attempts int `json:"attempts"`
	// StreamTotalMillis 流式请求总超时，单位毫秒，1000～1200000；0 表示使用默认值。
	StreamTotalMillis int `json:"streamTotalMillis"`
	// FirstByteSeconds 首次响应超时，单位秒，1～300；0 表示使用默认值。
	FirstByteSeconds int `json:"firstByteSeconds"`
	// RequestSeconds 非流式请求超时，单位秒，1～1200；0 表示使用默认值。
	RequestSeconds int `json:"requestSeconds"`
	// MaxTransfers 最大转移次数；0 表示不限制。
	MaxTransfers int `json:"maxTransfers"`
	// MaxTransferSeconds 最大转移超时，单位秒；0 表示不限制。
	MaxTransferSeconds int `json:"maxTransferSeconds"`
	// Proxy 供应商的出口选择。
	Proxy ProxyChoice `json:"proxy"`
	// ResponsesWebSocket 是否启用 Responses WebSocket 模式。
	ResponsesWebSocket bool `json:"responsesWebSocket"`
	// TransportFailover 传输失败后是否继续转移。
	TransportFailover bool `json:"transportFailover"`
}

// Provider 是一个上游供应商（中转站）的本地配置。
type Provider struct {
	// ID 稳定身份，删除后重建同名供应商不会复用。
	ID string `json:"id"`
	// Name 名称，全局唯一且区分大小写。
	Name string `json:"name"`
	// Website 供应商主站链接，可为空；与服务地址无关，不由服务地址推导。
	Website string `json:"website"`
	// URL 服务地址，只保存站点根地址，例如 https://api.example.com。
	URL string `json:"url"`
	// Note 备注，不参与调用。
	Note string `json:"note"`
	// Enabled 是否启用；停用后退出调用候选，Key 与其他配置保留。
	Enabled bool `json:"enabled"`
	// Request 请求配置。
	Request RequestPolicy `json:"request"`
}

// ProviderDraft 是新增或编辑供应商时可以提交的字段；启停通过单独的操作修改。
type ProviderDraft struct {
	// Name 名称，去掉首尾空格后为 1～80 个字符。
	Name string `json:"name"`
	// Website 主站链接；可为空，未写协议时按 https 处理。
	Website string `json:"website"`
	// URL 服务地址；可带 /v1 或 /v1beta 后缀，保存时去掉。
	URL string `json:"url"`
	// Note 备注。
	Note string `json:"note"`
	// Request 请求配置。
	Request RequestPolicy `json:"request"`
}

// ProviderHealthState 是供应商健康汇总的分档。
type ProviderHealthState string

const (
	// ProviderHealthNormal 平均健康度不低于 80%。
	ProviderHealthNormal ProviderHealthState = "normal"
	// ProviderHealthDegraded 平均健康度不低于 50%。
	ProviderHealthDegraded ProviderHealthState = "degraded"
	// ProviderHealthAbnormal 平均健康度低于 50%。
	ProviderHealthAbnormal ProviderHealthState = "abnormal"
	// ProviderHealthUnavailable 供应商停用或没有已启用的 Key，当前无法调用。
	ProviderHealthUnavailable ProviderHealthState = "unavailable"
)

// ProviderHealth 是供应商的健康汇总：已启用 Key 的健康分等权平均。
type ProviderHealth struct {
	// State 健康分档。
	State ProviderHealthState `json:"state"`
	// Percent 平均健康度，0～100 的整数；不可用时为 nil。
	Percent *int `json:"percent"`
	// Reason 不可用的原因；其他分档为空。
	Reason string `json:"reason"`
}

// PreferredKey 是供应商列表展示的首选 Key：已启用的 Key 中优先级数值最小的一把，优先级相同时取先创建的。
type PreferredKey struct {
	// Name Key 名称。
	Name string `json:"name"`
	// Multiplier 倍率；nil 表示未知，0 是有效值。
	Multiplier *float64 `json:"multiplier"`
}

// ProviderOverview 是供应商列表的一行：配置加上由 Key 汇总出的展示信息。
type ProviderOverview struct {
	// Provider 供应商配置。
	Provider Provider `json:"provider"`
	// KeyCount 上游 Key 的数量，包含已停用的。
	KeyCount int `json:"keyCount"`
	// PreferredKey 首选 Key；nil 表示暂无可用 Key。
	PreferredKey *PreferredKey `json:"preferredKey"`
	// Health 健康汇总。
	Health ProviderHealth `json:"health"`
}

// ProviderDefaults 是表单占位文字、预填值和“当前生效值”提示使用的默认值，由本模块唯一定义。
type ProviderDefaults struct {
	// Attempts 总尝试次数（含首次）。
	Attempts int `json:"attempts"`
	// StreamTotalMillis 流式请求总超时，单位毫秒。
	StreamTotalMillis int `json:"streamTotalMillis"`
	// FirstByteSeconds 首次响应超时，单位秒。
	FirstByteSeconds int `json:"firstByteSeconds"`
	// RequestSeconds 非流式请求超时，单位秒。
	RequestSeconds int `json:"requestSeconds"`
	// KeyMultiplier 新增 Key 的倍率。
	KeyMultiplier float64 `json:"keyMultiplier"`
	// KeyCacheMinutes 新增 Key 的缓存 TTL，单位分钟。
	KeyCacheMinutes int `json:"keyCacheMinutes"`
	// KeyProbeMinutes 新增 Key 的熔断探测间隔上限，单位分钟。
	KeyProbeMinutes int `json:"keyProbeMinutes"`
}

// ProviderSnapshot 是供应商页一次读取的内容。
type ProviderSnapshot struct {
	// Providers 供应商概览：启用的在前，其次按创建顺序；没有供应商时为空数组。
	Providers []ProviderOverview `json:"providers"`
	// Defaults 表单使用的默认值。
	Defaults ProviderDefaults `json:"defaults"`
}

// ProviderKey 是一把上游 Key 的展示信息；密钥明文不随它返回。
type ProviderKey struct {
	// ID 稳定身份。
	ID string `json:"id"`
	// ProviderID 所属供应商。
	ProviderID string `json:"providerId"`
	// Name 名称，同一供应商内可以重名。
	Name string `json:"name"`
	// Note 备注。
	Note string `json:"note"`
	// Enabled 是否启用。
	Enabled bool `json:"enabled"`
	// HasSecret 是否已设置密钥。
	HasSecret bool `json:"hasSecret"`
	// SecretMask 密钥掩码；未设置密钥时为空字符串。
	SecretMask string `json:"secretMask"`
	// Multiplier 倍率；nil 表示未知，0 是有效值。
	Multiplier *float64 `json:"multiplier"`
	// Priority 优先级，数值越小越优先。
	Priority int `json:"priority"`
	// RPMLimit 每分钟请求数上限，1～10000；0 表示不限制。
	RPMLimit int `json:"rpmLimit"`
	// ConcurrencyLimit 并发请求上限；0 表示不限制。
	ConcurrencyLimit int `json:"concurrencyLimit"`
	// CacheMinutes 缓存 TTL，单位分钟，0～60；0 表示禁用。
	CacheMinutes int `json:"cacheMinutes"`
	// ProbeMinutes 熔断探测间隔上限，单位分钟，0～32。
	ProbeMinutes int `json:"probeMinutes"`
	// Proxy 出口选择，默认跟随所属供应商。
	Proxy ProxyChoice `json:"proxy"`
	// AllModels 是否允许该供应商的全部模型。
	AllModels bool `json:"allModels"`
	// HealthScore 健康分，0～10。
	HealthScore float64 `json:"healthScore"`
}

// ProviderKeyDraft 是新增或编辑上游 Key 时可以提交的字段；启停和模型权限通过单独的操作修改。
type ProviderKeyDraft struct {
	// Name 名称，去掉首尾空格后为 1～100 个字符。
	Name string `json:"name"`
	// Secret 密钥明文。新增时为空表示暂不设置，编辑时为空表示不修改原密钥。
	Secret string `json:"secret"`
	// Note 备注。
	Note string `json:"note"`
	// Multiplier 手动倍率，0～100，0 是有效值；必须填写，缺失不会被当作 0。
	Multiplier *float64 `json:"multiplier"`
	// Priority 优先级，非负整数，数值越小越优先。
	Priority int `json:"priority"`
	// RPMLimit 每分钟请求数上限，1～10000；0 表示不限制。
	RPMLimit int `json:"rpmLimit"`
	// ConcurrencyLimit 并发请求上限，非负整数；0 表示不限制。
	ConcurrencyLimit int `json:"concurrencyLimit"`
	// CacheMinutes 缓存 TTL，单位分钟，0～60；0 表示禁用。
	CacheMinutes int `json:"cacheMinutes"`
	// ProbeMinutes 熔断探测间隔上限，单位分钟，0～32。
	ProbeMinutes int `json:"probeMinutes"`
	// Proxy 出口选择。
	Proxy ProxyChoice `json:"proxy"`
}
