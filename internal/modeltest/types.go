// Package modeltest 提供独立于业务调度与指纹验证的主动模型连通诊断。
package modeltest

// Request 是一次主动诊断配置；ID 由页面生成，用于取消本次请求。
type Request struct {
	// firstByteSeconds 由供应商配置注入，页面不可覆盖首次响应超时秒数。
	firstByteSeconds int
	// ID 是本次测试唯一标识，重复运行须重新生成。
	ID string `json:"id"`
	// ProviderID、ModelID 固定供应商与已关联统一模型稳定身份。
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	// KeyID、RequestModel 为空时按映射优先级及 Key 顺序自动尝试。
	KeyID        string `json:"keyId"`
	RequestModel string `json:"requestModel"`
	// Protocol 取 chat、responses、anthropic、gemini、image、embedding、rerank 或 video。
	Protocol string `json:"protocol"`
	// Message 是默认请求使用的输入；RequestBody 非空时使用 JSON 对象覆盖默认正文。
	Message     string `json:"message"`
	RequestBody string `json:"requestBody"`
	// RequestHeaders 为本次自定义 JSON 请求头，不允许覆盖认证、Host 与 Cookie。
	RequestHeaders string `json:"requestHeaders"`
}

// KeyOption 是经过模型权限和密钥存在性筛选的诊断 Key，不含认证信息。
type KeyOption struct {
	// Value 是 Key 稳定身份；Label 为名称，停用对象可主动诊断。
	Value string `json:"value"`
	Label string `json:"label"`
}

// Options 是前端预选测试配置所需的当前配置快照。
type Options struct {
	// ModelName、Kind 来自统一模型；不从展示名称推测模型类型。
	ModelName string `json:"modelName"`
	Kind      string `json:"kind"`
	// Keys 仅列有权使用该统一模型且已设置密钥的当前供应商 Key。
	Keys []KeyOption `json:"keys"`
	// Mappings 是按优先级排列的已配置请求名；无映射时为默认模型名。
	Mappings []string `json:"mappings"`
}

// Attempt 保存一次固定映射与 Key 组合的执行结果及脱敏响应，不保存明文认证。
type Attempt struct {
	// KeyID、KeyName、RequestModel 是实际尝试身份。
	KeyID        string `json:"keyId"`
	KeyName      string `json:"keyName"`
	RequestModel string `json:"requestModel"`
	// State 取 success、failed、unused 或 cancelled；未使用组合不发请求。
	State string `json:"state"`
	// Status 是 HTTP 状态，未收到响应头时为零；DurationMillis 为本次耗时毫秒。
	Status         int   `json:"status"`
	DurationMillis int64 `json:"durationMillis"`
	// Error 为脱敏后的失败说明，成功或未使用时为空。
	Error string `json:"error"`
	// ResponseBody 是该次尝试截断并脱敏后的上游正文；未收到或旧记录未保存时为空。
	ResponseBody string `json:"responseBody"`
}

// Result 是一次诊断最终结果及脱敏快照，持久化最近一次已完成结果。
type Result struct {
	// ID、ProviderID、ModelID 固定本次诊断身份。
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	// Protocol 为本次实际选择的协议；State 取 success、failed 或 cancelled。
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	// CompletedAt 是 RFC3339Nano 时间；DurationMillis、FirstByteMillis 为真实总耗时与首响应耗时。
	CompletedAt     string `json:"completedAt"`
	DurationMillis  int64  `json:"durationMillis"`
	FirstByteMillis int64  `json:"firstByteMillis"`
	// Status 为最后尝试 HTTP 状态；Error 为最终失败原因。
	Status int    `json:"status"`
	Error  string `json:"error"`
	// RequestModel、KeyName 为最终实际尝试的目标身份。
	RequestModel string `json:"requestModel"`
	KeyName      string `json:"keyName"`
	// RequestBody、RequestHeaders、ResponseBody 是截断并脱敏的诊断快照。
	RequestBody    string `json:"requestBody"`
	RequestHeaders string `json:"requestHeaders"`
	ResponseBody   string `json:"responseBody"`
	// Attempts 保留实际顺序及首个成功后未使用的组合。
	Attempts []Attempt `json:"attempts"`
}
