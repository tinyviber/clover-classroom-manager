//go:build windows

package trayicon

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	niifInfo   = 0x01

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	wmTray   = win.WM_USER + 200 // 托盘回调消息
	wmInvoke = win.WM_USER + 201 // 跨线程投递到托盘线程
)

var (
	shell32         = windows.NewLazySystemDLL("shell32.dll")
	shellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	user32          = windows.NewLazySystemDLL("user32.dll")
	appendMenuW     = user32.NewProc("AppendMenuW")
	createIconEx    = user32.NewProc("CreateIconFromResourceEx")
	registerMsgW    = user32.NewProc("RegisterWindowMessageW")
)

var classSeq atomic.Uint32

// Tray 一个托盘图标实例。整个生命周期在内部消息线程上跑。
type Tray struct {
	hwnd           win.HWND
	icon           win.HICON
	mu             sync.Mutex
	items          []Item
	tip            string
	ready          chan struct{}
	done           chan struct{} // 消息线程退出信号（Close 等待用）
	taskbarCreated uint32        // explorer 重启广播消息号
}

// Supported 表示当前平台有真实托盘（仅 Windows）。
func Supported() bool { return true }

// New 创建托盘图标。ico 为 ICO 文件字节（可用包内 IconICO），tip 为悬浮提示。
func New(ico []byte, tip string) (*Tray, error) {
	t := &Tray{tip: tip, ready: make(chan struct{}), done: make(chan struct{})}
	go t.thread(ico)
	<-t.ready
	if t.hwnd == 0 {
		return nil, errFailed
	}
	return t, nil
}

var errFailed = errorf("托盘图标创建失败")

func errorf(s string) error { return &trayErr{s} }

type trayErr struct{ s string }

func (e *trayErr) Error() string { return e.s }

// SetTooltip 更新悬浮提示（128 字符截断由系统处理）。
func (t *Tray) SetTooltip(tip string) {
	t.mu.Lock()
	t.tip = tip
	t.mu.Unlock()
	t.invoke(func() {
		nid := t.nid()
		nid.UFlags = nifTip
		copyU16(nid.SzTip[:], tip)
		shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	})
}

// SetMenu 替换右键菜单。
func (t *Tray) SetMenu(items []Item) {
	t.mu.Lock()
	t.items = append([]Item(nil), items...)
	t.mu.Unlock()
}

// Notify 弹一条气泡通知（Win10+ 显示为 toast）。
func (t *Tray) Notify(title, text string) {
	t.invoke(func() {
		nid := t.nid()
		nid.UFlags = nifInfo
		nid.DwInfoFlags = niifInfo
		copyU16(nid.SzInfoTitle[:], title)
		copyU16(nid.SzInfo[:], text)
		shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	})
}

// Close 删除托盘图标并停掉消息线程（等线程跑完清理再返回，防止
// 进程在 NIM_DELETE 投递前退出留下幽灵图标）。
func (t *Tray) Close() {
	t.invoke(func() {
		nid := t.nid()
		shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		if t.icon != 0 {
			win.DestroyIcon(t.icon)
			t.icon = 0
		}
		if t.hwnd != 0 {
			win.DestroyWindow(t.hwnd)
			t.hwnd = 0
		}
		win.PostQuitMessage(0)
	})
	select {
	case <-t.done:
	case <-time.After(500 * time.Millisecond):
	}
}

func (t *Tray) nid() win.NOTIFYICONDATA {
	var nid win.NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	t.mu.Lock()
	nid.HWnd = t.hwnd
	nid.HIcon = t.icon
	copyU16(nid.SzTip[:], t.tip)
	t.mu.Unlock()
	nid.UID = 1
	nid.UCallbackMessage = wmTray
	return nid
}

// invoke 把 fn 投递到托盘消息线程执行（fire-and-forget）。
func (t *Tray) invoke(fn func()) {
	t.mu.Lock()
	h := t.hwnd
	t.mu.Unlock()
	if h == 0 {
		return
	}
	trayCmdsMu.Lock()
	trayCmds[uintptr(h)] = append(trayCmds[uintptr(h)], fn)
	trayCmdsMu.Unlock()
	win.PostMessage(h, wmInvoke, 0, 0)
}

// 按 hwnd 排队的投递队列（托盘实例极少，map 足够）。
var (
	trayCmds   = map[uintptr][]func(){}
	trayCmdsMu sync.Mutex
)

func (t *Tray) thread(ico []byte) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)

	// 每实例独立窗口类：LpfnWndProc 绑的是本实例的闭包，共享类名
	// 会让第二个 Tray 的消息走第一个的 wndProc。
	seq := classSeq.Add(1)
	clsName, _ := windows.UTF16PtrFromString(fmt.Sprintf("CloverTray%d", seq))
	wc := win.WNDCLASSEX{
		LpfnWndProc:   windows.NewCallback(t.wndProc),
		HInstance:     win.GetModuleHandle(nil),
		LpszClassName: clsName,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if win.RegisterClassEx(&wc) == 0 {
		close(t.ready)
		return
	}
	// 不用 HWND_MESSAGE：message-only 窗口收不到广播消息（如
	// TaskbarCreated）。普通隐藏窗口（父=桌面、永不显示）可以。
	h := win.CreateWindowEx(0, clsName, nil, win.WS_OVERLAPPED,
		0, 0, 0, 0, 0, 0, wc.HInstance, nil)
	if h == 0 {
		close(t.ready)
		return
	}
	t.mu.Lock()
	t.hwnd = h
	t.mu.Unlock()

	if len(ico) > 0 {
		ret, _, _ := createIconEx.Call(
			uintptr(unsafe.Pointer(&ico[0])), uintptr(len(ico)),
			1, 0x00030000, 16, 16, 0)
		t.mu.Lock()
		t.icon = win.HICON(ret)
		t.mu.Unlock()
	}
	t.mu.Lock()
	if t.icon == 0 {
		t.icon = win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION))
	}
	t.mu.Unlock()

	nid := t.nid()
	nid.UFlags = nifMessage | nifIcon | nifTip
	ret, _, _ := shellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		win.DestroyWindow(h)
		t.mu.Lock()
		t.hwnd = 0
		t.mu.Unlock()
		close(t.ready)
		return
	}
	// 刻意不调 NIM_SETVERSION：v0 语义下托盘回调的 lParam 就是原始
	// 鼠标消息（WM_RBUTTONUP 等）；v4 会把它重打包成事件号+图标ID。
	// explorer 重启后需重新 NIM_ADD。
	if name, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		r, _, _ := registerMsgW.Call(uintptr(unsafe.Pointer(name)))
		t.taskbarCreated = uint32(r)
	}

	close(t.ready)

	var m win.MSG
	for win.GetMessage(&m, 0, 0, 0) > 0 {
		win.TranslateMessage(&m)
		win.DispatchMessage(&m)
	}
}

func (t *Tray) wndProc(h win.HWND, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmInvoke:
		trayCmdsMu.Lock()
		fns := trayCmds[uintptr(h)]
		delete(trayCmds, uintptr(h))
		trayCmdsMu.Unlock()
		for _, fn := range fns {
			fn()
		}
		return 0
	case wmTray:
		switch uint32(lp) {
		case win.WM_RBUTTONUP:
			t.showMenu()
		}
		return 0
	case win.WM_DESTROY:
		return 0
	}
	if msg == t.taskbarCreated && t.taskbarCreated != 0 {
		// explorer.exe 重启：托盘区被清空，重新 NIM_ADD。
		nid := t.nid()
		nid.UFlags = nifMessage | nifIcon | nifTip
		shellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		return 0
	}
	return win.DefWindowProc(h, msg, wp, lp)
}

func (t *Tray) showMenu() {
	t.mu.Lock()
	items := append([]Item(nil), t.items...)
	t.mu.Unlock()
	if len(items) == 0 {
		return
	}
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)
	for i, it := range items {
		if it.Separator {
			appendMenuW.Call(uintptr(menu), mfSeparator, 0, 0)
			continue
		}
		flags := mfString
		if it.Disabled {
			flags |= mfGrayed
		}
		if it.Checked {
			flags |= mfChecked
		}
		label, _ := windows.UTF16PtrFromString(it.Label)
		appendMenuW.Call(uintptr(menu), uintptr(flags), uintptr(i+1),
			uintptr(unsafe.Pointer(label)))
	}
	var pt win.POINT
	win.GetCursorPos(&pt)
	win.SetForegroundWindow(t.hwnd)
	cmd := win.TrackPopupMenu(menu,
		win.TPM_RETURNCMD|win.TPM_NONOTIFY|win.TPM_RIGHTBUTTON,
		pt.X, pt.Y, 0, t.hwnd, nil)
	win.PostMessage(t.hwnd, 0, 0, 0) // 让菜单正确收回
	if cmd > 0 && int(cmd) <= len(items) {
		if fn := items[cmd-1].OnClick; fn != nil {
			go fn()
		}
	}
}

// copyU16 把字符串拷进定长 UTF16 数组（自动截断，尾部留 NUL）。
func copyU16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst)-1 {
		u = u[:len(dst)-1]
	}
	copy(dst, u)
}
