package castview

import "time"

// 紧急解锁：Ctrl+Alt+Shift+F12 在 3 秒内连按 3 次。
// 锁死状态下管理员的本机兜底通道（教师端异常关闭/断网时救机器）。
const (
	vkF12     = 0x7B
	vkControl = 0x11
	vkShift   = 0x10
	vkMenu    = 0x12 // Alt
)

// emergencyChord 记录紧急解锁连按。低级钩子把按键喂进来；
// 计数本身不依赖桌面，测试可以直接驱动。
type emergencyChord struct {
	n       int
	last    time.Time
	f12Held bool
}

// key 处理一次键盘钩子事件。up 为抬起。返回 true 表示组合已完成，
// 调用方应解锁。长按自动重复不计数；缺少 Ctrl/Alt/Shift 的 F12 也不计数。
func (e *emergencyChord) key(vk uint32, up, ctrl, shift, alt bool, now time.Time) bool {
	if up {
		if vk == vkF12 {
			e.f12Held = false
		}
		return false
	}
	if vk != vkF12 || e.f12Held {
		return false
	}
	e.f12Held = true
	if !ctrl || !shift || !alt {
		return false
	}
	if now.Sub(e.last) > 3*time.Second {
		e.n = 0
	}
	e.last = now
	e.n++
	if e.n >= 3 {
		e.n = 0
		return true
	}
	return false
}
