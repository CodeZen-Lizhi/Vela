package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// backupBeforeBodyMigration 在持有数据目录锁时保全旧库和凭据，供升级失败时恢复。
func backupBeforeBodyMigration(dataDir string) error {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	dataDir = absolute
	path := filepath.Join(dataDir, "vela.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	needed, err := needsBodyMigrationBackup(path)
	if err != nil || !needed {
		return err
	}

	backup := dataDir + ".before-body-migration"
	if info, err := os.Lstat(backup); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("备份路径已被占用：%s", backup)
		}
		if _, err := os.Stat(filepath.Join(backup, "vela.sqlite")); err != nil {
			return fmt.Errorf("迁移备份缺少数据库：%s: %w", backup, err)
		}
		log.Printf("复用迁移前备份: %s", backup)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	staging, err := os.MkdirTemp(filepath.Dir(dataDir), ".vela-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	args := []string{"-Rp"}
	if runtime.GOOS == "darwin" {
		args = append(args, "-c")
	}
	copyPath := filepath.Join(staging, "data")
	output, err := exec.Command("cp", append(args, dataDir, copyPath)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("复制数据目录到备份失败: %w: %s", err, output)
	}
	if err := os.Chmod(copyPath, 0700); err != nil {
		return err
	}
	if err := os.Rename(copyPath, backup); err != nil {
		return err
	}
	log.Printf("已创建迁移前备份: %s", backup)
	return nil
}

// needsBodyMigrationBackup 在应用写入数据库前检查旧正文和迁移标记。
func needsBodyMigrationBackup(path string) (bool, error) {
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return false, err
	}
	defer db.Close()
	var hasPayloads bool
	err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='request_payloads')").Scan(&hasPayloads)
	if err != nil || !hasPayloads {
		return false, err
	}
	var hasSettings bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='settings')").Scan(&hasSettings); err != nil {
		return false, err
	}
	if hasSettings {
		var migrated bool
		err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM settings WHERE name='request-body-archive-v1')").Scan(&migrated)
		if err != nil || migrated {
			return false, err
		}
	}
	var needed bool
	err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM request_payloads)").Scan(&needed)
	return needed, err
}
