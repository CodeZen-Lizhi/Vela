// Package records 保存真实请求、上下游诊断与独立长期累计，不依赖桌面运行时。
package records

import (
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"net/http"
	"time"
)

// Input 是执行入口提供的请求身份，不能从前端任意创建历史记录。
type Input struct {
	// Method、Path 为入口实际动作及无认证查询参数的路径；空值表示旧调用方未采集。
	Method string
	Path   string
	// ModelID 为统一模型稳定身份或真实统一调用名；来源为 business、modeltest 或 verification。
	ModelID string
	Source  string
	// Protocol 与 Type 分别表示 API 格式和业务类型；Stream 是客户端请求模式。
	Protocol string
	Type     string
	Stream   bool
	// Transport 是客户端实际传输：standard、stream 或 websocket。
	Transport string
	// ResourceQuery 标识入口已确认的纯资源查询或删除，不产生新的模型用量；仅供内部记录会话使用。
	ResourceQuery bool
	// ProviderID、KeyID 仅用于固定目标诊断，普通调度在实际尝试处填充。
	ProviderID string
	KeyID      string
	// Body 与 Headers 是客户端原始内容，仅以脱敏快照保留。
	Body    []byte
	Headers http.Header
}

// Target 是实际出站候选及请求开始时的倍率，不包含凭据。
type Target struct {
	// ProviderID、ProviderName、KeyID、KeyName 为稳定身份与当时名称。
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	KeyID        string `json:"keyId"`
	KeyName      string `json:"keyName"`
	// Multiplier 未知时不能当作一倍，零倍率有效。
	Multiplier *float64 `json:"multiplier"`
}

// Usage 使用已归一化且不重复包含缓存的输入量；nil 用量表示上游没有提供。
type Usage struct {
	// inputKnown、outputKnown 仅用于流片段合并，缺少必需字段不伪造零用量。
	inputKnown  bool
	outputKnown bool
	// Quality 是媒体计价质量，空值表示请求未明确提供。
	Quality string `json:"quality"`
	// Input、Output、CacheRead、CacheWrite、CacheWrite1h 的单位为 Token；Context 为完整输入上下文。
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cacheRead"`
	CacheWrite   int64 `json:"cacheWrite"`
	CacheWrite1h int64 `json:"cacheWrite1h"`
	Context      int64 `json:"context"`
	// ImageInput、ImageOutput、AudioInput、AudioOutput 是包含在输入/输出总量内的媒体 Token。
	ImageInput  int64 `json:"imageInput"`
	ImageOutput int64 `json:"imageOutput"`
	AudioInput  int64 `json:"audioInput"`
	AudioOutput int64 `json:"audioOutput"`
	// Quantity、Unit、Specification 为真实媒体数量、单位及规格，不伪造 Token。
	Quantity      float64 `json:"quantity"`
	Unit          string  `json:"unit"`
	Specification string  `json:"specification"`
}

// Charge 是按历史快照计算的一项估算金额。
type Charge struct {
	// Key、Quantity、Price、Amount 分别是维度、数量、美元单价和美元分项金额。
	Key      string  `json:"key"`
	Quantity float64 `json:"quantity"`
	Price    float64 `json:"price"`
	Amount   float64 `json:"amount"`
}

// ImageProgress 保留图片生成的实际流事件进度，不保存图片二进制；零计数仅用于已启用观察的尝试。
type ImageProgress struct {
	// Phase 为 connecting、streaming、completed 或 failed；完成必须由执行结果确认。
	Phase string `json:"phase"`
	// Frames 是收到的完整 SSE 数据事件数；PartialImages 是实际预览事件数。
	Frames        int64 `json:"frames"`
	PartialImages int64 `json:"partialImages"`
	// LastEvent、LastEventAt 是最后一个上游事件类型与 Unix 毫秒时刻。
	LastEvent   string `json:"lastEvent"`
	LastEventAt int64  `json:"lastEventAt"`
}

// Attempt 保留每次真正调用的目标、HTTP 状态及耗时；内容独立受保留期限控制。
type Attempt struct {
	// ImageProgress 仅在图片请求或真实图片生成事件出现时采集；nil 表示未采集，不伪造进度。
	ImageProgress *ImageProgress `json:"imageProgress"`
	// Protection 保留本次完成后触发的冷却或熔断，不读取当前 Key 状态覆盖历史。
	Protection
	Target
	// Number 为从一开始的实际网络尝试顺序；Path 不包含认证查询参数。
	Number int    `json:"number"`
	Path   string `json:"path"`
	// Polling 标记视频任务状态查询；BillingPending 表示尚未完成的任务响应，不是另一笔消耗。
	Polling        bool `json:"polling"`
	BillingPending bool `json:"billingPending"`
	// EndedAt 为网络尝试收尾时刻（Unix 毫秒），零表示尚未收尾或旧记录未采集。
	EndedAt int64 `json:"endedAt"`
	// Status 为上游 HTTP 状态，零表示未收到响应；StartedAt、DurationMillis 为真实计时。
	Status         int   `json:"status"`
	StartedAt      int64 `json:"startedAt"`
	DurationMillis int64 `json:"durationMillis"`
	// FirstByteMillis 未收到正文时为 nil；Error 只包含传输或执行错误，原始错误正文见 Payload。
	FirstByteMillis *int64 `json:"firstByteMillis"`
	Error           string `json:"error"`
	// Usage、Cost、Charges 使用本次实际用量与倍率，未知价格不补零。
	Usage   *Usage   `json:"usage"`
	Cost    *float64 `json:"cost"`
	Charges []Charge `json:"charges"`
	// Payload 只在详情读取，列表始终不加载正文。
	Payload *Payload `json:"payload"`
}

// Payload 是脱敏后的头与正文；正文过期后保留明确状态。
type Payload struct {
	// RequestHeaders、ResponseHeaders 是 JSON 格式脱敏头。
	RequestHeaders  string `json:"requestHeaders"`
	ResponseHeaders string `json:"responseHeaders"`
	// RequestBody、ResponseBody 只保留文本与媒体元信息，最多各一 MiB。
	RequestBody  string `json:"requestBody"`
	ResponseBody string `json:"responseBody"`
	// BodyState 为 available、expired、unavailable 或 truncated。
	BodyState string `json:"bodyState"`
}

// Record 是单个逻辑请求事实；名字快照与价格不随资源改名或删除变化。
type Record struct {
	// Method、Path 为客户端请求入口快照；历史空值不推测为某个固定端点。
	Method string `json:"method"`
	Path   string `json:"path"`
	// CurrentProviderName、CurrentKeyName、CurrentModelName 为当前名称或已删除标记，仅用于详情。
	CurrentProviderName string `json:"currentProviderName"`
	CurrentKeyName      string `json:"currentKeyName"`
	CurrentModelName    string `json:"currentModelName"`
	Target
	// ID 为随机稳定身份；时间为 Unix 毫秒，EndedAt 为零表示进行中。
	ID        string `json:"id"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt"`
	// ModelID、Model、ModelName 分别是稳定身份、统一调用名与当时显示名。
	ModelID   string `json:"modelId"`
	Model     string `json:"model"`
	ModelName string `json:"modelName"`
	// UpstreamModel、Effort、Tier 为实际请求模型、思考强度与处理层级。
	UpstreamModel string `json:"upstreamModel"`
	Effort        string `json:"effort"`
	Tier          string `json:"tier"`
	// Source、Protocol、Type、Stream 区分来源、格式、业务类型和客户端传输方式。
	Source   string `json:"source"`
	Protocol string `json:"protocol"`
	Type     string `json:"type"`
	Stream   bool   `json:"stream"`
	// Transport 记录真实连接方式；旧记录为空时由 Stream 解释为 HTTP 标准或流式。
	Transport string `json:"transport"`
	// State 为 active、success、failed 或 local；Execution 为 direct、retry、transfer 或 both。
	State     string `json:"state"`
	Execution string `json:"execution"`
	// Error 为最终执行原因；CircuitSourceID 为已知熔断来源，不允许猜测。
	Error           string `json:"error"`
	CircuitSourceID string `json:"circuitSourceId"`
	// DurationMillis、FirstByteMillis、OutputRate 为端到端计时和每秒输出 Token。
	DurationMillis  int64    `json:"durationMillis"`
	FirstByteMillis *int64   `json:"firstByteMillis"`
	OutputRate      *float64 `json:"outputRate"`
	// Usage 与 Cost 只在真实有数据且可计算时填写；UnknownCosts 标记有用量但无法计价的尝试。
	Usage        *Usage   `json:"usage"`
	Cost         *float64 `json:"cost"`
	UnknownCosts int      `json:"unknownCosts"`
	// StandardCost 仅在列表读取时按历史价格和倍率一派生；进行中或不能完整计价时为 nil。
	StandardCost *float64 `json:"standardCost"`
	// Pricing 固定开始时完整模型规则；Charges 包含实际各尝试费用。
	Pricing models.Pricing `json:"pricing"`
	Charges []Charge       `json:"charges"`
	// Attempts 为实际出站尝试，Client 仅在详情中读取。
	Attempts []Attempt `json:"attempts"`
	// Candidates 是调度排序快照，只有 Attempts 代表实际出站。
	Candidates []Candidate `json:"candidates"`
	Client     *Payload    `json:"client"`
}

// Query 在数据库内组合筛选、分页；Before 以数据库插入顺序锚定阅读快照，防止新记录挤动后续页。
type Query struct {
	// Search 按空白分词，所有关键词同时满足；时间为 Unix 毫秒，零表示不限制。
	Search string `json:"search"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Before int64  `json:"before"`
	// ModelID、ProviderID、Protocol、Source 为单选，空值表示全部。
	ModelID    string `json:"modelId"`
	ProviderID string `json:"providerId"`
	Protocol   string `json:"protocol"`
	Source     string `json:"source"`
	// States、Types、Transports、Executions 在组内或、组间且；传输取 stream、standard 或 websocket。
	States     []string `json:"states"`
	Types      []string `json:"types"`
	Transports []string `json:"transports"`
	Executions []string `json:"executions"`
	// Page 从一开始；Size 只允许 10、20、50、100。
	Page int `json:"page"`
	Size int `json:"size"`
}

// Summary 是整个筛选集合的合计，未知数据独立计数而非补零。
type Summary struct {
	// Total、Active、Success、Failed、Local 为各状态请求数。
	Total   int64 `json:"total"`
	Active  int64 `json:"active"`
	Success int64 `json:"success"`
	Failed  int64 `json:"failed"`
	Local   int64 `json:"local"`
	// Tokens、InputContext、CacheRead、Cost 为已知量合计，费用为估算美元。
	Tokens       int64   `json:"tokens"`
	InputContext int64   `json:"inputContext"`
	CacheRead    int64   `json:"cacheRead"`
	Cost         float64 `json:"cost"`
	// UnknownUsage、UnknownCost 为结束但缺少实际用量或完整价格的请求数。
	UnknownUsage int64 `json:"unknownUsage"`
	UnknownCost  int64 `json:"unknownCost"`
	// AverageMillis 只计算已结束有效耗时；Timed 为分母。
	AverageMillis float64 `json:"averageMillis"`
	Timed         int64   `json:"timed"`
}

// Option 为历史记录中的稳定身份与标签，删除资源后仍可筛选。
type Option struct {
	// Value 是稳定身份或协议；Label 为可显示名称。
	Value string `json:"value"`
	Label string `json:"label"`
}

// Page 包含一次查询同一事务内的分页、摘要及筛选候选。
type Page struct {
	// Records 不含正文；Summary 覆盖整个匹配集合；Before 为数据库插入顺序快照。
	Records []Record `json:"records"`
	Summary Summary  `json:"summary"`
	Before  int64    `json:"before"`
	// Models、Providers、Protocols、Types 为保留期内历史选项，不局限当前页。
	Models    []Option `json:"models"`
	Providers []Option `json:"providers"`
	Protocols []Option `json:"protocols"`
	Types     []Option `json:"types"`
}

// CleanupPreview 固定截止时刻，确认时只清理此前预览过的已结束数据。
type CleanupPreview struct {
	// Before 为结束截止时间毫秒；Count 为将删除明细数。
	Before int64 `json:"before"`
	Count  int64 `json:"count"`
}

// Aggregate 是独立于明细保留的日维度累计。
type Aggregate struct {
	// Day、ProviderID、ModelID、Protocol 为固定统计时区的维度，名称在读取时解析。
	Day        string `json:"day"`
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	Protocol   string `json:"protocol"`
	// ProviderName、ModelName 是当前名称或删除前最后名称，不合并同名重建身份。
	ProviderName string `json:"providerName"`
	ModelName    string `json:"modelName"`
	// Requests、Tokens、Cost 为请求数、已知 Token 和估算美元；未知值独立累计。
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	UnknownUsage int64   `json:"unknownUsage"`
	UnknownCost  int64   `json:"unknownCost"`
}

// clockNow 集中真实时钟，测试通过 Store.now 覆盖到期边界。
func clockNow() time.Time { return time.Now() }
