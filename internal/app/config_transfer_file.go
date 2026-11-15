package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
)

// ConfigurationFile 是用户从系统对话框选中的加密文件；取消时两个字段均为空，不向页面暴露本机绝对路径。
type ConfigurationFile struct {
	// Name 为展示用文件名；Content 为有体积上限的加密 JSON，不包含明文认证信息。
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ExportConfigurationFile 生成密码加密配置并通过原生对话框保存；取消返回 false，写入失败保留已有目标文件。
func (s *VelaService) ExportConfigurationFile(ctx context.Context, password string) (bool, error) {
	content, err := s.ExportConfiguration(ctx, password)
	if err != nil {
		return false, err
	}
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title: "导出配置", ButtonLabel: "导出", DefaultPath: "vela-config-" + time.Now().Format("20060102") + ".json",
		Filters: []mygo.FileFilter{{Name: "Vela 加密配置", Extensions: []string{"json"}}},
	})
	if err != nil || path == "" {
		return false, err
	}
	if err = writeConfigurationFile(path, content); err != nil {
		return false, err
	}
	return true, nil
}

// ReadConfigurationFile 通过原生对话框选择文件，仅读取有界的普通文件；取消不影响现有配置。
func (s *VelaService) ReadConfigurationFile() (ConfigurationFile, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: "导入配置", ButtonLabel: "选择配置文件",
		Filters: []mygo.FileFilter{{Name: "Vela 加密配置", Extensions: []string{"json"}}},
	})
	if err != nil || len(paths) == 0 {
		return ConfigurationFile{}, err
	}
	return readConfigurationFile(paths[0])
}

// readConfigurationFile 限制输入文件大小，读取期间增长的文件也不能突破 5 MB 上限。
func readConfigurationFile(path string) (ConfigurationFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return ConfigurationFile{}, errors.New("无法打开所选配置文件，请检查文件是否存在及读取权限")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ConfigurationFile{}, errors.New("请选择普通的 Vela 配置文件")
	}
	data, err := io.ReadAll(io.LimitReader(file, configurationLimit+1))
	if err != nil {
		return ConfigurationFile{}, errors.New("读取配置文件失败")
	}
	if len(data) > configurationLimit {
		return ConfigurationFile{}, errors.New("配置文件超过 5 MB")
	}
	return ConfigurationFile{Name: filepath.Base(path), Content: string(data)}, nil
}

// writeConfigurationFile 同目录写入受限权限临时文件并原子替换，任何写入或同步失败都不截断原文件。
func writeConfigurationFile(path, content string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".vela-config-*")
	if err != nil {
		return errors.New("无法创建导出文件，请检查保存目录权限")
	}
	defer os.Remove(file.Name()) // 原子替换成功后临时路径已不存在。
	_, writeErr := file.WriteString(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("写入配置文件失败，原文件已保留: %w", writeErr)
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return errors.New("保存配置文件失败，原文件已保留，请检查文件权限")
	}
	return nil
}
