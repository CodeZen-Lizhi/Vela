package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
)

// TestSaveGatewaySettingsAppliesBeforePersisting 验证监听失败不覆盖旧端口，成功后才保存新端口。
func TestSaveGatewaySettingsAppliesBeforePersisting(t *testing.T) {
	store, err := models.Open(filepath.Join(t.TempDir(), "vela.sqlite"), http.DefaultClient, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	preferences, err := settings.New(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	applyErr := errors.New("端口占用")
	applied := 0
	service := NewVelaService(Options{Settings: preferences, ApplyGateway: func(context.Context, settings.GatewaySettings) error {
		applied++
		return applyErr
	}})
	for _, port := range []int{0, 80, 1023, 65536, -1} {
		if err = service.SaveGatewaySettings(context.Background(), settings.GatewaySettings{Port: port}); err == nil {
			t.Fatalf("非法端口 %d 未被拒绝", port)
		}
	}
	if applied != 0 {
		t.Fatalf("非法端口触发监听 %d 次", applied)
	}
	if err = service.SaveGatewaySettings(context.Background(), settings.GatewaySettings{Port: 19090}); err == nil {
		t.Fatal("监听失败仍返回成功")
	}
	if got, err := preferences.Gateway(context.Background()); err != nil || got.Port != settings.DefaultGateway().Port {
		t.Fatalf("失败后端口 = %+v，%v", got, err)
	}

	service.applyGateway = func(context.Context, settings.GatewaySettings) error { return nil }
	if err = service.SaveGatewaySettings(context.Background(), settings.GatewaySettings{Port: 19090}); err != nil {
		t.Fatal(err)
	}
	if got, err := preferences.Gateway(context.Background()); err != nil || got.Port != 19090 {
		t.Fatalf("成功后端口 = %+v，%v", got, err)
	}
}

// TestSaveAccessKeyUpdatesListeningGateway 验证新密钥落盘后立即授权网关重新读取。
func TestSaveAccessKeyUpdatesListeningGateway(t *testing.T) {
	dir := t.TempDir()
	store, err := models.Open(filepath.Join(dir, "vela.sqlite"), http.DefaultClient, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	preferences, err := settings.New(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied := settings.GatewaySettings{}
	service := NewVelaService(Options{
		Settings:    preferences,
		Credentials: secrets,
		ApplyGateway: func(_ context.Context, value settings.GatewaySettings) error {
			applied = value
			return nil
		},
	})
	if err = service.SaveAccessKey("test-access-key"); err != nil {
		t.Fatal(err)
	}
	if applied.Port != settings.DefaultGateway().Port {
		t.Fatalf("应用端口 = %d", applied.Port)
	}
	if value, err := secrets.Get("gateway-access-key"); err != nil || string(value) != "test-access-key" {
		t.Fatalf("保存密钥 = %q，%v", value, err)
	}
}
