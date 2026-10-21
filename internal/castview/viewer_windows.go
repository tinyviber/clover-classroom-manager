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
	"fmt"
	"image"
	_ "image/jpeg"
	"runtime"
	"sync"
	"time"
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
	mu        sync.Mutex
	hwnd      win.HWND
	bar       win.HWND
	btnM      win.HWND
	img       *image.RGBA // 最新解码帧
	mode      string      // 广播模式 fullscreen|window（与锁定无关）
	visible   bool        // 广播窗在显示（隐藏时 Frame 不解码省 CPU）
	teachLock bool        // screen.lock 教师锁
	guardLock bool        // 断线守护锁（与教师锁独立，互不误解锁）
	teachWhy  string
	guardWhy  string
	cmds      = make(chan func(), 16)
	frames    = make(chan []byte, 1) // 单槽：只解最新帧
	once      sync.Once
)

// OnExit 学生端悬浮条「退出广播」回调（隐藏窗口之外的动作由调用方决定，
// 例如 agent 用来停掉组播接收）。在窗口线程上被调，实现里不许阻塞。
var OnExit func()

// OnEmergencyUnlock 紧急解锁组合键（Ctrl+Alt+Shift+F12 ×3）触发时回调，
// 供 agent 上报管理端。不许阻塞。
var OnEmergencyUnlock func()

// locked 任一锁生效（mu 内）。教师锁与守护锁独立释放。
func locked() bool { return teachLock || guardLock }

func lockReason() string {
	if teachLock {
		return teachWhy
	}
	return guardWhy
}

// Locked 供外部（agent 状态上报）查询当前是否处于锁定态。
func Locked() bool {
	mu.Lock()
	defer mu.Unlock()
	return locked()
}

var (
	gdi32        = windows.NewLazySystemDLL("gdi32.dll")
	stretchDI    = gdi32.NewProc("StretchDIBits")
	user32       = windows.NewLazySystemDLL("user32.dll")
	setTextW     = user32.NewProc("SetWindowTextW")
	setHookW     = user32.NewProc("SetWindowsHookExW")
	unhookW      = user32.NewProc("UnhookWindowsHookEx")
	callNextH    = user32.NewProc("CallNextHookEx")
	findWinW     = user32.NewProc("FindWindowW")
	getAsyncKeyW = user32.NewProc("GetAsyncKeyState")
)

// ---- 全屏输入锁定（仿极域：广播时锁死学生机键鼠）----
//
// WH_KEYBOARD_LL/WH_MOUSE_LL 低级钩子在我们自己的进程里回调（不注入
// 别人），装在跑消息循环的窗口线程上。键盘全吞（含 Alt+Tab/Win/
// Ctrl+Esc）；鼠标只放行落在悬浮工具条矩形内的点（模式切换/退出
// 按钮还要能用）。Ctrl+Alt+Del 是安全注意序列，用户态拦不了——
// 和极域一样，只能再配 DisableTaskMgr 策略兜底。

const (
	whKeyboardLL = 13
	whMouseLL    = 14
)

var (
	kbdHookH, mouHookH uintptr
	taskbarHidden      bool
	kbdCb, mouCb       uintptr
)

// 回调在 init 里建而非 var 初始化——kbdHookProc→emergencyUnlock→
// syncState→lockInput 会指回 kbdCb，var 级初始化有环。
func init() {
	kbdCb = windows.NewCallback(kbdHookProc)
	mouCb = windows.NewCallback(mouseHookProc)
}

var chord emergencyChord

func kbdHookProc(code int32, wp, lp uintptr) uintptr {
	if code < 0 {
		r, _, _ := callNextH.Call(0, uintptr(code), wp, lp)
		return r
	}
	vk := *(*uint32)(unsafe.Pointer(lp))
	up := wp == win.WM_KEYUP || wp == win.WM_SYSKEYUP
	down := func(key int32) bool {
		r, _, _ := getAsyncKeyW.Call(uintptr(key))
		return r&0x8000 != 0
	}
	if chord.key(vk, up, down(vkControl), down(vkShift), down(vkMenu), time.Now()) {
		emergencyUnlock()
	}
	return 1 // 吞掉所有按键
}

// emergencyUnlock 本机紧急解锁：两类锁全清 + 恢复任务栏（窗口线程上跑）。
func emergencyUnlock() {
	mu.Lock()
	teachLock, guardLock = false, false
	teachWhy, guardWhy = "", ""
	mu.Unlock()
	enqueue(syncState)
	if f := OnEmergencyUnlock; f != nil {
		go f() // 钩子回调里不许阻塞，异步上报
	}
}

func mouseHookProc(code int32, wp, lp uintptr) uintptr {
	if code < 0 {
		r, _, _ := callNextH.Call(0, uintptr(code), wp, lp)
		return r
	}
	pt := (*win.POINT)(unsafe.Pointer(lp)) // MSLLHOOKSTRUCT.pt 在前
	mu.Lock()
	b := bar
	mu.Unlock()
	if b != 0 {
		var rc win.RECT
		win.GetWindowRect(b, &rc)
		if pt.X >= rc.Left && pt.X < rc.Right && pt.Y >= rc.Top && pt.Y < rc.Bottom {
			r, _, _ := callNextH.Call(0, uintptr(code), wp, lp)
			return r // 放行工具条上的点
		}
	}
	return 1 // 吞掉其余鼠标
}

// lockInput/unlockInput 只在窗口线程上调。
func lockInput() {
	_ = installInputHooks()
	hideTaskbar()
}

// installInputHooks 装上低级键鼠钩子。失败时返回错误且不留下半套钩子。
// 不碰任务栏——测试可以只验钩子，不必抢交互桌面的外壳。
func installInputHooks() error {
	hMod := uintptr(win.GetModuleHandle(nil))
	if kbdHookH == 0 {
		h, _, err := setHookW.Call(whKeyboardLL, kbdCb, hMod, 0)
		if h == 0 {
			return fmt.Errorf("WH_KEYBOARD_LL: %v", err)
		}
		kbdHookH = h
	}
	if mouHookH == 0 {
		h, _, err := setHookW.Call(whMouseLL, mouCb, hMod, 0)
		if h == 0 {
			removeInputHooks()
			return fmt.Errorf("WH_MOUSE_LL: %v", err)
		}
		mouHookH = h
	}
	return nil
}

func removeInputHooks() {
	if kbdHookH != 0 {
		unhookW.Call(kbdHookH)
		kbdHookH = 0
	}
	if mouHookH != 0 {
		unhookW.Call(mouHookH)
		mouHookH = 0
	}
}

func hideTaskbar() {
	for _, cls := range []string{"Shell_TrayWnd", "Shell_SecondaryTrayWnd"} {
		p, _ := windows.UTF16PtrFromString(cls)
		if h, _, _ := findWinW.Call(uintptr(unsafe.Pointer(p)), 0); h != 0 {
			win.ShowWindow(win.HWND(h), win.SW_HIDE)
		}
	}
	taskbarHidden = true
}

func unlockInput() {
	removeInputHooks()
	restoreTaskbar()
}

func restoreTaskbar() {
	if !taskbarHidden {
		return
	}
	for _, cls := range []string{"Shell_TrayWnd", "Shell_SecondaryTrayWnd"} {
		p, _ := windows.UTF16PtrFromString(cls)
		if h, _, _ := findWinW.Call(uintptr(unsafe.Pointer(p)), 0); h != 0 {
			win.ShowWindow(win.HWND(h), win.SW_SHOW)
		}
	}
	taskbarHidden = false
}

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
// 锁定态下广播照常收流（画面被黑屏盖住，输入仍锁）。
func Show(m string) {
	mu.Lock()
	mode = m
	visible = true
	mu.Unlock()
	once.Do(func() {
		go windowThread()
		go decodeThread()
	})
	enqueue(syncState)
}

// Frame 喂一帧 JPEG。解码在专门 goroutine 上跑（不占调用方的
// UDP/TCP 读循环），窗口隐藏时直接丢弃省 CPU。
func Frame(jpeg []byte) {
	mu.Lock()
	v := visible && !locked() // 锁定态黑屏盖着广播，不解码省 CPU
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

// Lock 教师黑屏锁定（screen.lock）。
func Lock(reason string) {
	mu.Lock()
	teachLock, teachWhy = true, reason
	mu.Unlock()
	once.Do(func() {
		go windowThread()
		go decodeThread()
	})
	enqueue(syncState)
}

// Unlock 解除教师锁（screen.unlock）。守护锁仍在则保持锁定。
func Unlock() {
	mu.Lock()
	teachLock, teachWhy = false, ""
	mu.Unlock()
	enqueue(syncState)
}

// GuardLock/GuardUnlock 断线守护锁：独立于教师锁，重连只解守护锁。
func GuardLock(reason string) {
	mu.Lock()
	guardLock, guardWhy = true, reason
	mu.Unlock()
	once.Do(func() {
		go windowThread()
		go decodeThread()
	})
	enqueue(syncState)
}

func GuardUnlock() {
	mu.Lock()
	guardLock, guardWhy = false, ""
	mu.Unlock()
	enqueue(syncState)
}

// Close 只收广播（学生端「退出广播」也走这里），不影响任何锁定。
func Close() {
	mu.Lock()
	visible = false
	mu.Unlock()
	enqueue(syncState)
}

// hideAll 等同于 Close 的内部入口。
func hideAll() {
	mu.Lock()
	visible = false
	mu.Unlock()
	syncState()
}

// syncState 是唯一的状态汇点（窗口线程上跑）：广播、教师锁、守护锁
// 三组输入在这里算出四个输出，任何状态变化都统一走它：
//
//	wantVisible   = 广播中 || 任一锁定
//	wantFullscreen = 任一锁定 || 广播模式为 fullscreen
//	wantInputLock = 任一锁定 || （广播中且 fullscreen）
//	wantBar       = 广播中 && 无锁定（锁定不给学生工具条）
func syncState() {
	mu.Lock()
	view := deriveLockView(visible, teachLock, guardLock, mode)
	h := hwnd
	mu.Unlock()
	wantVisible := view.visible
	wantFullscreen := view.fullscreen
	wantInputLock := view.inputLock
	wantBar := view.bar

	sw, sh := win.GetSystemMetrics(win.SM_CXSCREEN), win.GetSystemMetrics(win.SM_CYSCREEN)
	style := uint32(win.WS_OVERLAPPEDWINDOW)
	x, y, w, ht := int32(60), int32(40), sw-120, sh-80
	if wantFullscreen {
		style = win.WS_POPUP
		x, y, w, ht = 0, 0, sw, sh
	}

	if !wantVisible {
		if h != 0 {
			win.ShowWindow(h, win.SW_HIDE)
		}
		mu.Lock()
		b := bar
		img = nil
		mu.Unlock()
		if b != 0 {
			win.ShowWindow(b, win.SW_HIDE)
		}
		unlockInput()
		return
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

	if wantInputLock {
		lockInput()
	} else {
		unlockInput()
	}
	if wantBar {
		showBar()
	} else {
		mu.Lock()
		b := bar
		mu.Unlock()
		if b != 0 {
			win.ShowWindow(b, win.SW_HIDE)
		}
	}
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
			syncState()
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
	defer restoreTaskbar() // 线程死了钩子自动拆，但任务栏藏过的要还回去
	// Create the first window before entering GetMessage. Show may enqueue its
	// follow-up mode update before hwnd exists, in which case there is no HWND
	// to wake with PostMessage; starting here makes the first call visible.
	syncState()
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
		l := locked()
		mu.Unlock()
		if isFS || l {
			return 0 // 全屏广播/锁定态不可关（退出走工具条或 screen.unlock）
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
		unlockInput() // 窗口没了无论如何放开键鼠和任务栏
		mu.Lock()
		hwnd = 0
		mu.Unlock()
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(h, umsg, wp, lp)
}

// paint 把最新帧拉伸画满客户区（黑边留宽高比）。锁定态画黑底+提示。
func paint(hdc win.HDC, rc win.RECT) {
	mu.Lock()
	src := img
	l, why := locked(), lockReason()
	mu.Unlock()
	if l {
		win.SetTextColor(hdc, 0x00DDDDDD)
		win.SetBkMode(hdc, win.TRANSPARENT)
		msg := "屏幕已锁定"
		if why != "" {
			msg = why
		}
		txt, _ := windows.UTF16FromString(msg)
		drawText(hdc, txt, &rc, win.DT_CENTER|win.DT_VCENTER|win.DT_SINGLELINE)
		return
	}
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
