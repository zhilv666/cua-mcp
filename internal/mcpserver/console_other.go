//go:build !windows

package mcpserver

import (
	"os"
	"syscall"
)

// openConsole 在类 Unix 系统上打开终端设备。
//
// 优先用 /dev/tty —— 它指向控制终端，与 stdin 是否被重定向无关。
func openConsole(_ string) (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// openConsoleOut 在类 Unix 系统上打开终端输出。
func openConsoleOut(_ string) (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}
