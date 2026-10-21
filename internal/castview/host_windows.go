//go:build windows

package castview

// 广播窗子进程宿主：agent 用 `agent.exe -castview` 拉起本进程。
// stdin 收控制/帧消息（协议见 proto.go），stdout 回事件。
// 父进程死掉 → stdin 断 → 立即退出，学生机上不留孤儿全屏窗。
// stderr 由父进程接进 agent.log，这里 panic/log 都会留痕。

import (
	"log"
	"os"
	"sync"
	"time"
)

var hostSendMu sync.Mutex

func hostSend(op byte, payload []byte) {
	hostSendMu.Lock()
	defer hostSendMu.Unlock()
	_ = writeMsg(os.Stdout, op, payload)
}

// RunHost 是 `-castview` 模式的进程入口；返回进程退出码。
func RunHost() int {
	defer restoreTaskbar() // 任何退出路径都归还任务栏
	// 主循环 panic 必须非零退出——否则父进程把它当正常退出，
	// 异常重拉路径里该有的告警就被吞了
	defer fatalPanic("host")

	// 本进程内部用回调变量向父进程回事件（proto.go 声明的钩子
	// 在子进程侧就是管道事件）。
	OnExit = func() { hostSend(evExit, nil) }
	OnEmergencyUnlock = func() { hostSend(evUnlock, nil) }

	// 锁定态变化 → evLocked，供父进程 Locked() 缓存（心跳上报用）。
	go func() {
		var last bool
		for {
			l := lockedState()
			if l != last {
				last = l
				hostSend(evLocked, []byte{map[bool]byte{true: 1, false: 0}[l]})
			}
			time.Sleep(400 * time.Millisecond)
		}
	}()

	hostSend(evReady, nil)
	for {
		op, payload, err := readMsg(os.Stdin)
		if err != nil {
			// EOF（父进程正常关闭）或管道断裂（父进程崩溃）——都该自毁。
			log.Printf("castview: 父进程管道已断开（%v），退出", err)
			return 0
		}
		switch op {
		case opFrame:
			pushFrame(payload)
		case opShow:
			showView(string(payload))
		case opClose:
			closeView()
		case opLock:
			setTeachLock(string(payload))
		case opUnlock:
			clearTeachLock()
		case opGuardLock:
			setGuardLock(string(payload))
		case opGuardUnlock:
			clearGuardLock()
		case opQuit:
			return 0
		}
	}
}
