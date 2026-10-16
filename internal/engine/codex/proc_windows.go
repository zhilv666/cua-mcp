//go:build windows

package codex

import (
	"os"
	"os/exec"
	"syscall"
)

func pid() int { return os.Getpid() }

// setSysProcAttr 让 helper 拥有独立进程组，避免与本进程抢控制台。
func setSysProcAttr(cmd *exec.Cmd) {
	// CREATE_NEW_PROCESS_GROUP
	const createNewProcessGroup = 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// afterStart 在 Windows 上无需额外处理。
//
// 这里**故意不挂 Job Object**。实测（见下方注释）表明：
//
//	helper 收到 --parent-pid，它会自己盯住父进程。父进程一死，
//	helper 立刻自行退出，并在这条路径上还原系统光标。
//
// 若用 Job Object 的 KILL_ON_JOB_CLOSE，父进程消失时系统会**抢先**
// 强杀 helper，它来不及清理 —— 结果是 cursor overlay 的隐藏状态
// 留在系统里，表现为整个桌面看不到鼠标指针（实测踩过两次）。
//
// 换句话说：防孤儿进程的措施反而破坏了优雅清理。
// helper 自己就会退，不需要我们插手。
func afterStart(cmd *exec.Cmd) {}
