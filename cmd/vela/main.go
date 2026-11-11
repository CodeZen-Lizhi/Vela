package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/app"
	"github.com/CodeZen-Lizhi/vela/internal/buildinfo"
	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/egoist/mygo"
)

// trayIconPNG 是菜单栏使用的黑色透明模板图标，由 macOS 自动适配明暗外观。
//
//go:embed assets/tray.png
var trayIconPNG []byte

// main 组装桌面宿主、模型、设置与调度模块，生成绑定时不打开用户数据库。
func main() {
	// MyGo 生成客户端会执行入口，只注册类型即可，避免构建时创建用户数据。
	if os.Getenv("MYGO_GENERATE") != "" {
		mygo.Bind(app.NewVelaService(app.Options{Version: buildinfo.Version}))
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}

	dataDir := os.Getenv("VELA_DATA_DIR")
	isolated := dataDir != ""
	if !isolated {
		base, err := os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		dataDir = filepath.Join(base, "Vela")
	} else {
		// 隔离数据目录同时承载窗口状态，验收运行不改动日常窗口位置。
		mygo.App.SetPath(mygo.PathUserData, dataDir)
	}
	secrets, err := credentials.New(dataDir, legacyCredentials(isolated))
	if err != nil {
		log.Fatal(err)
	}
	store, err := models.Open(filepath.Join(dataDir, "vela.sqlite"), &http.Client{Timeout: 45 * time.Second}, "https://models.dev/api.json")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭模型数据库失败: %v", err)
		}
	}()
	if err := store.ProtectProxy(secrets); err != nil {
		log.Fatal(err)
	}
	preferences, err := settings.New(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	policies, err := scheduling.New(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	upstreams, err := providers.New(store.DB(), credentialEntry{store: secrets}, secrets)
	if err != nil {
		log.Fatal(err)
	}
	// 读取失败时以默认行为启动，设置页会显示同一错误，已保存的内容不被覆盖。
	if general, err = preferences.General(context.Background()); err != nil {
		log.Printf("读取常规设置失败，本次使用默认行为: %v", err)
	}
	mygo.Theme.SetSource(themeSource(general.Appearance))
	mygo.Bind(app.NewVelaService(app.Options{
		Version:      buildinfo.Version,
		Models:       store,
		Providers:    upstreams,
		Settings:     preferences,
		Scheduling:   policies,
		Credentials:  secrets,
		ApplyGeneral: applyGeneral,
	}))

	mygo.App.WhenReady(func() {
		mainWindow = createMainWindow()
		syncTray()
	})
	mygo.App.OnWindowAllClosed(lastWindowClosed)
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			openMainWindow()
		}
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
