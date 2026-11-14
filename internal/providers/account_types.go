package providers

import "time"

// QueryConfig 定义账户连接；秘密字段读取只返回 ***，保存空串或 *** 均保留原值。
type QueryConfig struct {
	// Mode 接入方式：sub2api、newapi、usage_api、custom；空表示未配置。
	Mode string `json:"mode"`
	// BaseURL 内置站点地址；空时保存供应商地址。
	BaseURL string `json:"baseUrl"`
	// RefreshToken、AccessToken、APIKey 为加密认证信息，读取只返回掩码。
	RefreshToken string `json:"refreshToken"`
	AccessToken  string `json:"accessToken"`
	APIKey       string `json:"apiKey"`
	// UserID 为 New API 用户标识。
	UserID string `json:"userId"`
	// QuotaDivisor 为 New API 正数额度除数，0 使用 500000。
	QuotaDivisor float64 `json:"quotaDivisor"`
	// Method、URL 是自定义 GET/POST 请求；URL 查询参数读取时逐值掩码，留空/掩码保留。
	Method string `json:"method"`
	URL    string `json:"url"`
	// Headers、Body 是加密保存的请求头 JSON 与 POST JSON；读取只返回掩码。
	Headers string `json:"headers"`
	Body    string `json:"body"`
	// BalancePath 是自定义余额点路径，Currency 为原币种，默认 USD。
	BalancePath string `json:"balancePath"`
	Currency    string `json:"currency"`
}

// QueryStatus 保存单个字段最近查询状态及最后成功时间；失败不清除旧值。
type QueryStatus struct {
	// State 为 unknown、success 或 failed；Error 为不含凭据的失败原因。
	State string `json:"state"`
	Error string `json:"error"`
	// LastSuccess 为最后成功时间，零值表示从未成功。
	LastSuccess time.Time `json:"lastSuccess"`
}

// AccountBalance 保留原币种金额及独立上游额度；nil 表示未知，0 有效。
type AccountBalance struct {
	// Amount、Cash、Points、Used 分别为余额、现金、积分、上游已用。
	Amount *float64 `json:"amount"`
	Cash   *float64 `json:"cash"`
	Points *float64 `json:"points"`
	Used   *float64 `json:"used"`
	// Currency 原币种；Subscription 为不包含认证的上游订阅对象 JSON。
	Currency     string `json:"currency"`
	Subscription string `json:"subscription"`
	// Status、SubscriptionStatus、Checkin 分别记录余额、订阅与签到结果。
	Status             QueryStatus `json:"status"`
	SubscriptionStatus QueryStatus `json:"subscriptionStatus"`
	Checkin            QueryStatus `json:"checkin"`
}

// ProviderAccount 是余额连接与已保存的查询结果，不包含任何明文凭据。
type ProviderAccount struct {
	// ProviderID 为所属供应商，Config 为脱敏连接。
	ProviderID string      `json:"providerId"`
	Config     QueryConfig `json:"config"`
	// MonthlyQuota 是本地月配额美元金额，0 不限；Checkin 仅 New API 余额查询使用。
	MonthlyQuota float64 `json:"monthlyQuota"`
	Checkin      bool    `json:"checkin"`
	// Balance 为最近结果，失败保留旧值。
	Balance AccountBalance `json:"balance"`
}

// ProviderAccountDraft 只允许修改账户配置，不接收客户端余额结果。
type ProviderAccountDraft struct {
	// ValidationID 引用当前草稿的临时结果；仅成功验证可应用余额，空时只保存配置。
	ValidationID string `json:"validationId"`
	// Config、MonthlyQuota、Checkin 与 ProviderAccount 同名字段含义一致。
	Config       QueryConfig `json:"config"`
	MonthlyQuota float64     `json:"monthlyQuota"`
	Checkin      bool        `json:"checkin"`
}

// ProviderKeyQuery 保存每把 Key 的连接选择、独立来源和同步状态。
type ProviderKeyQuery struct {
	// KeyID 为本地 Key；ReuseProvider 默认 true，false 使用独立 Config。
	KeyID         string      `json:"keyId"`
	ReuseProvider bool        `json:"reuseProvider"`
	Config        QueryConfig `json:"config"`
	// NameSource 为 manual、upstream_key、upstream_group；MultiplierSource 为 manual、upstream。
	NameSource       string `json:"nameSource"`
	MultiplierSource string `json:"multiplierSource"`
	// NamePath、GroupPath、MultiplierPath、KeyPath 为自定义对象点路径；复用时 KeyPath 必须精确匹配完整密钥。
	NamePath       string `json:"namePath"`
	GroupPath      string `json:"groupPath"`
	MultiplierPath string `json:"multiplierPath"`
	KeyPath        string `json:"keyPath"`
	// GroupName 为只读上游分组，NameStatus 与 MultiplierStatus 独立记录字段状态。
	GroupName        string      `json:"groupName"`
	NameStatus       QueryStatus `json:"nameStatus"`
	MultiplierStatus QueryStatus `json:"multiplierStatus"`
}

// ProviderKeyQueryDraft 为可保存的 Key 查询配置，不允许伪造同步结果。
type ProviderKeyQueryDraft struct {
	// ReuseProvider、Config、来源与路径对应 ProviderKeyQuery；调用方须明确提交复用选择。
	ReuseProvider    bool        `json:"reuseProvider"`
	Config           QueryConfig `json:"config"`
	NameSource       string      `json:"nameSource"`
	MultiplierSource string      `json:"multiplierSource"`
	NamePath         string      `json:"namePath"`
	GroupPath        string      `json:"groupPath"`
	MultiplierPath   string      `json:"multiplierPath"`
	KeyPath          string      `json:"keyPath"`
}

// ProviderAccountRefresh 汇总本次主动查询之后的本地状态，单项失败通过各自 Status 返回。
type ProviderAccountRefresh struct {
	// Accounts 与 Keys 仅包含本次指定范围的对象。
	Accounts []ProviderAccount  `json:"accounts"`
	Keys     []ProviderKeyQuery `json:"keys"`
}

// ProviderAccountValidation 只返回余额验证结果及不透明引用，不向页面回传认证信息。
type ProviderAccountValidation struct {
	// ID 为临时查询引用；仅成功结果可应用余额，保存、取消或再次验证后失效。
	ID string `json:"id"`
	// Balance 为本次草稿查询结果，不代表已写入正式账户。
	Balance AccountBalance `json:"balance"`
}
