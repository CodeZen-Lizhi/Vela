package main

import (
	_ "embed"
	"log"

	"github.com/CodeZen-Lizhi/vela/internal/app"
	"github.com/CodeZen-Lizhi/vela/internal/buildinfo"
	"github.com/egoist/mygo"
)

// trayIconPNG 是菜单栏使用的黑色透明模板图标，由 macOS 自动适配明暗外观。
//
//go:embed assets/tray.png
var trayIconPNG []byte

var (
	mainWindow *mygo.Window
	appTray    *mygo.Tray
)

// main 组装 MyGo 桌面宿主与最小应用服务。
func main() {
	service := app.NewVelaService(buildinfo.Version)
	mygo.Bind(service)

	mygo.App.WhenReady(func() {
		mainWindow = createMainWindow()
		installTray()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if appTray != nil {
			appTray.Destroy()
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// createMainWindow 创建 Vela 主窗口，供应用启动和菜单栏重新打开时复用。
func createMainWindow() *mygo.Window {
	return mygo.NewWindow(mygo.WindowOptions{
		Title:           "Vela",
		Width:           1180,
		Height:          760,
		MinWidth:        820,
		MinHeight:       600,
		BackgroundColor: "#f4f0e7",
		StateKey:        "main",
		URL:             "/",
	})
}

// showMainWindow 将现有主窗口带回前台；窗口已关闭时重新创建。
func showMainWindow(_ *mygo.MenuItem, _ *mygo.Window) {
	if mainWindow == nil || mainWindow.IsDestroyed() {
		mainWindow = createMainWindow()
	}
	mygo.App.Show()
	mainWindow.Show()
	mainWindow.Focus()
}

// installTray 创建菜单栏 V 图标，并提供打开主窗口和退出应用的入口。
func installTray() {
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "vela.status",
		Icon:           trayIconPNG,
		IconIsTemplate: true,
		ToolTip:        "Vela AI Gateway",
		Menu: mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开 Vela", Click: showMainWindow},
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
