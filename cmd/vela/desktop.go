package main

import (
	"log"

	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/egoist/mygo"
)

// 主窗口、菜单栏图标与当前生效的常规设置；启动后只在主线程读写。
var (
	mainWindow *mygo.Window
	appTray    *mygo.Tray
	general    = settings.DefaultGeneral()
)

// themeSource 把保存的外观映射为宿主外观，页面通过 prefers-color-scheme 跟随。
func themeSource(appearance string) mygo.ThemeSource {
	if appearance == "dark" {
		return mygo.ThemeDark
	}
	return mygo.ThemeLight
}

// applyGeneral 让保存后的常规设置立即生效；绑定方法运行在各自协程，这里切回主线程。
func applyGeneral(value settings.GeneralSettings) {
	mygo.RunOnMain(func() {
		general = value
		mygo.Theme.SetSource(themeSource(value.Appearance))
		syncTray()
	})
}

// createMainWindow 创建主窗口，隐藏系统标题底色并保留窗口按钮；底色随浅深外观切换。
func createMainWindow() *mygo.Window {
	return mygo.NewWindow(mygo.WindowOptions{
		Title:           "Vela",
		Width:           1180,
		Height:          760,
		MinWidth:        820,
		MinHeight:       600,
		BackgroundColor: "light-dark(#f4f6f9, #10151c)",
		TitleBarStyle:   mygo.TitleBarHiddenInset,
		StateKey:        "main",
		URL:             "/",
	})
}

// openMainWindow 恢复 Dock 图标并把主窗口带回前台；窗口已关闭时重新创建。
func openMainWindow() {
	mygo.App.Dock.Show()
	if mainWindow == nil || mainWindow.IsDestroyed() {
		mainWindow = createMainWindow()
	}
	mygo.App.Show()
	mainWindow.Show()
	mainWindow.Focus()
	mygo.App.Focus()
}

// lastWindowClosed 在最后一个窗口关闭后按设置退出，或继续运行并只保留所选后台入口。
func lastWindowClosed() {
	if general.CloseAction == "quit" {
		mygo.App.Quit()
		return
	}
	// 菜单栏图标不可用时保留 Dock 图标，始终留有重新打开的入口。
	if general.BackgroundEntry == "menu" && appTray != nil {
		mygo.App.Dock.Hide()
	}
}

// syncTray 按设置创建或移除菜单栏图标：仅在允许后台运行且入口包含菜单栏时显示。
func syncTray() {
	want := general.CloseAction == "background" && general.BackgroundEntry != "dock"
	if want == (appTray != nil) {
		return
	}
	if !want {
		appTray.Destroy()
		appTray = nil
		return
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "vela.status",
		Icon:           trayIconPNG,
		IconIsTemplate: true,
		ToolTip:        "Vela AI Gateway",
		Menu: mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开 Vela", Click: func(*mygo.MenuItem, *mygo.Window) { openMainWindow() }},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}),
	})
	if err != nil {
		log.Printf("创建菜单栏图标失败: %v", err)
		return
	}
	appTray = tray
}
