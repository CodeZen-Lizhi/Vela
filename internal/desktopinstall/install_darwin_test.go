package desktopinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureBundle 创建可签名、可启动的隔离 App；真实进程捕获 SIGTERM 后写收尾标记，不模拟系统命令。
func fixtureBundle(t *testing.T, directory string) string {
	t.Helper()
	app := filepath.Join(directory, "Vela.app")
	executable := filepath.Join(app, "Contents/MacOS/Vela")
	if err := os.MkdirAll(filepath.Dir(executable), 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "helper.c")
	code := `#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
static volatile sig_atomic_t stopped = 0;
static void stop(int sig) { stopped = 1; }
int main(int argc, char **argv) {
 signal(SIGTERM, argc > 1 ? SIG_IGN : stop);
 char path[4096]; snprintf(path, sizeof(path), "%.*s.ready", (int)strlen(argv[0]) - (int)strlen("/Contents/MacOS/Vela"), argv[0]);
 FILE *f = fopen(path, "w"); if (!f) return 2; fclose(f);
 while (!stopped) pause();
 usleep(200000);
 snprintf(path, sizeof(path), "%.*s.stopped", (int)strlen(argv[0]) - (int)strlen("/Contents/MacOS/Vela"), argv[0]);
 f = fopen(path, "w"); if (!f) return 3; fclose(f);
 return 0;
}`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, "/usr/bin/cc", source, "-o", executable)
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.codezenlizhi.vela</string><key>CFBundleName</key><string>Vela install test</string><key>CFBundleExecutable</key><string>Vela</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, "/usr/bin/codesign", "--force", "--sign", "-", app)
	return app
}

// runCommand 对真实系统命令保留失败诊断。
func runCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, output)
	}
	return strings.TrimSpace(string(output))
}

// waitFile 等待真实进程启动或收尾，超时为明确的验收失败。
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待文件超时: %s", path)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// startBundle 以绝对路径启动目标进程；测试清理仅处理本测试创建的进程。
func startBundle(t *testing.T, app string, refuse bool) *exec.Cmd {
	t.Helper()
	executable := filepath.Join(app, "Contents/MacOS/Vela")
	command := exec.Command(executable)
	if refuse {
		command.Args = append(command.Args, "ignore")
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	waitFile(t, app+".ready")
	return command
}

// TestInstall 验证真实复制、签名、目标旧进程退出、整包替换与数据边界。
func TestInstall(t *testing.T) {
	source := fixtureBundle(t, filepath.Join(t.TempDir(), "DMG source with spaces"))
	parent := t.TempDir()
	target := fixtureBundle(t, parent)
	other := fixtureBundle(t, t.TempDir())
	old := startBundle(t, target, false)
	untouched := startBundle(t, other, false)
	marker := filepath.Join(target, "obsolete.txt")
	if err := os.WriteFile(marker, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(parent, "data.txt")
	if err := os.WriteFile(data, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(source, target); err != nil {
		t.Fatal(err)
	}
	if err := old.Wait(); err != nil {
		t.Fatal(err)
	}
	output := runCommand(t, "/bin/ps", "-p", fmt.Sprint(untouched.Process.Pid), "-o", "comm=")
	if output != filepath.Join(other, "Contents/MacOS/Vela") {
		t.Fatal(output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("旧版文件未移除: %v", err)
	}
	if content, err := os.ReadFile(data); err != nil || string(content) != "keep" {
		t.Fatalf("无关数据改变: %s %v", content, err)
	}
	runCommand(t, "/usr/bin/codesign", "--verify", "--deep", "--strict", target)
}

// TestInstallInvalidSource 验证来源损坏时在退出旧进程之前失败。
func TestInstallInvalidSource(t *testing.T) {
	source := fixtureBundle(t, t.TempDir())
	target := fixtureBundle(t, t.TempDir())
	child := startBundle(t, target, false)
	if err := os.WriteFile(filepath.Join(source, "Contents/MacOS/Vela"), []byte("corrupt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Install(source, target); err == nil {
		t.Fatal("损坏来源不应安装成功")
	}
	runCommand(t, "/bin/ps", "-p", fmt.Sprint(child.Process.Pid), "-o", "comm=")
	runCommand(t, "/usr/bin/codesign", "--verify", "--deep", "--strict", target)
}

// TestInstallRefusedQuit 验证拒绝退出时不强杀、不替换旧包并清理暂存。
func TestInstallRefusedQuit(t *testing.T) {
	source := fixtureBundle(t, t.TempDir())
	parent := t.TempDir()
	target := fixtureBundle(t, parent)
	child := startBundle(t, target, true)
	marker := filepath.Join(target, "keep-old.txt")
	if err := os.WriteFile(marker, []byte("keep old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(source, target); err == nil || !strings.Contains(err.Error(), "30 秒") {
		t.Fatalf("退出超时错误: %v", err)
	}
	runCommand(t, "/bin/ps", "-p", fmt.Sprint(child.Process.Pid), "-o", "comm=")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(parent, ".vela-install.*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("暂存未清理: %v %v", entries, err)
	}
}

// TestSourceBundleMounted 在指定真实安装卷时验证来源识别；普通测试不挂载或修改系统安装目录。
func TestSourceBundleMounted(t *testing.T) {
	app := os.Getenv("VELA_INSTALL_TEST_APP")
	if app == "" {
		t.Skip("需提供只读挂载的 DMG 中 Vela.app 路径")
	}
	got, err := SourceBundle(filepath.Join(app, "Contents/MacOS/Vela"))
	if err != nil || got != app {
		t.Fatalf("安装卷识别: %s %v", got, err)
	}
}
