//go:build !windows

// Package cursor 在非 Windows 平台上不做任何事。
//
// 光标接管是 Windows 引擎（codex-computer-use.exe）的行为，
// 本包在这边只是为了交叉编译能通过。
package cursor

import "fmt"

// State 在非 Windows 上无意义。
type State struct {
	Visible bool
	Width   int32
	Height  int32
}

// LooksHijacked 在非 Windows 上恒为 false。
func (s State) LooksHijacked() bool { return false }

func (s State) String() string { return "非 Windows 平台，不适用" }

// Probe 在非 Windows 上返回空状态。
func Probe() State { return State{Visible: true} }

// Restore 在非 Windows 上无操作。
func Restore() (int, error) {
	return 0, fmt.Errorf("光标修复仅在 Windows 上可用")
}

// FixIfHijacked 在非 Windows 上无操作。
func FixIfHijacked() (bool, State, error) {
	return false, Probe(), nil
}
