// Package desktopinstall 处理从只读 macOS 安装介质启动时的应用替换，不读取业务数据。
package desktopinstall

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// installScript 先校验并暂存新版，再退出目标旧进程、换名替换；失败保留原应用。
//
//go:embed install.sh
var installScript []byte

// SourceBundle 仅识别只读安装卷或 Gatekeeper 转置中的 Vela.app，普通已安装或开发进程返回空。
func SourceBundle(executable string) (string, error) {
	const suffix = "/Vela.app/Contents/MacOS/Vela"
	if !strings.HasSuffix(executable, suffix) || (!strings.HasPrefix(executable, "/Volumes/") && !strings.Contains(executable, "/AppTranslocation/")) {
		return "", nil
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(executable, &stat); err != nil {
		return "", fmt.Errorf("检查安装介质: %w", err)
	}
	if stat.Flags&unix.MNT_RDONLY == 0 {
		return "", nil
	}
	return strings.TrimSuffix(executable, "/Contents/MacOS/Vela"), nil
}

// Install 把已验证的来源复制到目标路径；必要时由系统请求管理员授权，始终不以管理员身份启动应用。
func Install(source, target string) error {
	if filepath.Base(source) != "Vela.app" || filepath.Base(target) != "Vela.app" || source == target {
		return errors.New("无效的 Vela 安装路径")
	}
	directory, err := os.MkdirTemp("", "vela-install-")
	if err != nil {
		return fmt.Errorf("准备安装程序: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			log.Printf("清理临时安装脚本失败: %v", err)
		}
	}()
	script := filepath.Join(directory, "install.sh")
	if err := os.WriteFile(script, installScript, 0600); err != nil {
		return fmt.Errorf("准备安装脚本: %w", err)
	}
	command := exec.Command("/bin/sh", script, source, target)
	privileged, err := needsAdministrator(target)
	if err != nil {
		return err
	}
	if privileged {
		// 参数经 AppleScript 的 quoted form 转义，不把文件路径插入脚本源码。
		command = exec.Command("/usr/bin/osascript", "-e", `on run argv
return do shell script "/bin/sh " & quoted form of (item 1 of argv) & " " & quoted form of (item 2 of argv) & " " & quoted form of (item 3 of argv) with administrator privileges
end run`, script, source, target)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装未完成: %w\n%s", err, bytes.TrimSpace(output))
	}
	return nil
}

// needsAdministrator 在目标父目录不可写或旧版归其他用户所有时使用系统授权，确保可完整替换并清理旧包。
func needsAdministrator(target string) (bool, error) {
	if err := unix.Access(filepath.Dir(target), unix.W_OK); err != nil {
		if errors.Is(err, unix.EACCES) {
			return true, nil
		}
		return false, fmt.Errorf("检查应用程序目录: %w", err)
	}
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查旧版应用: %w", err)
	}
	return info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()), nil
}

// Launch 通过当前用户的 LaunchServices 明确打开已安装副本，避免再次激活仍在 DMG 中的安装进程。
func Launch(target string) error {
	args := []string{"-n"}
	// 显式隔离启动必须传递给安装后进程，LaunchServices 默认不继承调用者环境。
	if dataDir := os.Getenv("VELA_DATA_DIR"); dataDir != "" {
		args = append(args, "--env", "VELA_DATA_DIR="+dataDir)
	}
	args = append(args, target)
	if output, err := exec.Command("/usr/bin/open", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("Vela 已安装到 %s，但未能自动打开，请手动启动: %w\n%s", target, err, bytes.TrimSpace(output))
	}
	return nil
}
