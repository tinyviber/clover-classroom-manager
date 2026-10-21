//go:build windows

package capture

import (
	"image"
	"sync"
	"unsafe"

	"github.com/kbinani/screenshot"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	user32      = windows.NewLazySystemDLL("user32.dll")
	getWinTextW = user32.NewProc("GetWindowTextW")
)

// grab 截取主屏（display 0）。
func grab() (image.Image, error) {
	return screenshot.CaptureDisplay(0)
}

// pinned 是窗口广播锁定的目标窗口：开播那一刻的前台窗，之后教师切
// 焦点不再影响共享对象（想换共享对象须停广播重开）。标题用于管理端
// 展示「正在共享 xxx」。
var pinned struct {
	mu   sync.Mutex
	hwnd win.HWND
}

// PinWindow 钉住当前前台窗口作为窗口广播的共享对象，返回其标题。
func PinWindow() string {
	hwnd := win.GetForegroundWindow()
	pinned.mu.Lock()
	pinned.hwnd = hwnd
	pinned.mu.Unlock()
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 256)
	n, _, _ := getWinTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 {
		return windows.UTF16ToString(buf[:n])
	}
	return ""
}

// UnpinWindow 解除钉住（广播结束时调用）。
func UnpinWindow() {
	pinned.mu.Lock()
	pinned.hwnd = 0
	pinned.mu.Unlock()
}

func grabWindow() (image.Image, error) {
	pinned.mu.Lock()
	hwnd := pinned.hwnd
	pinned.mu.Unlock()
	if hwnd == 0 {
		hwnd = win.GetForegroundWindow() // 未钉住：退回前台窗（演示页/联调）
	}
	if hwnd == 0 {
		return grab()
	}
	var rect win.RECT
	if !win.GetWindowRect(hwnd, &rect) {
		return grab()
	}
	left, top := int(rect.Left), int(rect.Top)
	right, bottom := int(rect.Right), int(rect.Bottom)
	if right-left < 32 || bottom-top < 32 {
		return grab()
	}
	return screenshot.CaptureRect(image.Rect(left, top, right, bottom))
}
