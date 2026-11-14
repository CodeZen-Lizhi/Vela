package app

import (
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/updates"
)

// ApplicationUpdate 返回更新状态，生成绑定和浏览器预览不会启动更新器。
func (s *VelaService) ApplicationUpdate() updates.Status {
	if s.updates == nil {
		return updates.Status{CurrentVersion: s.version, Phase: "idle"}
	}
	return s.updates.Status()
}

// CheckApplicationUpdate 检查固定官方源，不接受前端提供下载地址。
func (s *VelaService) CheckApplicationUpdate() updates.Status {
	if s.updates == nil {
		return s.ApplicationUpdate()
	}
	return s.updates.Check()
}

// DownloadApplicationUpdate 下载并验证已检查版本，下载完成后等待用户确认重启。
func (s *VelaService) DownloadApplicationUpdate() error {
	if s.updates == nil {
		return errors.New("当前环境不支持应用更新")
	}
	return s.updates.Download()
}

// RestartApplicationUpdate 仅允许已校验完成的更新进入正常重启链路。
func (s *VelaService) RestartApplicationUpdate() error {
	if s.updates == nil {
		return errors.New("当前环境不支持应用更新")
	}
	return s.updates.Restart()
}
