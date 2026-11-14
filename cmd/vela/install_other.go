//go:build !darwin

package main

// runDiskImageInstaller 在非 macOS 平台不拦截普通启动。
func runDiskImageInstaller() (bool, error) { return false, nil }
