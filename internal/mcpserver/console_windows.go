//go:build windows

package mcpserver

import (
	"os"
	"syscall"
)

// openConsole 打开 Windows 控制台输入设备。
//
// 实测注意：本机（Win11 + 该运行环境）CONIN$ 用 os.OpenFile 打不开，
// 会返回 ENOENT；但 CreateFile 走原生路径可以。这里用 syscall 直开。
func openConsole(name string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	// GENERIC_READ | GENERIC_WRITE，OPEN_EXISTING
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

// openConsoleOut 打开控制台输出设备。
func openConsoleOut(name string) (*os.File, error) {
	return openConsole(name)
}
