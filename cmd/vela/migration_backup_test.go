package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestBackupBeforeBodyMigration 核对备份包含迁移前的数据库与凭据，并能在重试时复用。
func TestBackupBeforeBodyMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Vela data")
	lock, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	path := filepath.Join(dir, "vela.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE settings(name TEXT); CREATE TABLE request_payloads(document TEXT); INSERT INTO request_payloads VALUES('old body')")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("创建旧库: %v %v", err, closeErr)
	}
	if err := os.Mkdir(filepath.Join(dir, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials", "fixture"), []byte("test key"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := backupBeforeBodyMigration(dir); err != nil {
			t.Fatal(err)
		}
	}
	backup := dir + ".before-body-migration"
	content, err := os.ReadFile(filepath.Join(backup, "credentials", "fixture"))
	if err != nil || string(content) != "test key" {
		t.Fatalf("读取备份凭据: %v", err)
	}
	needed, err := needsBodyMigrationBackup(filepath.Join(backup, "vela.sqlite"))
	if err != nil || !needed {
		t.Fatalf("备份未保留旧库: %v", err)
	}
}
