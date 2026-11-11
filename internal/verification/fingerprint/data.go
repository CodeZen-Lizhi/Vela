package fingerprint

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// detectorSchema 固定上游评分器的二进制格式。
const detectorSchema = "shared-detector-v2"

// bundledBank 保存原始参考库；仅 LoadBundled 解码，不在包初始化时分配评分状态。
//
//go:embed data/unified_bank.json
var bundledBank []byte

// bundledDetector 保存与参考库同一提交的二进制评分数据。
//
//go:embed data/shared_detector.bin
var bundledDetector []byte

type bankModel struct {
	ID            string  `json:"id"`
	DisplayName   string  `json:"display_name"`
	Family        string  `json:"family"`
	FamilyName    string  `json:"family_name"`
	ResponseCount float64 `json:"response_count"`
}

// bank holds the fields scoring reads. bundle.json repeats them at its top level, so it parses as a bank too.
type bank struct {
	BuiltAt string      `json:"built_at"`
	Models  []bankModel `json:"models"`
}

func newDetector(b *bank, det *detector) (*Detector, error) {
	matches := b.Models != nil && det.schema == detectorSchema && b.BuiltAt == det.bankBuiltAt &&
		len(b.Models) == len(det.modelIDs) && len(det.responseCounts) == len(det.modelIDs)
	for i := 0; matches && i < len(b.Models); i++ {
		matches = b.Models[i].ID == det.modelIDs[i] && b.Models[i].ResponseCount == det.responseCounts[i]
	}
	if !matches {
		return nil, errors.New("The reference bank does not match its detector.")
	}
	return &Detector{bank: b, detector: det}, nil
}

// LoadBundled 按需解码内置离线数据，不访问网络或缓存；调用方在一次任务内复用返回值，结束后释放引用即可回收解码状态。
func LoadBundled() (*Detector, error) {
	var b bank
	if err := json.Unmarshal(bundledBank, &b); err != nil {
		return nil, fmt.Errorf("decode bundled reference bank: %w", err)
	}
	det, err := decodeDetector(bundledDetector)
	if err != nil {
		return nil, fmt.Errorf("decode bundled detector: %w", err)
	}
	return newDetector(&b, det)
}

// ReferenceModel 是内置参考库中的真实模型身份及采集量，不包含 Vela 业务映射。
type ReferenceModel struct {
	// ID、DisplayName 是上游稳定身份和显示名；Family、FamilyName 是上游家族身份和显示名。
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Family      string `json:"family"`
	FamilyName  string `json:"family_name"`
	// ResponseCount 是该模型在参考库中采集的响应数量。
	ResponseCount float64 `json:"response_count"`
}

// Models 返回参考模型元数据的独立副本，不触发重新加载；修改返回切片不会改变评分器。
func (d *Detector) Models() []ReferenceModel {
	models := make([]ReferenceModel, len(d.bank.Models))
	for i, m := range d.bank.Models {
		models[i] = ReferenceModel{ID: m.ID, DisplayName: m.DisplayName, Family: m.Family, FamilyName: m.FamilyName, ResponseCount: m.ResponseCount}
	}
	return models
}

// ReferenceModels 返回已加载评分器的参考模型，等同于 Models，不单独解码数据。
func (d *Detector) ReferenceModels() []ReferenceModel { return d.Models() }
