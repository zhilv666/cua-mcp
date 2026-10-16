//go:build windows

// Package cursor 检测并修复被 computer use 引擎接管的系统光标。
//
// 背景：引擎带 cursor overlay —— 操作窗口时它会隐藏系统光标、自己绘制一个。
// 正常情况下引擎退出时会还原。但若引擎被强杀且来不及清理，
// 隐藏状态会留在系统里：**整个桌面看不到鼠标指针**（鼠标仍可移动可点击）。
//
// 为什么这层保险只能做成"事后修复"而不是"拦截退出"：
// Windows 没有可捕获的终止信号。TerminateProcess 是瞬时的，
// 目标进程一行代码都跑不了 —— 没有任何 handler 能拦下强杀。
// 所以做法是提供一条恢复命令（cua-mcp fix-cursor），必要时手工执行。
package cursor

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	advapi = syscall.NewLazyDLL("advapi32.dll")

	procGetCursorInfo    = user32.NewProc("GetCursorInfo")
	procGetCursor        = user32.NewProc("GetCursor")
	procGetIconInfo      = user32.NewProc("GetIconInfo")
	procSystemParameters = user32.NewProc("SystemParametersInfoW")
	procLoadImage        = user32.NewProc("LoadImageW")
	procCopyImage        = user32.NewProc("CopyImage")
	procSetSystemCursor  = user32.NewProc("SetSystemCursor")

	procGetObject    = gdi32.NewProc("GetObjectW")
	procDeleteObject = gdi32.NewProc("DeleteObject")

	procRegOpenKeyEx    = advapi.NewProc("RegOpenKeyExW")
	procRegQueryValueEx = advapi.NewProc("RegQueryValueExW")
	procRegCloseKey     = advapi.NewProc("RegCloseKey")
)

type point struct{ x, y int32 }

type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     uintptr
	ptScreenPos point
}

type iconInfo struct {
	fIcon    int32
	xHotspot int32
	yHotspot int32
	hbmMask  uintptr
	hbmColor uintptr
}

type bitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

const (
	cursorShowing = 0x00000001
	imageCursor   = 2

	spiSetCursors     = 0x0057
	spifUpdateINIFile = 0x01
	spifSendChange    = 0x02

	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040

	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	regSZ           = 1
)

// State 描述当前系统光标。
type State struct {
	// Visible 是 GetCursorInfo 报的可见标志。
	Visible bool
	// Width/Height 是当前光标位图尺寸。
	// 0x0 或 1x1 说明光标被换成了不可见的空位图。
	Width  int32
	Height int32
}

// LooksHijacked 判断光标是否呈被接管后的异常形态。
//
// 只看 GetCursorInfo 的可见标志不可靠 —— 实测引擎把位图换成空白后，
// 该标志仍为 true。位图尺寸才是可靠指标。
func (s State) LooksHijacked() bool {
	if !s.Visible {
		return true
	}
	return s.Width <= 1 && s.Height <= 1
}

func (s State) String() string {
	return fmt.Sprintf("可见=%v 位图=%dx%d", s.Visible, s.Width, s.Height)
}

// Probe 读取当前系统光标状态。
func Probe() State {
	var st State

	ci := cursorInfo{cbSize: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ok != 0 {
		st.Visible = ci.flags&cursorShowing != 0
	}

	h, _, _ := procGetCursor.Call()
	if h == 0 {
		return st
	}

	var ii iconInfo
	if ok, _, _ := procGetIconInfo.Call(h, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return st
	}
	if ii.hbmColor != 0 {
		var bm bitmap
		if n, _, _ := procGetObject.Call(ii.hbmColor,
			uintptr(unsafe.Sizeof(bitmap{})), uintptr(unsafe.Pointer(&bm))); n != 0 {
			st.Width, st.Height = bm.bmWidth, bm.bmHeight
		}
		procDeleteObject.Call(ii.hbmColor)
	}
	if ii.hbmMask != 0 {
		procDeleteObject.Call(ii.hbmMask)
	}
	return st
}

// 光标注册表值名 -> SetSystemCursor 的 OCR_* id。
var cursorIDs = []struct {
	name string
	id   uint32
}{
	{"Arrow", 32512}, {"IBeam", 32513}, {"Wait", 32514}, {"Crosshair", 32515},
	{"UpArrow", 32516}, {"SizeNWSE", 32642}, {"SizeNS", 32643}, {"SizeWE", 32644},
	{"SizeNESW", 32645}, {"SizeAll", 32646}, {"No", 32648}, {"Hand", 32649},
	{"AppStarting", 32650}, {"Help", 32651}, {"NWPen", 32631},
}

// Restore 从注册表的光标方案重装系统光标，返回恢复了多少项。
//
// 单靠 SPI_SETCURSORS 不够 —— 光标被替换过时它不生效（实测返回 false）。
// 必须逐项 SetSystemCursor 把每一项真正装回去。
func Restore() (int, error) {
	paths, err := schemePaths()
	if err != nil {
		return 0, err
	}

	restored := 0
	for _, c := range cursorIDs {
		path, ok := paths[c.name]
		if !ok || path == "" {
			continue
		}
		if loadAndSetCursor(path, c.id) {
			restored++
		}
	}

	// 通知系统刷新。
	procSystemParameters.Call(spiSetCursors, 0, 0, spifUpdateINIFile|spifSendChange)

	if restored == 0 {
		return 0, fmt.Errorf("没有恢复任何光标（光标方案里找不到可用文件）")
	}
	return restored, nil
}

// FixIfHijacked 先探测，只有确实异常才恢复。返回是否做了修复。
func FixIfHijacked() (bool, State, error) {
	before := Probe()
	if !before.LooksHijacked() {
		return false, before, nil
	}
	if _, err := Restore(); err != nil {
		return false, before, err
	}
	return true, before, nil
}

func loadAndSetCursor(path string, id uint32) bool {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	img, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(name)),
		imageCursor, 0, 0, lrLoadFromFile|lrDefaultSize)
	if img == 0 {
		return false
	}

	// SetSystemCursor 会**销毁**传入的句柄，必须传副本 ——
	// 直接传 LoadImage 的句柄会损坏共享的系统光标资源。
	copyImg, _, _ := procCopyImage.Call(img, imageCursor, 0, 0, 0)
	if copyImg == 0 {
		return false
	}
	ok, _, _ := procSetSystemCursor.Call(copyImg, uintptr(id))
	return ok != 0
}

// schemePaths 读 HKCU\Control Panel\Cursors 下每个角色的文件路径。
//
// 直接走注册表 API 而不用 PowerShell —— 后者可能被策略限制，也更慢。
func schemePaths() (map[string]string, error) {
	subKey, err := syscall.UTF16PtrFromString(`Control Panel\Cursors`)
	if err != nil {
		return nil, err
	}

	var hKey uintptr
	ret, _, _ := procRegOpenKeyEx.Call(
		hkeyCurrentUser, uintptr(unsafe.Pointer(subKey)), 0, keyQueryValue,
		uintptr(unsafe.Pointer(&hKey)))
	if ret != 0 {
		return nil, fmt.Errorf("打开光标注册表项失败 (code=%d)", ret)
	}
	defer procRegCloseKey.Call(hKey)

	out := map[string]string{}
	for _, c := range cursorIDs {
		if v := queryStringValue(hKey, c.name); v != "" {
			out[c.name] = v
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("光标方案为空")
	}
	return out, nil
}

func queryStringValue(hKey uintptr, name string) string {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}

	var (
		typ  uint32
		size uint32
	)
	// 先问长度（传 nil 缓冲区）。
	ret, _, _ := procRegQueryValueEx.Call(hKey, uintptr(unsafe.Pointer(namePtr)),
		0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size)))
	if ret != 0 || size == 0 {
		return ""
	}
	// 只处理 REG_SZ
	if typ != regSZ {
		return ""
	}

	buf := make([]uint16, size/2+1)
	ret, _, _ = procRegQueryValueEx.Call(hKey, uintptr(unsafe.Pointer(namePtr)),
		0, uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
