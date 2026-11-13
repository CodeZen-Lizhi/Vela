package models

import "encoding/json"

// clonePricing 分离表单、缓存与模型的嵌套切片，避免价格草稿修改共享来源数据。
func clonePricing(pricing Pricing) Pricing {
	// Pricing 仅包含已校验的有限数值与可序列化值，序列化不会失败。
	data, _ := json.Marshal(pricing)
	var cloned Pricing
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
