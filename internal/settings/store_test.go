package settings

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openStore 在真实 SQLite 文件上打开设置模块，供重启重读场景复用同一路径。
func openStore(t *testing.T, path string) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

// TestAccountRefreshDurationBounds 验证两组间隔均拒绝时长溢出，失败保留旧设置，最大可表示分钟数可保存并重读。
func TestAccountRefreshDurationBounds(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	maximum := int(math.MaxInt64 / int64(time.Minute))
	for _, value := range []AccountRefresh{
		{Minutes: maximum + 1, KeyMinutes: 30},
		{Minutes: 5, KeyMinutes: maximum + 1},
		{Minutes: math.MaxInt, KeyMinutes: 30},
		{Minutes: 5, KeyMinutes: math.MaxInt},
	} {
		if err := s.SaveAccountRefresh(ctx, value); err == nil {
			t.Fatalf("溢出间隔被保存: %+v", value)
		}
		if got, err := s.AccountRefresh(ctx); err != nil || got != DefaultAccountRefresh() {
			t.Fatalf("拒绝后设置改变: %+v, %v", got, err)
		}
	}
	want := AccountRefresh{Enabled: true, Minutes: maximum, KeyEnabled: true, KeyMinutes: maximum}
	if err := s.SaveAccountRefresh(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.AccountRefresh(ctx); err != nil || got != want {
		t.Fatalf("有效边界重读失败: %+v, %v", got, err)
	}
}

// TestDefaultsAndPersistence 验证未保存时的默认值，以及各分类保存后重新打开数据库仍可重读。
func TestDefaultsAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vela.sqlite")
	s, db := openStore(t, path)

	general, err := s.General(ctx)
	if err != nil || general != DefaultGeneral() {
		t.Fatalf("常规默认值 = %+v, %v", general, err)
	}
	gateway, err := s.Gateway(ctx)
	if err != nil || gateway.Port != 18080 {
		t.Fatalf("端口默认值 = %+v, %v", gateway, err)
	}
	account, err := s.AccountRefresh(ctx)
	if err != nil || account.Enabled || account.Minutes != 5 {
		t.Fatalf("账户刷新默认值 = %+v, %v", account, err)
	}
	attention, err := s.Attention(ctx)
	if err != nil || attention != DefaultAttention() {
		t.Fatalf("提醒默认值 = %+v, %v", attention, err)
	}

	wantGeneral := GeneralSettings{Appearance: "system", CloseAction: "quit", BackgroundEntry: "both", DashboardSeconds: 30, ProviderSeconds: 120, ModelAutoUpdate: true, ModelUpdateMinutes: 10}
	wantAttention := AttentionSettings{Balance: false, Difference: true, Health: false, Rate: true, BalanceThreshold: 0}
	if err = s.SaveGeneral(ctx, wantGeneral); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGateway(ctx, GatewaySettings{Port: 19090}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAccountRefresh(ctx, AccountRefresh{Enabled: true, Minutes: 7, KeyEnabled: true, KeyMinutes: 31, OnVisible: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAttention(ctx, wantAttention); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, db = openStore(t, path)
	defer db.Close()
	if general, err = s.General(ctx); err != nil || general != wantGeneral {
		t.Fatalf("重读常规设置 = %+v, %v", general, err)
	}
	if gateway, err = s.Gateway(ctx); err != nil || gateway.Port != 19090 {
		t.Fatalf("重读端口 = %+v, %v", gateway, err)
	}
	if account, err = s.AccountRefresh(ctx); err != nil || !account.Enabled || account.Minutes != 7 || !account.KeyEnabled || account.KeyMinutes != 31 || !account.OnVisible {
		t.Fatalf("重读账户刷新 = %+v, %v", account, err)
	}
	if attention, err = s.Attention(ctx); err != nil || attention != wantAttention {
		t.Fatalf("重读提醒偏好 = %+v, %v", attention, err)
	}
}

// TestInvalidValuesKeepSavedSettings 验证非法输入被拒绝且不覆盖已保存的设置。
func TestInvalidValuesKeepSavedSettings(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	if err := s.SaveGateway(ctx, GatewaySettings{Port: 20000}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountRefresh(ctx, AccountRefresh{Enabled: true, Minutes: 9, KeyMinutes: 30}); err != nil {
		t.Fatal(err)
	}

	for _, port := range []int{0, 80, 1023, 65536, -1} {
		if err := s.SaveGateway(ctx, GatewaySettings{Port: port}); err == nil {
			t.Fatalf("端口 %d 应被拒绝", port)
		}
	}
	if err := s.SaveAccountRefresh(ctx, AccountRefresh{Enabled: true, Minutes: 0}); err == nil {
		t.Fatal("0 分钟间隔应被拒绝")
	}
	if err := s.SaveAttention(ctx, AttentionSettings{BalanceThreshold: -0.01}); err == nil {
		t.Fatal("负数阈值应被拒绝")
	}
	invalid := []GeneralSettings{
		{Appearance: "sepia", CloseAction: "background", BackgroundEntry: "menu", DashboardSeconds: 10, ProviderSeconds: 60},
		{Appearance: "light", CloseAction: "hide", BackgroundEntry: "menu", DashboardSeconds: 10, ProviderSeconds: 60},
		{Appearance: "light", CloseAction: "background", BackgroundEntry: "none", DashboardSeconds: 10, ProviderSeconds: 60},
		{Appearance: "light", CloseAction: "background", BackgroundEntry: "menu", DashboardSeconds: 5, ProviderSeconds: 60},
		{Appearance: "light", CloseAction: "background", BackgroundEntry: "menu", DashboardSeconds: 10, ProviderSeconds: 30},
	}
	for _, value := range invalid {
		if err := s.SaveGeneral(ctx, value); err == nil {
			t.Fatalf("常规设置 %+v 应被拒绝", value)
		}
	}

	if gateway, err := s.Gateway(ctx); err != nil || gateway.Port != 20000 {
		t.Fatalf("端口应保持 20000，实际 %+v, %v", gateway, err)
	}
	if account, err := s.AccountRefresh(ctx); err != nil || account.Minutes != 9 {
		t.Fatalf("账户间隔应保持 9，实际 %+v, %v", account, err)
	}
	if general, err := s.General(ctx); err != nil || general != DefaultGeneral() {
		t.Fatalf("常规设置应保持默认，实际 %+v, %v", general, err)
	}
	if attention, err := s.Attention(ctx); err != nil || attention != DefaultAttention() {
		t.Fatalf("提醒偏好应保持默认，实际 %+v, %v", attention, err)
	}
}

// TestSectionsAreIndependent 验证保存一个分类不改动其他分类，也不影响同表中的全局代理行。
func TestSectionsAreIndependent(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO settings(name,document) VALUES('proxy','{"enabled":true,"url":"http://127.0.0.1:7890"}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGateway(ctx, GatewaySettings{Port: 21000}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountRefresh(ctx, AccountRefresh{Enabled: true, Minutes: 3, KeyMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	if gateway, err := s.Gateway(ctx); err != nil || gateway.Port != 21000 {
		t.Fatalf("保存账户后端口 = %+v, %v", gateway, err)
	}
	var proxy string
	if err := db.QueryRow("SELECT document FROM settings WHERE name='proxy'").Scan(&proxy); err != nil || !strings.Contains(proxy, "7890") {
		t.Fatalf("全局代理行被改动: %q, %v", proxy, err)
	}
}

// TestStoredDocumentProblems 验证旧文档缺字段时补默认值，损坏或越界内容报错而不伪装成默认设置。
func TestStoredDocumentProblems(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO settings(name,document) VALUES('general','{"appearance":"dark"}')`); err != nil {
		t.Fatal(err)
	}
	general, err := s.General(ctx)
	want := DefaultGeneral()
	want.Appearance = "dark"
	if err != nil || general != want {
		t.Fatalf("缺字段文档 = %+v, %v", general, err)
	}
	if _, err = db.Exec(`INSERT INTO settings(name,document) VALUES('gateway','{broken')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Gateway(ctx); err == nil {
		t.Fatal("损坏的端口文档应报错")
	}
	if _, err = db.Exec(`INSERT INTO settings(name,document) VALUES('account','{"enabled":true,"minutes":0}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AccountRefresh(ctx); err == nil {
		t.Fatal("越界的账户间隔应报错")
	}
}

// TestValidateAccessKey 验证访问密钥只接受 1～256 个可见 ASCII 字符。
func TestValidateAccessKey(t *testing.T) {
	for _, value := range []string{"a", "sk-Vela_2026!@#", strings.Repeat("k", 256)} {
		if err := ValidateAccessKey(value); err != nil {
			t.Fatalf("%q 应通过: %v", value, err)
		}
	}
	for _, value := range []string{"", "has space", "中文密钥", "tab\tkey", "line\nbreak", strings.Repeat("k", 257)} {
		if err := ValidateAccessKey(value); err == nil {
			t.Fatalf("%q 应被拒绝", value)
		}
	}
}

// TestLegacyAccountRefresh 保留旧余额开关及间隔，新 Key 和窗口恢复开关默认关闭。
func TestLegacyAccountRefresh(t *testing.T) {
	s, db := openStore(t, filepath.Join(t.TempDir(), "legacy.sqlite"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO settings(name,document) VALUES('account','{"enabled":true,"minutes":17}')`); err != nil {
		t.Fatal(err)
	}
	value, err := s.AccountRefresh(context.Background())
	if err != nil || !value.Enabled || value.Minutes != 17 || value.KeyEnabled || value.KeyMinutes != 30 || value.OnVisible {
		t.Fatalf("旧设置兼容失败: %+v %v", value, err)
	}
}
