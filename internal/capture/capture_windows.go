//go:build windows

package capture

import (
	"image"

	"github.com/kbinani/screenshot"
	"github.com/lxn/win"
)

// grab 截取主屏（display 0）。
func grab() (image.Image, error) {
	return screenshot.CaptureDisplay(0)
}

func grabWindow() (image.Image, error) {
	hwnd := win.GetForegroundWindow()
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
