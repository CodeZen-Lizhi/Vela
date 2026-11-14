package main

import (
	"fmt"
	"os"

	"github.com/CodeZen-Lizhi/vela/internal/desktopinstall"
	"github.com/egoist/mygo"
)

// runDiskImageInstaller 在单实例检查和数据初始化前拦截 DMG 启动，取消时不读取或改写用户数据。
func runDiskImageInstaller() (bool, error) {
	if mygo.IsDev() {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("定位应用路径: %w", err)
	}
	source, err := desktopinstall.SourceBundle(executable)
	if err != nil || source == "" {
		return false, err
	}
	return true, desktopinstall.Run(source, "/Applications/Vela.app")
}
