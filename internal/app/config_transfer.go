package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
)

// configurationBundle 只允许搬运资源配置；本机历史、统计、偏好、网关访问密钥和主密钥均无导入入口。
type configurationBundle struct {
	// Models 包含模型身份、来源与完整本地价格；Providers 包含供应商从属配置及明文凭据，仅在加密边界内使用。
	Models    []models.Model          `json:"models"`
	Providers providers.Configuration `json:"providers"`
	// Scheduling 包含全局策略与来源供应商顺序，运行健康和亲和状态不跨机迁移。
	Scheduling scheduling.SchedulingPolicy `json:"scheduling"`
}

// ConfigurationPreview 只向页面返回资源数量，不暴露解密后的认证信息。
type ConfigurationPreview struct {
	// Models、Providers、Keys、Mappings 分别为包内模型、供应商、Key 和请求名映射数量，不是实际覆盖数量。
	Models    int `json:"models"`
	Providers int `json:"providers"`
	Keys      int `json:"keys"`
	Mappings  int `json:"mappings"`
}

// configurationStores 检查搬运依赖是否就绪，绑定生成等无数据库场景不能伪造空包。
func (s *VelaService) configurationStores() error {
	if s.models == nil || s.providers == nil || s.scheduling == nil {
		return errors.New("配置数据库尚未初始化")
	}
	return nil
}

// ExportConfiguration 读取同一数据库快照并直接加密，页面只收到可跨机解密的配置包。
func (s *VelaService) ExportConfiguration(ctx context.Context, password string) (string, error) {
	if err := configurationPassword(password); err != nil {
		return "", err
	}
	if err := s.configurationStores(); err != nil {
		return "", err
	}
	tx, err := s.models.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var bundle configurationBundle
	if bundle.Models, err = s.models.ExportConfiguration(ctx, tx); err != nil {
		return "", err
	}
	if bundle.Providers, err = s.providers.ExportConfiguration(ctx, tx); err != nil {
		return "", errors.New("读取供应商或认证信息失败，未导出文件；请检查本机凭据是否完整")
	}
	if bundle.Scheduling, err = s.scheduling.ExportConfiguration(ctx, tx); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if err = bundle.validate(); err != nil {
		return "", fmt.Errorf("当前配置无法导出: %w", err)
	}
	plain, err := json.Marshal(bundle)
	if err != nil {
		return "", errors.New("配置编码失败，未生成文件")
	}
	defer clear(plain)
	return encryptConfiguration(plain, password)
}

// PreviewConfiguration 解密并校验整包结构、规则和关联，仅返回数量，不写入任何数据或凭据。
func (s *VelaService) PreviewConfiguration(content, password string) (ConfigurationPreview, error) {
	bundle, err := parseConfiguration(content, password)
	if err != nil {
		return ConfigurationPreview{}, err
	}
	return bundle.preview(), nil
}

// ImportConfiguration 在确认时重新校验包，原子合并模型、供应商、认证、映射和调度；失败全部回滚。
// mode 只能为 overwrite 或 preserve，两种模式均不删除本机独有配置或触碰请求记录、报告及累计统计。
func (s *VelaService) ImportConfiguration(ctx context.Context, content, password, mode string) (ConfigurationPreview, error) {
	if mode != "overwrite" && mode != "preserve" {
		return ConfigurationPreview{}, errors.New("请选择覆盖相同配置或保留原有、仅新增")
	}
	if err := s.configurationStores(); err != nil {
		return ConfigurationPreview{}, err
	}
	bundle, err := parseConfiguration(content, password)
	if err != nil {
		return ConfigurationPreview{}, err
	}
	overwrite := mode == "overwrite"
	err = s.models.WithConfigurationLock(func() error {
		return s.providers.ConfigurationTransaction(ctx, func(tx *sql.Tx) error {
			previous, err := s.scheduling.ExportConfiguration(ctx, tx)
			if err != nil {
				return err
			}
			modelIDs, err := s.models.ImportConfiguration(ctx, tx, bundle.Models, overwrite)
			if err != nil {
				return err
			}
			providerIDs, err := s.providers.ImportConfiguration(ctx, tx, bundle.Providers, modelIDs, overwrite)
			if err != nil {
				return err
			}
			return s.scheduling.ImportConfiguration(ctx, tx, bundle.Scheduling, providerIDs, previous, overwrite)
		})
	})
	if err != nil {
		return ConfigurationPreview{}, fmt.Errorf("导入失败，原有配置与使用记录已保留: %w", err)
	}
	return bundle.preview(), nil
}

// parseConfiguration 只接收当前版本资源包，拒绝混入历史、统计等非资源字段和不完整结构。
func parseConfiguration(content, password string) (configurationBundle, error) {
	var bundle configurationBundle
	plain, err := decryptConfiguration(content, password)
	if err != nil {
		return bundle, err
	}
	defer clear(plain)
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&bundle); err != nil || !json.Valid(plain) {
		return configurationBundle{}, errors.New("配置包内容不合法，仅支持完整的 Vela 资源配置")
	}
	if bundle.Models == nil || bundle.Providers.Providers == nil || bundle.Scheduling.ProviderOrder == nil {
		return configurationBundle{}, errors.New("配置包缺少模型、供应商或调度配置")
	}
	if err = bundle.validate(); err != nil {
		return configurationBundle{}, err
	}
	return bundle, nil
}

// validate 复用领域校验并确保调度完整引用包内供应商，不允许文件制造悬空关联或重复身份。
func (b configurationBundle) validate() error {
	if err := models.ValidateConfiguration(b.Models); err != nil {
		return err
	}
	modelIDs := map[string]bool{}
	for _, m := range b.Models {
		modelIDs[m.ID] = true
	}
	if err := providers.ValidateConfiguration(b.Providers, modelIDs); err != nil {
		return err
	}
	if err := b.Scheduling.Strategy.Validate(); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, p := range b.Providers.Providers {
		ids[p.Provider.ID] = true
	}
	if len(b.Scheduling.ProviderOrder) != len(ids) {
		return errors.New("调度顺序必须完整包含配置包内的供应商")
	}
	for _, id := range b.Scheduling.ProviderOrder {
		if !ids[id] {
			return errors.New("调度顺序包含重复或不存在的供应商")
		}
		delete(ids, id)
	}
	return nil
}

// preview 汇总资源数量；仅数量越过后端秘密边界用于确认导入范围。
func (b configurationBundle) preview() ConfigurationPreview {
	p := ConfigurationPreview{Models: len(b.Models), Providers: len(b.Providers.Providers)}
	for _, provider := range b.Providers.Providers {
		p.Keys += len(provider.Keys)
		for _, m := range provider.Models {
			p.Mappings += len(m.Mappings)
		}
	}
	return p
}
