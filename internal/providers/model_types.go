package providers

// ProviderModel 是已关联统一模型及其供应商侧配置；ModelID 指本地稳定身份，ModelName 才是真实默认请求名。
type ProviderModel struct {
	// ModelID 统一模型的本地稳定身份，用于关联、权限及映射写入。
	ModelID string `json:"modelId"`
	// ModelName 统一模型的调用名称，未设置映射时使用。
	ModelName string `json:"modelName"`
	// Name 模型显示名称，列表按它与稳定身份排序。
	Name string `json:"name"`
	// Kind 统一模型类型：chat、image、video 或 vector。
	Kind string `json:"kind"`
	// Enabled 供应商是否提供此模型；停用保留权限及映射。
	Enabled bool `json:"enabled"`
	// Mappings 已保存的有序映射，空数组表示默认请求名。
	Mappings []ProviderModelMapping `json:"mappings"`
}

// ProviderModelsDraft 原子替换供应商的完整关联集合；保留项的启停、映射及权限不变，不为新增关联配置 Key 权限。
type ProviderModelsDraft struct {
	// ModelIDs 完整目标关联集合，使用统一模型的本地稳定身份。
	ModelIDs []string `json:"modelIds"`
}

// ProviderModelPermissions 将全部模式与指定零个独立保留；主动不选任何项保存时调用方应传 AllModels=true。
type ProviderModelPermissions struct {
	// AllModels 为 true 时自动覆盖当前和以后关联的模型，此时 ModelIDs 必须为空。
	AllModels bool `json:"allModels"`
	// ModelIDs 指定权限的统一模型稳定身份；AllModels=false 且为空表示无模型权限。
	ModelIDs []string `json:"modelIds"`
}

// ProviderModelMapping 是模型级请求名及优先级，不含 Key 覆盖或 API 格式。
type ProviderModelMapping struct {
	// RequestName 去首尾空白的上游请求名，同模型内必须唯一。
	RequestName string `json:"requestName"`
	// Priority 正整数，越小越优先；传零时按列表位置生成 1..N，保留旧版同级排序兼容。
	Priority int `json:"priority"`
}

// ProviderModelMappingsDraft 是工作台中一个模型的完整映射草稿。
type ProviderModelMappingsDraft struct {
	// ModelID 已关联统一模型的本地稳定身份。
	ModelID string `json:"modelId"`
	// Mappings 完整目标映射集合，空数组清除映射并恢复默认请求名。
	Mappings []ProviderModelMapping `json:"mappings"`
}
