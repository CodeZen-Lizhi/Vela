package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/app"
	"github.com/CodeZen-Lizhi/vela/internal/buildinfo"
	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/gateway"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/CodeZen-Lizhi/vela/internal/updates"
	"github.com/CodeZen-Lizhi/vela/internal/verification"
	"github.com/egoist/mygo"
)

// trayIconPNG 是菜单栏使用的黑色透明模板图标，由 macOS 自动适配明暗外观。
//
//go:embed assets/tray.png
var trayIconPNG []byte

// windowVisible 通知当前页面从隐藏或最小化恢复，普通聚焦不触发。
var windowVisible = mygo.NewEvent[bool]("window:visible")

// localRefreshCompleted 通知已发布的总览后台结果。
var localRefreshCompleted = mygo.NewEvent[string]("local:refresh-completed")

// providerAccountsRefreshed 通知后台账户查询已结束，页面重读落库后的余额和倍率。
var providerAccountsRefreshed = mygo.NewEvent[bool]("provider:accounts-refreshed")

// providerKeyHealthChanged 通知页面重读指定 Key 的运行状态。
var providerKeyHealthChanged = mygo.NewEvent[string]("provider:key-health-changed")

// verificationProgress 将合并后的检测样本进度推送给已打开页面。
var verificationProgress = mygo.NewEvent[verification.VerificationProgress]("verification:progress")

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
	if handled, err := runDiskImageInstaller(); handled || err != nil {
		if err != nil {
			log.Printf("Vela 安装失败: %v", err)
			os.Exit(1)
		}
		return
	}
	if err := runDesktop(); err != nil {
		log.Printf("Vela 启动失败: %v", err)
		if !mygo.App.IsReady() {
			showStartupError(err)
		}
		os.Exit(1)
	}
}

// runDesktop 在打开数据库前取得数据目录独占权，并在所有业务资源关闭后释放。
func runDesktop() error {
	dataDir := os.Getenv("VELA_DATA_DIR")
	isolated := dataDir != ""
	if !isolated {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("定位应用数据目录: %w", err)
		}
		dataDir = filepath.Join(base, "Vela")
	} else {
		// 隔离数据目录同时承载窗口状态，验收运行不改动日常窗口位置。
		mygo.App.SetPath(mygo.PathUserData, dataDir)
	}
	// 普通重复启动交由宿主唤回窗口；开发版和安装版共用数据时仍由下方文件锁保护。
	if !isolated {
		mygo.App.OnSecondInstance(func([]string, string) {
			if mygo.App.IsReady() {
				openMainWindow()
			}
		})
		if !mygo.App.RequestSingleInstanceLock() {
			return nil
		}
	}
	lock, err := lockDataDirectory(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Close(); err != nil {
			log.Printf("释放数据目录锁失败: %v", err)
		}
	}()
	logFile, err := openDesktopLog(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		log.SetOutput(os.Stderr)
		if err := logFile.Close(); err != nil {
			log.Printf("关闭诊断日志失败: %v", err)
		}
	}()
	log.Printf("Vela %s 启动", buildinfo.Version)
	if err := startApplication(dataDir, isolated); err != nil {
		log.Printf("启动失败，数据目录 %s: %v", dataDir, err)
		return fmt.Errorf("%w\n\n数据目录：%s\n诊断日志：%s", err, dataDir, filepath.Join(dataDir, "vela.log"))
	}
	return nil
}

// startApplication 装配真实业务依赖；初始化失败会运行已有清理，再由入口显示原生错误。
func startApplication(dataDir string, isolated bool) error {
	secrets, err := credentials.New(dataDir, legacyCredentials(isolated))
	if err != nil {
		return fmt.Errorf("读取本机凭据: %w", err)
	}
	store, err := models.Open(filepath.Join(dataDir, "vela.sqlite"), &http.Client{Timeout: 45 * time.Second}, models.BasellmCatalogURL)
	if err != nil {
		return fmt.Errorf("打开模型数据库: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭模型数据库失败: %v", err)
		}
	}()
	if err := store.ProtectProxy(secrets); err != nil {
		return fmt.Errorf("读取代理配置: %w", err)
	}
	preferences, err := settings.New(store.DB())
	if err != nil {
		return fmt.Errorf("初始化设置: %w", err)
	}
	policies, err := scheduling.New(store.DB())
	if err != nil {
		return fmt.Errorf("初始化调度: %w", err)
	}
	upstreams, err := providers.New(store.DB(), credentialEntry{store: secrets}, secrets)
	if err != nil {
		return fmt.Errorf("初始化供应商: %w", err)
	}
	if err = upstreams.InitModels(); err != nil {
		return fmt.Errorf("初始化供应商模型: %w", err)
	}
	history, err := records.New(store.DB())
	if err != nil {
		return fmt.Errorf("初始化请求记录: %w", err)
	}
	historyContext, stopHistory := context.WithCancel(context.Background())
	historyDone := make(chan struct{})
	defer func() { stopHistory(); <-historyDone }()
	go func() {
		defer close(historyDone)
		if err := history.Run(historyContext); err != nil && historyContext.Err() == nil {
			log.Printf("请求记录到期清理停止: %v", err)
		}
	}()
	execution, err := app.NewSchedulingExecution(context.Background(), policies, upstreams, store, history)
	if err != nil {
		return fmt.Errorf("初始化请求执行: %w", err)
	}
	defer execution.Close()
	execution.Executor.Runtime.SetHealthChanged(func(keyID string) {
		if err := providerKeyHealthChanged.Broadcast(keyID); err != nil {
			log.Printf("推送 Key 健康变化失败: %v", err)
		}
	})
	gatewayServer = gateway.New(store, execution.Executor)
	applyGateway := func(ctx context.Context, desired settings.GatewaySettings) error {
		key, readErr := secrets.Get("gateway-access-key")
		if errors.Is(readErr, credentials.ErrNotFound) {
			return gateway.ErrAccessKeyRequired
		}
		if readErr != nil {
			return readErr
		}
		gatewayServer.SetAccessKey(string(key))
		return gatewayServer.Listen(ctx, desired.Port)
	}
	if gatewaySettings, readErr := preferences.Gateway(context.Background()); readErr != nil {
		log.Printf("读取网关监听设置失败，本机网关未启动: %v", readErr)
	} else if applyErr := applyGateway(context.Background(), gatewaySettings); applyErr != nil {
		log.Printf("本机网关未启动: %v", applyErr)
	}
	defer func() {
		if closeErr := gatewayServer.Close(context.Background()); closeErr != nil {
			log.Printf("关闭本机网关失败: %v", closeErr)
		}
	}()
	modelTests, err := app.NewModelTests(store, upstreams, execution)
	if err != nil {
		return fmt.Errorf("初始化模型测试: %w", err)
	}
	modelTests.Records = history
	defer modelTests.Close()
	accounts, err := providers.NewAccounts(upstreams, func(ctx context.Context) (bool, string, error) {
		proxy, readErr := store.GetProxy(ctx)
		return proxy.Enabled, proxy.URL, readErr
	})
	if err != nil {
		return fmt.Errorf("初始化账户查询: %w", err)
	}
	accounts.OnBackgroundRefresh = func() {
		if err := providerAccountsRefreshed.Broadcast(true); err != nil {
			log.Printf("推送账户查询完成事件失败: %v", err)
		}
	}
	queryContext, stopQueries := context.WithCancel(context.Background())
	// 定时和窗口恢复查询都须在关闭共用数据库前退出。
	var queryWork sync.WaitGroup
	defer func() { stopQueries(); queryWork.Wait() }()
	queryWork.Add(1)
	go func() {
		defer queryWork.Done()
		select {
		case <-queryContext.Done():
			return
		case <-time.After(1 * time.Second):
		}
		if err := accounts.Run(queryContext, preferences.AccountRefresh); err != nil && queryContext.Err() == nil {
			log.Printf("账户定时刷新停止: %v", err)
		}
	}()
	// 读取失败时以默认行为启动，设置页会显示同一错误，已保存的内容不被覆盖。
	if general, err = preferences.General(context.Background()); err != nil {
		log.Printf("读取常规设置失败，本次使用默认行为: %v", err)
	}
	store.ConfigureCatalogUpdates(general.ModelAutoUpdate, general.ModelUpdateMinutes)
	catalogContext, stopCatalog := context.WithCancel(context.Background())
	catalogDone := make(chan struct{})
	defer func() { stopCatalog(); <-catalogDone }()
	go func() {
		defer close(catalogDone)
		select {
		case <-catalogContext.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if runErr := store.RunCatalogUpdates(catalogContext); runErr != nil && catalogContext.Err() == nil {
			log.Printf("模型目录自动更新停止: %v", runErr)
		}
	}()
	verifier, err := verification.New(store.DB(), upstreams, store)
	if err != nil {
		return fmt.Errorf("初始化模型验证: %w", err)
	}
	verifier.Records = history
	verifier.SetProgressHandler(func(progress verification.VerificationProgress) {
		if err := verificationProgress.Broadcast(progress); err != nil {
			log.Printf("推送模型验证进度失败: %v", err)
		}
	})
	mygo.Power.OnSuspend(verifier.Suspend)
	mygo.Power.OnResume(func() {
		store.CheckCatalog()
		if err := verifier.Resume(context.Background()); err != nil {
			log.Printf("恢复模型验证计时失败: %v", err)
		}
	})
	verificationContext, stopVerification := context.WithCancel(context.Background())
	verificationDone := make(chan struct{})
	defer func() { stopVerification(); verifier.Close(); <-verificationDone }()
	go func() {
		defer close(verificationDone)
		if err := verifier.Run(verificationContext); err != nil && verificationContext.Err() == nil {
			log.Printf("模型验证定时执行停止: %v", err)
		}
	}()
	mygo.Theme.SetSource(themeSource(general.Appearance))
	applicationUpdates := updates.NewDesktop(buildinfo.Version)
	defer applicationUpdates.Close()
	service := app.NewVelaService(app.Options{
		Updates:      applicationUpdates,
		Records:      history,
		Version:      buildinfo.Version,
		Models:       store,
		Providers:    upstreams,
		Accounts:     accounts,
		Settings:     preferences,
		Scheduling:   policies,
		Execution:    execution,
		Verification: verifier,
		ModelTests:   modelTests,
		Credentials:  secrets,
		ApplyGeneral: applyGeneral,
		ApplyGateway: applyGateway,
	})
	mygo.Bind(service)
	queryWork.Add(1)
	go func() {
		defer queryWork.Done()
		app.RunLocalRefresh(queryContext, service, func(scope string) {
			if err := localRefreshCompleted.Broadcast(scope); err != nil && queryContext.Err() == nil {
				log.Printf("推送后台刷新结果失败: %v", err)
			}
		})
	}()

	refreshOnVisible = func() {
		store.CheckCatalog()
		if err := windowVisible.Broadcast(true); err != nil {
			log.Printf("推送窗口恢复事件失败: %v", err)
		}
		queryWork.Add(1)
		go func() {
			defer queryWork.Done()
			config, err := preferences.AccountRefresh(queryContext)
			if err == nil && config.OnVisible {
				_ = accounts.RefreshBackground(queryContext, true, true)
			}
		}()
	}
	mygo.App.WhenReady(func() {
		watchWindowVisibility(queryContext)
		mainWindow = createMainWindow()
		syncTray()
	})
	mygo.App.OnWindowAllClosed(lastWindowClosed)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
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

	return mygo.App.Run()
}
