package settings

import (
	"context"
	"path/filepath"
	"testing"
)

// TestModelUpdateSettings 通过真实SQLite验证默认值、旧配置兼容、关闭保留间隔和非法输入拒绝。
func TestModelUpdateSettings(t *testing.T) {
	ctx := context.Background()
	s, db := openStore(t, filepath.Join(t.TempDir(), "settings.sqlite"))
	defer db.Close()
	g, err := s.General(ctx)
	if err != nil || !g.ModelAutoUpdate || g.ModelUpdateMinutes != 10 {
		t.Fatalf("defaults %+v %v", g, err)
	}
	if _, err = db.Exec(`INSERT INTO settings(name,document) VALUES('general','{"appearance":"light","closeAction":"background","backgroundEntry":"menu","dashboardSeconds":10,"providerSeconds":60}')`); err != nil {
		t.Fatal(err)
	}
	g, err = s.General(ctx)
	if err != nil || !g.ModelAutoUpdate || g.ModelUpdateMinutes != 10 {
		t.Fatalf("legacy %+v %v", g, err)
	}
	g.ModelAutoUpdate = false
	g.ModelUpdateMinutes = 27
	if err = s.SaveGeneral(ctx, g); err != nil {
		t.Fatal(err)
	}
	reread, err := s.General(ctx)
	if err != nil || reread.ModelAutoUpdate || reread.ModelUpdateMinutes != 27 {
		t.Fatal(reread, err)
	}
	g.ModelUpdateMinutes = 0
	if err = s.SaveGeneral(ctx, g); err == nil {
		t.Fatal("zero interval accepted")
	}
	reread, _ = s.General(ctx)
	if reread.ModelUpdateMinutes != 27 {
		t.Fatal("failed save changed settings")
	}
}
