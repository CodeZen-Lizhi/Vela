package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataDirectoryLock 通过独立进程验证同目录独占、目录别名保护和进程退出后重新打开。
func TestDataDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	alias := filepath.Join(t.TempDir(), "same-data")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, alias} {
		checkDataLockProcess(t, path, "blocked")
	}
	// 不同隔离数据目录仍可并行运行。
	checkDataLockProcess(t, t.TempDir(), "acquired")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// 子进程刻意不关闭描述符，退出后操作系统必须释放锁，不能被残留文件永久阻止。
	checkDataLockProcess(t, dir, "acquired")
	reopened, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

// checkDataLockProcess 在只执行锁测试入口的子进程中核对预期，不启动桌面或读取真实用户目录。
func checkDataLockProcess(t *testing.T, dir, expected string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestDataDirectoryLockProcess$")
	command.Env = append(os.Environ(), "VELA_TEST_LOCK_DIR="+dir, "VELA_TEST_LOCK_EXPECTED="+expected)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("独立进程锁检查失败: %v\n%s", err, output)
	}
}

// TestDataDirectoryLockProcess 是真实进程锁探针；普通测试运行不进入子进程分支。
func TestDataDirectoryLockProcess(t *testing.T) {
	dir := os.Getenv("VELA_TEST_LOCK_DIR")
	if dir == "" {
		t.Skip("仅供独立进程锁验收")
	}
	lock, err := lockDataDirectory(dir)
	if os.Getenv("VELA_TEST_LOCK_EXPECTED") == "blocked" {
		if err == nil {
			lock.Close()
			t.Fatal("同数据目录被第二个进程打开")
		}
		if !strings.Contains(err.Error(), "另一个 Vela 进程") {
			t.Fatalf("未返回可理解的占用说明: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// 保持描述符存活至子进程结束，验收操作系统自动释放；不操作任何业务数据。
	if _, err := lock.Stat(); err != nil {
		t.Fatal(err)
	}
}

// TestDesktopLogRotation 验证本次与上次日志分开保留，当前日志只允许所属用户读取。
func TestDesktopLogRotation(t *testing.T) {
	dir := t.TempDir()
	oldWriter := log.Writer()
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	first, err := openDesktopLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.Print("上一轮启动")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := openDesktopLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	log.Print("当前启动")
	current, err := os.ReadFile(filepath.Join(dir, "vela.log"))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(filepath.Join(dir, "vela.previous.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "当前启动") || strings.Contains(string(current), "上一轮启动") || !strings.Contains(string(previous), "上一轮启动") {
		t.Fatal("本次与上次诊断日志未正确隔离")
	}
	info, err := os.Stat(filepath.Join(dir, "vela.log"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("诊断日志权限不正确: %v", err)
	}
}
