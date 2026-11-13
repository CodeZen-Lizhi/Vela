package main

import (
	"context"
	"log"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/egoist/mygo"
)

// 主窗口、菜单栏图标与当前生效的常规设置；启动后只在主线程读写。
var (
	mainWindow    *mygo.Window
	appTray       *mygo.Tray
	general       = settings.DefaultGeneral()
	gatewayServer *gateway.Server
)

// quitting 区分真正退出与关闭到后台，仅在主线程读写。
var quitting bool

// themeSource 把保存的浅色、深色或跟随系统选项映射为宿主外观，页面通过 prefers-color-scheme 跟随。
func themeSource(appearance string) mygo.ThemeSource {
	if appearance == "system" {
		return mygo.ThemeSystem
	}
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

// createMainWindow 首次按当前屏幕工作区居中打开，尺寸不超过 1200×800 逻辑像素及工作区的 90%；后续由 MyGo 恢复窗口状态。
// 关闭到后台时保留 WebView，使当前页面、筛选与草稿不丢失。
func createMainWindow() *mygo.Window {
	workArea := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint()).WorkArea
	width := min(1200, workArea.Width*9/10)
	height := min(800, workArea.Height*9/10)
	window := mygo.NewWindow(mygo.WindowOptions{
		Title:           "Vela",
		X:               workArea.X + (workArea.Width-width)/2,
		Y:               workArea.Y + (workArea.Height-height)/2,
		Width:           width,
		Height:          height,
		MinWidth:        min(820, width),
		MinHeight:       min(600, height),
		BackgroundColor: "light-dark(#f4f6f9, #10151c)",
		TitleBarStyle:   mygo.TitleBarHidden,
		// 系统按钮组向左上收紧，侧栏通过宿主提供的按钮占位保留右侧间距。
		TrafficLightPosition: &mygo.Point{X: 8, Y: 8},
		StateKey:             "main",
		URL:                  "/",
	})
	window.OnClose(func(event *mygo.CloseEvent) {
		if quitting || general.CloseAction == "quit" {
			return
		}
		event.PreventDefault()
		window.Hide()
		lastWindowClosed()
	})
	return window
}

// openMainWindow 恢复 Dock 图标并把主窗口带回前台；窗口已关闭时重新创建。
func openMainWindow() {
	mygo.App.Dock.Show()
	if mainWindow == nil || mainWindow.IsDestroyed() {
		mainWindow = createMainWindow()
	}
	mygo.App.Show()
	if mainWindow.IsMinimized() {
		mainWindow.Restore()
	}
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

// syncTray 按设置创建或移除菜单栏图标；左键打开主窗口，右键显示操作菜单。
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
	})
	if err != nil {
		log.Printf("创建菜单栏图标失败: %v", err)
		return
	}
	tray.OnClick(openMainWindow)
	tray.OnRightClick(func() {
		// macOS 常驻菜单会截获点击；仅在右键弹出期间挂载，关闭后恢复左键事件。
		tray.SetMenu(gatewayMenu())
		defer tray.SetMenu(nil)
		tray.PopUpMenu()
	})
	appTray = tray
}

// gatewayMenu 每次右键时按真实监听状态构造菜单，确保暂停后再次打开即显示“恢复网关”。
func gatewayMenu() *mygo.Menu {
	pauseLabel := "暂停网关"
	pauseDisabled := gatewayServer == nil || !gatewayServer.Configured()
	if gatewayServer != nil && gatewayServer.Paused() {
		pauseLabel = "恢复网关"
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "打开 Vela", Click: func(*mygo.MenuItem, *mygo.Window) { openMainWindow() }},
		mygo.Separator(),
		{Label: pauseLabel, Disabled: pauseDisabled, Click: func(*mygo.MenuItem, *mygo.Window) { toggleGateway() }},
		mygo.Separator(),
		{Label: "退出 Vela", Role: mygo.RoleQuit},
	})
}

// toggleGateway 按当前菜单文字暂停或恢复真实监听；失败保留原状态原因供日志定位。
func toggleGateway() {
	if gatewayServer == nil || !gatewayServer.Configured() {
		return
	}
	var err error
	if gatewayServer.Paused() {
		err = gatewayServer.Resume(context.Background())
	} else {
		err = gatewayServer.Pause(context.Background())
	}
	if err != nil {
		log.Printf("切换本机网关状态失败: %v", err)
	}
}

// refreshOnVisible 由入口装配全局静默刷新；主线程只发起异步工作，不等待网络。
var refreshOnVisible func()

// watchWindowVisibility 观察原生窗口从不可见到可见的边沿，统一涵盖启动、菜单栏、Dock、隐藏及最小化恢复。
// 只读取宿主窗口状态，不因普通前后台焦点切换或页面导航触发查询；随应用退出释放计时器。
func watchWindowVisibility(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		wasVisible := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mygo.RunOnMain(func() {
					if ctx.Err() != nil {
						return
					}
					visible := mainWindow != nil && !mainWindow.IsDestroyed() && mainWindow.IsVisible() && !mainWindow.IsMinimized() && !mygo.App.IsHidden()
					if visible && !wasVisible && refreshOnVisible != nil {
						refreshOnVisible()
					}
					wasVisible = visible
				})
			}
		}
	}()
}
