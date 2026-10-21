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

// Frame 收到一帧（丢弃）。
func Frame(jpeg []byte) {}

// Close 关闭广播窗。
func Close() { log.Printf("castview: close") }

// Lock/Unlock 黑屏锁定 stub（非 Windows 只记日志）。
func Lock(reason string) { log.Printf("castview: lock %q", reason) }
func Unlock()          { log.Printf("castview: unlock") }
