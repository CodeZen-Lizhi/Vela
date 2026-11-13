package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/egoist/mygo"
)

// lockDataDirectory 用操作系统文件锁保护实际目录，覆盖共用数据的安装版、开发版及隔离实例。
// 锁文件保留在目录中；仅释放文件描述符，避免删除后产生两个不同 inode 的锁。
func lockDataDirectory(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("创建应用数据目录: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dataDir, ".instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开数据目录锁: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("此数据目录已由另一个 Vela 进程使用，请等待其退出后再打开：%s", dataDir)
		}
		return nil, fmt.Errorf("锁定应用数据目录: %w", err)
	}
	return file, nil
}

// openDesktopLog 保留本次和上次启动日志；取得目录独占权后才能轮换，避免重复启动覆盖正在使用的日志。
func openDesktopLog(dataDir string) (*os.File, error) {
	path := filepath.Join(dataDir, "vela.log")
	if err := os.Rename(path, filepath.Join(dataDir, "vela.previous.log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("保留上次诊断日志: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("创建诊断日志: %w", err)
	}
	log.SetOutput(io.MultiWriter(os.Stderr, file))
	return file, nil
}

// showStartupError 在业务尚未成功装配时启动最小原生弹窗，不创建业务窗口或重置用户数据。
func showStartupError(cause error) {
	mygo.App.WhenReady(func() {
		go func() {
			_, err := mygo.Dialog.Message(mygo.MessageOptions{
				Type:    mygo.MessageError,
				Title:   "Vela 启动失败",
				Message: "Vela 无法启动",
				Detail:  cause.Error() + "\n\n请保留现有数据，按使用文档排查后重试。",
				Buttons: []string{"退出"},
			})
			if err != nil {
				log.Printf("显示启动错误失败: %v", err)
			}
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Printf("启动错误窗口失败: %v", err)
	}
}
