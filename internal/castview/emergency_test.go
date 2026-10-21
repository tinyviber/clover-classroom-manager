package castview

import (
	"testing"
	"time"
)

func TestEmergencyChord(t *testing.T) {
	var c emergencyChord
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	press := func(ctrl, shift, alt bool, at time.Time) bool {
		down := c.key(vkF12, false, ctrl, shift, alt, at)
		c.key(vkF12, true, ctrl, shift, alt, at.Add(time.Millisecond))
		return down
	}

	if press(false, false, false, now) {
		t.Fatal("单独的 F12 不应解锁")
	}
	if press(true, true, true, now) || press(true, true, true, now.Add(time.Second)) {
		t.Fatal("两次组合键不应解锁")
	}
	if !press(true, true, true, now.Add(2*time.Second)) {
		t.Fatal("3 秒内第三次组合键应解锁")
	}
	if press(true, true, true, now.Add(2500*time.Millisecond)) {
		t.Fatal("解锁后计数应归零，下一次按键不能接着上次")
	}

	// 长按自动重复：按住期间的重复 keydown 不计。
	var held emergencyChord
	if held.key(vkF12, false, true, true, true, now) {
		t.Fatal("第一次按下不应解锁")
	}
	if held.key(vkF12, false, true, true, true, now.Add(30*time.Millisecond)) {
		t.Fatal("按住不放的自动重复不应计成第二次")
	}
	held.key(vkF12, true, true, true, true, now.Add(40*time.Millisecond))
	if held.n != 1 {
		t.Fatalf("有效按下应只有 1 次，got %d", held.n)
	}

	// 间隔超过 3 秒重新开始。
	var slow emergencyChord
	pressAt := func(c *emergencyChord, at time.Time) bool {
		down := c.key(vkF12, false, true, true, true, at)
		c.key(vkF12, true, true, true, true, at.Add(time.Millisecond))
		return down
	}
	pressAt(&slow, now)
	pressAt(&slow, now.Add(4*time.Second))
	if pressAt(&slow, now.Add(5*time.Second)) {
		t.Fatal("超时后的第三次不应把更早的按键算进去")
	}
}
