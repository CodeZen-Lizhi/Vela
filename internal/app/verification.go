package app

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/verification"
)

// verificationManager 返回宿主创建的验证模块，预览模式不伪造成功。
func (s *VelaService) verificationManager() (*verification.Manager, error) {
	if s.verification == nil {
		return nil, errors.New("模型验证模块尚未初始化")
	}
	return s.verification, nil
}

// Verification 读取本地计划、最近结果和进度，不触发采样或加载参考库。
func (s *VelaService) Verification(ctx context.Context) (verification.VerificationSnapshot, error) {
	manager, err := s.verificationManager()
	if err != nil {
		return verification.VerificationSnapshot{}, err
	}
	return manager.Snapshot(ctx)
}

// SaveVerificationPlan 原子保存供应商唯一计划，开启后等待周期，不立即执行。
func (s *VelaService) SaveVerificationPlan(ctx context.Context, plan verification.VerificationPlan) error {
	manager, err := s.verificationManager()
	if err != nil {
		return err
	}
	return manager.SavePlan(ctx, plan)
}

// SetVerificationPlanEnabled 切换自动检测，手动检测仍可使用。
func (s *VelaService) SetVerificationPlanEnabled(ctx context.Context, providerID string, enabled bool) error {
	manager, err := s.verificationManager()
	if err != nil {
		return err
	}
	return manager.SetEnabled(ctx, providerID, enabled)
}

// StartVerification 发起计划或报告固定目标复测，reportID为空时使用计划范围。
func (s *VelaService) StartVerification(ctx context.Context, providerID, reportID string) error {
	manager, err := s.verificationManager()
	if err != nil {
		return err
	}
	return manager.Start(ctx, providerID, reportID)
}

// StopVerification 取消当前轮次，在途消耗不可撤回。
func (s *VelaService) StopVerification() error {
	manager, err := s.verificationManager()
	if err != nil {
		return err
	}
	manager.Stop()
	return nil
}

// VerificationReferenceModels 供计划编辑器按需选择内置参考模型，不发出采样请求。
func (s *VelaService) VerificationReferenceModels() ([]verification.ReferenceModel, error) {
	manager, err := s.verificationManager()
	if err != nil {
		return nil, err
	}
	return manager.ReferenceModels()
}
