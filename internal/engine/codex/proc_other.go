//go:build !windows

package codex

import (
	"os"
	"os/exec"
	"syscall"
)

func pid() int { return os.Getpid() }

// setSysProcAttr 在非 Windows 上把子进程放进独立进程组，
// 便于整组回收。
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// afterStart 在非 Windows 上无需额外处理：
// 引擎本身在这些平台上并不存在（本机只有 Windows 运行时），
// 这里只是为了交叉编译能通过。
func afterStart(cmd *exec.Cmd) {}
