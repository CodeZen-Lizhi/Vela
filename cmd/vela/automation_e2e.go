//go:build e2e && unix

package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/egoist/mygo"
	// 自动化插件只在设置 MYGO_AUTOMATION 后监听。
	_ "github.com/egoist/mygo/plugins/automation"
)

// init 仅存在于带 e2e 标签的验收构建：收到 SIGUSR1 时记录桌面状态，SIGUSR2 恢复真实主窗口。
// 主窗口关闭后页面不可用，仍可据此从外部核对菜单栏图标、Dock 与窗口状态；正式构建不包含本文件。
func init() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for received := range signals {
			if received == syscall.SIGUSR2 {
				mygo.RunOnMain(func() {
					openMainWindow()
					mainWindow.SetBounds(mygo.Rectangle{X: 100, Y: 100, Width: 1200, Height: 800})
				})
			}
			mygo.RunOnMain(logDesktopState)
		}
	}()
}

// logDesktopState 输出当前生效的关闭行为、后台入口、菜单栏图标位置、Dock 策略与主窗口状态；只在主线程调用。
func logDesktopState() {
	window := "none"
	switch {
	case mainWindow == nil:
	case mainWindow.IsDestroyed():
		window = "closed"
	case mainWindow.IsVisible():
		window = "visible"
	default:
		window = "hidden"
	}
	tray := "none"
	if appTray != nil {
		tray = fmt.Sprintf("%+v", appTray.Bounds())
	}
	log.Printf("e2e desktop: close=%s entry=%s tray=%s dock=%s window=%s", general.CloseAction, general.BackgroundEntry, tray, mygo.App.ActivationPolicy(), window)
}
