//go:build windows

package castview

import (
	"runtime"
	"testing"
)

// interactiveInputDesktop 判断当前进程能否打开输入桌面。
// 会话 0 的服务没有输入桌面，低级钩子装得上也不会收到键鼠。
func interactiveInputDesktop() bool {
	open := user32.NewProc("OpenInputDesktop")
	const desktopReadObjects = 0x0001
	h, _, _ := open.Call(0, 0, desktopReadObjects)
	if h == 0 {
		return false
	}
	user32.NewProc("CloseDesktop").Call(h)
	return true
}

func TestLowLevelHooksInstallAndRemove(t *testing.T) {
	if !interactiveInputDesktop() {
		t.Skip("没有交互式输入桌面（OpenInputDesktop 失败）。WH_KEYBOARD_LL/WH_MOUSE_LL 需要能跑消息循环的桌面会话；全屏窗口、任务栏隐藏和真实吞键留给有桌面的 Windows 机器验收")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := installInputHooks(); err != nil {
		t.Fatalf("低级钩子安装失败: %v", err)
	}
	if kbdHookH == 0 || mouHookH == 0 {
		removeInputHooks()
		t.Fatalf("钩子句柄为空 kbd=%#x mou=%#x", kbdHookH, mouHookH)
	}
	removeInputHooks()
	if kbdHookH != 0 || mouHookH != 0 {
		t.Fatal("钩子句柄未清空")
	}
	// 安装测试不隐藏任务栏。这里确认它也没有被误标成已隐藏。
	if taskbarHidden {
		t.Fatal("钩子安装测试不应改动任务栏")
	}
}
