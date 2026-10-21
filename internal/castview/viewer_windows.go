//go:build windows

package castview

// 学生机广播窗：
//   - fullscreen：WS_POPUP 无边框全屏，WS_EX_TOPMOST 置顶（不可关闭）
//   - window：普通重叠窗 ~1280x720 置顶，可最小化（关闭按钮仅隐藏，不退出接收）
//   - 顶部悬浮工具条（仿极域）：切「全屏⇄窗口」+「退出广播」，可拖动
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

const (
	barHeight = 36
	barWidth  = 360
	btnMode   = 1
	btnExit   = 2
)

var (
	mu      sync.Mutex
	hwnd    win.HWND
	bar     win.HWND
	btnM    win.HWND
	img     *image.RGBA // 最新解码帧
	mode    string      // 目标模式
	visible bool        // 有窗口在显示（隐藏时 Frame 不解码省 CPU）
	cmds    = make(chan func(), 16)
	frames  = make(chan []byte, 1) // 单槽：只解最新帧
	once    sync.Once
)

// OnExit 学生端悬浮条「退出广播」回调（隐藏窗口之外的动作由调用方决定，
// 例如 agent 用来停掉组播接收）。在窗口线程上被调，实现里不许阻塞。
var OnExit func()

var (
	gdi32     = windows.NewLazySystemDLL("gdi32.dll")
	stretchDI = gdi32.NewProc("StretchDIBits")
	user32    = windows.NewLazySystemDLL("user32.dll")
	setTextW  = user32.NewProc("SetWindowTextW")
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
	visible = true
	mu.Unlock()
	once.Do(func() {
		go windowThread()
		go decodeThread()
	})
	enqueue(func() { applyMode() })
}

// Frame 喂一帧 JPEG。解码在专门 goroutine 上跑（不占调用方的
// UDP/TCP 读循环），窗口隐藏时直接丢弃省 CPU。
func Frame(jpeg []byte) {
	mu.Lock()
	v := visible
	mu.Unlock()
	if !v {
		return
	}
	select {
	case frames <- jpeg:
	default:
		select {
		case <-frames: // 丢弃未解码的旧帧，只留最新
		default:
		}
		select {
		case frames <- jpeg:
		default:
		}
	}
}

// decodeThread 从单槽队列取最新 JPEG 解码成 RGBA 并触发重绘。
func decodeThread() {
	for jpeg := range frames {
		src, _, err := image.Decode(bytes.NewReader(jpeg))
		if err != nil {
			continue
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
}

// Close 关闭广播窗与工具条（学生端「退出广播」也是走这里）。
func Close() {
	enqueue(func() { hideAll() })
}

func hideAll() {
	mu.Lock()
	h, b := hwnd, bar
	img = nil
	visible = false
	mu.Unlock()
	if b != 0 {
		win.ShowWindow(b, win.SW_HIDE)
	}
	if h != 0 {
		win.ShowWindow(h, win.SW_HIDE)
	}
}

// applyMode 在窗口线程内按 mode 调整（或创建）窗口与悬浮工具条。
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
	showBar()
}

// ---- 悬浮工具条（仿极域顶栏）----

func showBar() {
	mu.Lock()
	b := bar
	m := mode
	owner := hwnd
	mu.Unlock()
	sw := win.GetSystemMetrics(win.SM_CXSCREEN)
	x := (sw - barWidth) / 2
	if b == 0 {
		b = createBar(x, 4, owner)
		if b == 0 {
			return
		}
		mu.Lock()
		bar = b
		mu.Unlock()
	} else {
		win.SetWindowPos(b, win.HWND_TOPMOST, x, 4, barWidth, barHeight, win.SWP_SHOWWINDOW)
	}
	win.ShowWindow(b, win.SW_SHOW)
	label := "窗口模式"
	if m == "window" {
		label = "全屏模式"
	}
	if btnM != 0 {
		ptr, _ := windows.UTF16PtrFromString(label)
		setTextW.Call(uintptr(btnM), uintptr(unsafe.Pointer(ptr)))
	}
}

// createBar 建悬浮工具条；owner 传广播窗 hwnd —— 有 owner 的弹窗
// 永远停在 owner 之上，否则点一下广播窗工具条就被压到下层。
func createBar(x, y int32, owner win.HWND) win.HWND {
	name, _ := windows.UTF16PtrFromString("LabCastBar")
	wc := win.WNDCLASSEX{
		LpfnWndProc:   windows.NewCallback(barProc),
		HInstance:     win.GetModuleHandle(nil),
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)),
		HbrBackground: win.HBRUSH(win.GetStockObject(win.BLACK_BRUSH)),
		LpszClassName: name,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	win.RegisterClassEx(&wc)
	h := win.CreateWindowEx(
		win.WS_EX_TOPMOST|win.WS_EX_TOOLWINDOW|win.WS_EX_NOACTIVATE,
		name, nil,
		win.WS_POPUP|win.WS_VISIBLE,
		x, y, barWidth, barHeight, owner, 0, wc.HInstance, nil)
	if h == 0 {
		return 0
	}
	btnCls, _ := windows.UTF16PtrFromString("BUTTON")
	mkBtn := func(id int, text string, bx int32) win.HWND {
		t, _ := windows.UTF16PtrFromString(text)
		return win.CreateWindowEx(0, btnCls, t,
			win.WS_CHILD|win.WS_VISIBLE|win.BS_PUSHBUTTON,
			bx, 5, 92, 26, h, win.HMENU(uintptr(id)), wc.HInstance, nil)
	}
	btnM = mkBtn(btnMode, "全屏模式", 160)
	mkBtn(btnExit, "退出广播", 258)
	return h
}

func barProc(h win.HWND, umsg uint32, wp, lp uintptr) uintptr {
	switch umsg {
	case win.WM_PAINT:
		var ps win.PAINTSTRUCT
		hdc := win.BeginPaint(h, &ps)
		var rc win.RECT
		win.GetClientRect(h, &rc)
		win.SetTextColor(hdc, 0x00DDDDDD)
		win.SetBkMode(hdc, win.TRANSPARENT)
		txt, _ := windows.UTF16FromString("屏幕广播中")
		tr := win.RECT{Left: 12, Top: 0, Right: 154, Bottom: barHeight}
		drawText(hdc, txt, &tr, win.DT_LEFT|win.DT_VCENTER|win.DT_SINGLELINE)
		win.EndPaint(h, &ps)
		return 0
	case win.WM_COMMAND:
		switch int(win.LOWORD(uint32(wp))) {
		case btnMode:
			mu.Lock()
			if mode == "fullscreen" {
				mode = "window"
			} else {
				mode = "fullscreen"
			}
			mu.Unlock()
			applyMode()
		case btnExit:
			hideAll()
			if f := OnExit; f != nil {
				f() // 通知 agent 停收流（组播/TCP 帧不再解码）
			}
		}
		return 0
	case win.WM_NCHITTEST:
		return win.HTCAPTION // 空白处拖动
	case win.WM_DESTROY:
		mu.Lock()
		bar = 0
		mu.Unlock()
		return 0
	}
	return win.DefWindowProc(h, umsg, wp, lp)
}

// drawText 走 gdi32 DrawTextW（lxn/win 未导出）。
var drawTextW = gdi32.NewProc("DrawTextW")

func drawText(hdc win.HDC, s []uint16, rc *win.RECT, format uint32) {
	if len(s) == 0 {
		return
	}
	drawTextW.Call(uintptr(hdc), uintptr(unsafe.Pointer(&s[0])),
		uintptr(len(s)), uintptr(unsafe.Pointer(rc)), uintptr(format))
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
			return 0 // 全屏广播不可关（退出走工具条）
		}
		win.ShowWindow(h, win.SW_HIDE)
		mu.Lock()
		b := bar
		mu.Unlock()
		if b != 0 {
			win.ShowWindow(b, win.SW_HIDE)
		}
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
