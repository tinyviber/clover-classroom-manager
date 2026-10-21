//go:build !windows

package castview

import (
	"log"
	"sync"
)

var once sync.Once

// Show 开发 stub：只记日志（无窗口）。
func Show(mode string) {
	once.Do(func() { log.Printf("castview: 非 Windows 平台，广播窗以 stub 运行") })
	log.Printf("castview: show %s", mode)
}

// OnExit 学生端「退出广播」回调（非 Windows 无悬浮条，占位）。
var OnExit func()

// OnEmergencyUnlock 紧急解锁回调（非 Windows 无键鼠钩子，占位）。
var OnEmergencyUnlock func()

// Frame 收到一帧（丢弃）。
func Frame(jpeg []byte) {}

// Close 关闭广播窗。
func Close() { log.Printf("castview: close") }

// Locked 是否处于锁定态（stub 记录教师锁/守护锁供状态上报）。
var (
	stubMu        sync.Mutex
	stubTeachLock bool
	stubGuardLock bool
)

func Locked() bool {
	stubMu.Lock()
	defer stubMu.Unlock()
	return stubTeachLock || stubGuardLock
}

// Lock/Unlock 教师黑屏锁定 stub（非 Windows 只记日志）。
func Lock(reason string) {
	stubMu.Lock()
	stubTeachLock = true
	stubMu.Unlock()
	log.Printf("castview: lock %q", reason)
}
func Unlock() {
	stubMu.Lock()
	stubTeachLock = false
	stubMu.Unlock()
	log.Printf("castview: unlock")
}

// GuardLock/GuardUnlock 断线守护锁 stub。
func GuardLock(reason string) {
	stubMu.Lock()
	stubGuardLock = true
	stubMu.Unlock()
	log.Printf("castview: guard lock %q", reason)
}
func GuardUnlock() {
	stubMu.Lock()
	stubGuardLock = false
	stubMu.Unlock()
	log.Printf("castview: guard unlock")
}
