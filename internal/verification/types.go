// Package verification 管理固定目标的模型指纹计划、采样与最近报告，不参与调度健康。
package verification

// VerificationLine 是一条固定模型、Key、协议和思考强度的绑定。
type VerificationLine struct {
	// ModelID、KeyID 是本地稳定身份；Protocol 取 chat_completions、responses、messages、gemini；Effort 取 auto、low、medium、high。
	ModelID  string `json:"modelId"`
	KeyID    string `json:"keyId"`
	Protocol string `json:"protocol"`
	Effort   string `json:"effort"`
}

// VerificationPlan 保存供应商唯一计划及定时锚点，时间均为 Unix 毫秒。
type VerificationPlan struct {
	// ProviderID 是所属供应商；Enabled 仅控制自动运行，Hours 取 1、6、12、24。
	ProviderID string `json:"providerId"`
	Enabled    bool   `json:"enabled"`
	Hours      int    `json:"hours"`
	// Lines 是显式绑定，不包含空白尾行；NextAt、LastAt 分别为下次定时和最近实际执行时间。
	Lines  []VerificationLine `json:"lines"`
	NextAt int64              `json:"nextAt"`
	LastAt int64              `json:"lastAt"`
}

// VerificationTarget 是不含凭据的线路快照，Reason 非空时不可执行。
type VerificationTarget struct {
	VerificationLine
	// ProviderID、ProviderName、KeyName、ModelName 是目标身份及显示名；RequestModel 和 URL 是实际出站快照。
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	KeyName      string `json:"keyName"`
	ModelName    string `json:"modelName"`
	RequestModel string `json:"requestModel"`
	URL          string `json:"url"`
	Reason       string `json:"reason"`
}

// VerificationCandidate 是上游真实候选，Probability 为空时不显示置信度。
type VerificationCandidate struct {
	// Model、Name、Family 是参考库身份、显示名和家族；Score 为上游排名分，Probability 为 0～1 的封闭集合置信度。
	Model       string   `json:"model"`
	Name        string   `json:"name"`
	Family      string   `json:"family"`
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability"`
}

// VerificationSample 保存最近一轮样本及解析诊断，不包含请求认证信息。
type VerificationSample struct {
	// State 是 complete、truncated、failed、cancelled；Text 是原始回答，Error 是脱敏失败原因。
	State string `json:"state"`
	Text  string `json:"text"`
	Error string `json:"error"`
	// Count、ExpectedCount 是有效数字及题目要求的数量。
	Count         int `json:"count"`
	ExpectedCount int `json:"expectedCount"`
}

// VerificationReport 是一个组合唯一的最近一轮报告，重新检测后替换。
type VerificationReport struct {
	// ID 是组合稳定身份；Target 是执行时快照；Time 为检测完成时间（Unix 毫秒）。
	ID     string             `json:"id"`
	Target VerificationTarget `json:"target"`
	Time   int64              `json:"time"`
	// Conclusion 为 match、difference、insufficient；State 为 complete、failed、cancelled；Reason 是真实诊断原因。
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	// AlgorithmVersion、DataVersion 提供追溯；Candidates、Samples 保存真实分析及原始回答。
	AlgorithmVersion string                  `json:"algorithmVersion"`
	DataVersion      string                  `json:"dataVersion"`
	Candidates       []VerificationCandidate `json:"candidates"`
	Samples          []VerificationSample    `json:"samples"`
}

// VerificationProgress 是全局单轮采样进度，完成后 Running=false。
type VerificationProgress struct {
	// Running 表示任务占用；Mode 为 plan 或 single；Target 为当前组合；Done、Total 按样本计。
	Running bool               `json:"running"`
	Mode    string             `json:"mode"`
	Target  VerificationTarget `json:"target"`
	Done    int                `json:"done"`
	Total   int                `json:"total"`
	// Error 是任务最终失败原因，不将后台错误吞掉。
	Error string `json:"error"`
}

// VerificationSnapshot 一次读取本地计划、结果、可选目标和任务进度，不发出网络请求。
type VerificationSnapshot struct {
	// Plans、Reports、Targets 均保证为空数组而非 null；Targets 提供供应商已关联模型与 Key 的可执行状态。
	Plans    []VerificationPlan   `json:"plans"`
	Reports  []VerificationReport `json:"reports"`
	Targets  []VerificationTarget `json:"targets"`
	Progress VerificationProgress `json:"progress"`
}
