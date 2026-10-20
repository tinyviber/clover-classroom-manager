//go:build windows

package castview

// 学生机广播窗：
//   - fullscreen：WS_POPUP 无边框全屏，WS_EX_TOPMOST 置顶（不可关闭）
//   - window：普通重叠窗 ~1280x720 置顶，可最小化（关闭按钮仅隐藏，不退出接收）
// 帧由 Show/Frame/Close 从任意 goroutine 调用；窗口与消息循环跑在
// 专门的 LockOSThread goroutine，跨线程经 cmds 通道 + WM_USER 唤醒。

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"runtime"
	"sync"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const wmFrame = win.WM_USER + 1

var (
	mu   sync.Mutex
	hwnd win.HWND
	img  *image.RGBA // 最新解码帧
	mode string      // 目标模式
	cmds = make(chan func(), 16)
	once sync.Once
	cls  atom
)

var (
	gdi32     = windows.NewLazySystemDLL("gdi32.dll")
	stretchDI = gdi32.NewProc("StretchDIBits")
)

type atom = win.ATOM

func enqueue(fn func()) {
	select {
	case cmds <- fn:
	default:
	}
	mu.Lock()
	h := hwnd
	mu.Unlock()
	if h != 0 {
		win.PostMessage(h, wmFrame, 0, 0)
	}
}

// Show 打开/切换广播窗。mode: "fullscreen" | "window"。
func Show(m string) {
	mu.Lock()
	mode = m
	mu.Unlock()
	once.Do(func() {
		go windowThread()
	})
	enqueue(func() { applyMode() })
}

// Frame 喂一帧 JPEG。
func Frame(jpeg []byte) {
	src, _, err := image.Decode(bytes.NewReader(jpeg))
	if err != nil {
		return
	}
	rgba := toRGBA(src)
	mu.Lock()
	img = rgba
	h := hwnd
	mu.Unlock()
	if h != 0 {
		win.PostMessage(h, wmFrame, 0, 0)
	}
}

// Close 关闭广播窗（销毁窗口；线程退出后仍可再次 Show——Show 里重建）。
func Close() {
	enqueue(func() {
		mu.Lock()
		h := hwnd
		img = nil
		mu.Unlock()
		if h != 0 {
			win.ShowWindow(h, win.SW_HIDE)
		}
	})
}

// applyMode 在窗口线程内按 mode 调整（或创建）窗口。
func applyMode() {
	mu.Lock()
	m := mode
	h := hwnd
	mu.Unlock()
	sw, sh := win.GetSystemMetrics(win.SM_CXSCREEN), win.GetSystemMetrics(win.SM_CYSCREEN)
	style := uint32(win.WS_OVERLAPPEDWINDOW)
	x, y, w, ht := int32(60), int32(40), sw-120, sh-80
	if m == "fullscreen" {
		style = win.WS_POPUP
		x, y, w, ht = 0, 0, sw, sh
	}
	if h == 0 {
		h = createWindow(style, x, y, w, ht)
		if h == 0 {
			return
		}
		mu.Lock()
		hwnd = h
		mu.Unlock()
	} else {
		win.SetWindowLong(h, win.GWL_STYLE, int32(style))
		win.SetWindowPos(h, win.HWND_TOPMOST, x, y, w, ht,
			win.SWP_FRAMECHANGED|win.SWP_SHOWWINDOW)
	}
	win.ShowWindow(h, win.SW_SHOW)
	win.InvalidateRect(h, nil, true)
}

// ---- 窗口线程与 WndProc ----

func windowThread() {
	runtime.LockOSThread()
	// Create the first window before entering GetMessage. Show may enqueue its
	// follow-up mode update before hwnd exists, in which case there is no HWND
	// to wake with PostMessage; starting here makes the first call visible.
	applyMode()
	mu.Lock()
	h := hwnd
	mu.Unlock()
	if h != 0 {
		// Drain Show/Close commands queued during initial window creation.
		win.PostMessage(h, wmFrame, 0, 0)
	}
	for win.GetMessage(&msg, 0, 0, 0) > 0 {
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
	mu.Lock()
	hwnd = 0
	mu.Unlock()
}

var msg win.MSG

func createWindow(style uint32, x, y, w, h int32) win.HWND {
	name, _ := windows.UTF16PtrFromString("LabCastView")
	title, _ := windows.UTF16PtrFromString("屏幕广播")
	wc := win.WNDCLASSEX{
		LpfnWndProc:   windows.NewCallback(wndProc),
		HInstance:     win.GetModuleHandle(nil),
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)),
		HbrBackground: win.HBRUSH(win.GetStockObject(win.BLACK_BRUSH)),
		LpszClassName: name,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	win.RegisterClassEx(&wc)
	return win.CreateWindowEx(win.WS_EX_TOPMOST, name, title,
		style|win.WS_VISIBLE, x, y, w, h, 0, 0, wc.HInstance, nil)
}

func wndProc(h win.HWND, umsg uint32, wp, lp uintptr) uintptr {
	switch umsg {
	case wmFrame:
		for {
			select {
			case fn := <-cmds:
				fn()
			default:
				goto painted
			}
		}
	painted:
		win.InvalidateRect(h, nil, false)
		return 0
	case win.WM_PAINT:
		var ps win.PAINTSTRUCT
		hdc := win.BeginPaint(h, &ps)
		var rc win.RECT
		win.GetClientRect(h, &rc)
		paint(hdc, rc)
		win.EndPaint(h, &ps)
		return 0
	case win.WM_CLOSE:
		mu.Lock()
		isFS := mode == "fullscreen"
		mu.Unlock()
		if isFS {
			return 0 // 全屏广播不可关
		}
		win.ShowWindow(h, win.SW_HIDE)
		return 0
	case win.WM_DESTROY:
		mu.Lock()
		hwnd = 0
		mu.Unlock()
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(h, umsg, wp, lp)
}

// paint 把最新帧拉伸画满客户区（黑边留宽高比）。
func paint(hdc win.HDC, rc win.RECT) {
	mu.Lock()
	src := img
	mu.Unlock()
	if src == nil {
		return
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := rc.Right-rc.Left, rc.Bottom-rc.Top
	// 等比缩放
	tw, th := dw, dw*int32(sh)/int32(sw)
	if th > dh {
		th = dh
		tw = dh * int32(sw) / int32(sh)
	}
	tx := rc.Left + (dw-tw)/2
	ty := rc.Top + (dh-th)/2
	bgra := make([]byte, len(src.Pix))
	for i := 0; i < len(src.Pix); i += 4 {
		bgra[i], bgra[i+1], bgra[i+2], bgra[i+3] =
			src.Pix[i+2], src.Pix[i+1], src.Pix[i], 255
	}
	bi := win.BITMAPINFO{}
	bi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bi.BmiHeader))
	bi.BmiHeader.BiWidth = int32(sw)
	bi.BmiHeader.BiHeight = -int32(sh) // 负数 = 顶向下
	bi.BmiHeader.BiPlanes = 1
	bi.BmiHeader.BiBitCount = 32
	bi.BmiHeader.BiCompression = win.BI_RGB
	stretchDI.Call(
		uintptr(hdc),
		uintptr(tx), uintptr(ty), uintptr(tw), uintptr(th),
		0, 0, uintptr(sw), uintptr(sh),
		uintptr(unsafe.Pointer(&bgra[0])),
		uintptr(unsafe.Pointer(&bi)),
		win.DIB_RGB_COLORS,
		win.SRCCOPY,
	)
}

// toRGBA 解码结果统一转 RGBA。
func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}
