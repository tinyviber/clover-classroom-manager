package castview

// 广播窗与 agent 之间的进程间协议（仿极域学生端把广播渲染做成独立
// 模块的思路）：viewer 跑在 `agent.exe -castview` 子进程里，Windows
// 窗口/钩子/逐帧解码的全部崩溃面与 agent 本体隔离——viewer 挂了
// agent 存活并可自动重拉，agent 挂了管道断裂子进程自毁，不会在
// 学生机上留下锁死的全屏孤儿窗。
//
// 父进程→子进程（子进程 stdin）：u32be 总长度(len) | u8 op | payload
// 子进程→父进程（子进程 stdout）：同格式，ev 事件。
// 长度含 op 字节与 payload；单条上限见 msgMax。

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
)

const (
	opFrame       = 1 // payload = JPEG
	opShow        = 2 // payload = mode ("fullscreen"|"window")
	opClose       = 3
	opLock        = 4 // payload = reason
	opUnlock      = 5
	opGuardLock   = 6 // payload = reason
	opGuardUnlock = 7
	opQuit        = 8
)

const (
	evReady  = 1 // 子进程就绪
	evExit   = 2 // 学生端「退出广播」
	evUnlock = 3 // 紧急解锁触发
	evLocked = 4 // payload = 1B：当前是否处于锁定态
)

const msgMax = 4 << 20 // 单条消息上限（MaxCastJPEG 2.6MB + 余量）

func writeMsg(w io.Writer, op byte, payload []byte) error {
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(payload)+1))
	hdr[4] = op
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readMsg(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[:4]))
	if n < 1 || n > msgMax {
		return 0, nil, fmt.Errorf("castview: 非法消息长度 %d", n)
	}
	payload := make([]byte, n-1)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[4], payload, nil
}

// ---- agent 侧钩子与子进程管理变量 ----

// OnExit 学生端悬浮条「退出广播」回调（agent 用来停掉组播接收）。
// 子进程模式下由父进程在收到 evExit 事件时调用，不许阻塞。
var OnExit func()

// OnEmergencyUnlock 紧急解锁组合键触发时回调（agent 上报管理端）。不许阻塞。
var OnEmergencyUnlock func()

// OnChildDied 子进程异常退出回调（agent 上报管理端告警）。不许阻塞。
var OnChildDied func(err error)

// Disabled 为真时全部导出 API 变 no-op——管理端进程内演示 agent 用：
// 否则会拿管理端 exe 去起 -castview 子进程。
var Disabled atomic.Bool

// ErrSink 子进程 stderr 的去向（cmd/client 接 agent.log），nil 时丢弃。
var ErrSink io.Writer
