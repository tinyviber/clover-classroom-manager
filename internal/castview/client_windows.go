//go:build windows

package castview

// agent 侧（父进程）的 viewer 客户端：同一组导出 API，内部把广播窗
// 跑在 `agent.exe -castview` 子进程里。设计要点：
//   - 按需拉起：有可见需求（广播/教师锁/守护锁）才 spawn，需求清空
//     发 opQuit 让子进程自行退出（钩子/窗口随进程消失，不会残留）；
//   - 崩溃自愈：子进程异常退出时若仍有可见需求，立即重拉并回放
//     当前期望状态（广播中 viewer 崩了 ~百毫秒级恢复）；
//   - 帧投递语义与进程内版本一致：单槽通道只发最新帧，子进程
//     消费慢时丢帧不阻塞 agent 的 TCP/组播读循环；
//   - 互斥纪律：clMu 只管进程句柄与期望状态，写管道走 writeMu，
//     绝不 clMu→writeMu 持锁嵌套（与 waitLoop 的清理路径互斥）。

import (
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type childProc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	quit bool // 我们主动发过 opQuit（clMu 内访问）
}

var (
	clMu      sync.Mutex
	child     *childProc
	wantShow  bool
	wantMode  string
	teachOn   bool
	wantTeach string
	guardOn   bool
	wantGuard string
	writeMu   sync.Mutex
	frameCh   = make(chan []byte, 1)
	lockedC   atomic.Bool
)

func anyDemand() bool { return wantShow || teachOn || guardOn }

// ensure 需要时拉起 viewer 子进程。返回是否可用（Disabled 时恒 false）。
// 拉起后会把当前期望状态回放给新进程，调用方只管照常发指令。
// 正在退出（已发 opQuit）的旧进程不算可用——直接另起新进程，
// 老的由它自己的 waitLoop 收割，两进程短暂共存无害。
func ensure() bool {
	if Disabled.Load() {
		return false
	}
	clMu.Lock()
	if child != nil && !child.quit {
		clMu.Unlock()
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		clMu.Unlock()
		return false
	}
	c := exec.Command(exe, "-castview")
	in, err := c.StdinPipe()
	if err != nil {
		clMu.Unlock()
		return false
	}
	out, err := c.StdoutPipe()
	if err != nil {
		_ = in.Close()
		clMu.Unlock()
		return false
	}
	if ErrSink != nil {
		c.Stderr = ErrSink
	}
	if err := c.Start(); err != nil {
		clMu.Unlock()
		return false
	}
	ch := &childProc{cmd: c, in: in}
	child = ch
	show, m, tOn, t, gOn, g := wantShow, wantMode, teachOn, wantTeach, guardOn, wantGuard
	clMu.Unlock()

	go readLoop(ch, out)
	go waitLoop(ch)
	// 回放期望状态到这一代子进程。固定写 ch.in 而非 curIn()：
	// child 若在我们解锁后被更新一代替换，把旧快照写进新进程会回滚状态。
	if show {
		sendTo(ch, opShow, []byte(m))
	}
	if tOn {
		sendTo(ch, opLock, []byte(t))
	}
	if gOn {
		sendTo(ch, opGuardLock, []byte(g))
	}
	return true
}

// sendTo 向指定那一代子进程写消息；写失败只杀该进程（收割与重启决策
// 仍由它的 waitLoop 负责）。
func sendTo(ch *childProc, op byte, p []byte) {
	writeMu.Lock()
	err := writeMsg(ch.in, op, p)
	writeMu.Unlock()
	if err != nil && ch.cmd.Process != nil {
		_ = ch.cmd.Process.Kill()
	}
}

// sendCtrl 发控制消息给当前代。无存活子进程时静默丢弃（状态已记在
// want*，下次 ensure 会回放）。
func sendCtrl(op byte, p []byte) {
	clMu.Lock()
	ch := child
	clMu.Unlock()
	if ch == nil {
		return
	}
	sendTo(ch, op, p)
}

// readLoop 收子进程事件。代际纪律：只有当前代的子进程事件才允许更新
// 全局锁定态/触发回调；旧代（已被替换、在退出路径上）的事件一律丢弃，
// 防止陈旧进程污染新一代状态。
func readLoop(ch *childProc, out io.ReadCloser) {
	for {
		ev, p, err := readMsg(out)
		if err != nil {
			return
		}
		clMu.Lock()
		current := child == ch
		clMu.Unlock()
		if !current {
			return
		}
		switch ev {
		case evExit:
			if f := OnExit; f != nil {
				go f()
			}
		case evUnlock:
			// 管理员紧急解锁对整机生效：同步清父侧期望锁状态，否则
			// 子进程崩溃重拉时会按旧 teachOn 回放 opLock 把学生机再锁上。
			clMu.Lock()
			teachOn, wantTeach, guardOn, wantGuard = false, "", false, ""
			lockedC.Store(false)
			clMu.Unlock()
			maybeRelease()
			if f := OnEmergencyUnlock; f != nil {
				go f()
			}
		case evLocked:
			if len(p) > 0 {
				lockedC.Store(p[0] != 0)
			}
		}
	}
}

// waitLoop 收割子进程：异常退出时清锁定缓存、兜底还任务栏、上报告警。
// 只有当前代退出才能清 child/锁定缓存并决定是否重拉——旧代退出时
// 新代可能已在锁定状态，无条件清 lockedC 会把它暂时覆盖为 false。
func waitLoop(ch *childProc) {
	err := ch.cmd.Wait()
	clMu.Lock()
	expected := err == nil || ch.quit
	stillCurrent := child == ch
	if stillCurrent {
		child = nil
	}
	demand := anyDemand()
	clMu.Unlock()

	if stillCurrent {
		lockedC.Store(false)
	}
	if !expected {
		showTaskbar() // 子进程若藏过任务栏没来得及还，父进程兜底
		log.Printf("castview: viewer 子进程异常退出: %v", err)
		if f := OnChildDied; f != nil {
			go f(err)
		}
	}
	if demand {
		go ensure()
	}
}

// 期望可见性清零 → 礼貌退出子进程（不阻塞等收割，waitLoop 异步处理）。
func maybeRelease() {
	clMu.Lock()
	ch := child
	release := ch != nil && !ch.quit && !anyDemand()
	if release {
		ch.quit = true
	}
	clMu.Unlock()
	if release {
		writeMu.Lock()
		_ = writeMsg(ch.in, opQuit, nil)
		_ = ch.in.Close()
		writeMu.Unlock()
	}
}

// frameWriter：帧走单槽通道，只发最新（子进程消费慢时直接丢，视频语义）。
func frameWriter() {
	for jpeg := range frameCh {
		clMu.Lock()
		ch := child
		clMu.Unlock()
		if ch == nil {
			continue
		}
		sendTo(ch, opFrame, jpeg)
	}
}

// Show 打开/切换广播窗。mode: "fullscreen" | "window"。
func Show(m string) {
	if Disabled.Load() {
		return
	}
	clMu.Lock()
	wantShow, wantMode = true, m
	clMu.Unlock()
	if ensure() {
		sendCtrl(opShow, []byte(m))
	}
}

// Frame 喂一帧 JPEG（无子进程/Disabled 时直接丢）。
func Frame(jpeg []byte) {
	if Disabled.Load() {
		return
	}
	select {
	case frameCh <- jpeg:
	default:
		select {
		case <-frameCh:
		default:
		}
		select {
		case frameCh <- jpeg:
		default:
		}
	}
}

// Close 只收广播（学生端「退出广播」也走这里），不影响任何锁定。
func Close() {
	clMu.Lock()
	wantShow = false
	clMu.Unlock()
	sendCtrl(opClose, nil)
	maybeRelease()
}

// Lock 教师黑屏锁定（screen.lock）。
func Lock(reason string) {
	if Disabled.Load() {
		return
	}
	clMu.Lock()
	teachOn, wantTeach = true, reason
	clMu.Unlock()
	if ensure() {
		sendCtrl(opLock, []byte(reason))
	}
}

// Unlock 解除教师锁（screen.unlock）。守护锁仍在则保持锁定。
func Unlock() {
	clMu.Lock()
	teachOn, wantTeach = false, ""
	clMu.Unlock()
	sendCtrl(opUnlock, nil)
	maybeRelease()
}

// GuardLock/GuardUnlock 断线守护锁：独立于教师锁，重连只解守护锁。
func GuardLock(reason string) {
	if Disabled.Load() {
		return
	}
	clMu.Lock()
	guardOn, wantGuard = true, reason
	clMu.Unlock()
	if ensure() {
		sendCtrl(opGuardLock, []byte(reason))
	}
}

func GuardUnlock() {
	clMu.Lock()
	guardOn, wantGuard = false, ""
	clMu.Unlock()
	sendCtrl(opGuardUnlock, nil)
	maybeRelease()
}

// Locked 当前是否处于锁定态（子进程 evLocked 回传的缓存）。
func Locked() bool { return lockedC.Load() }

func init() {
	go frameWriter()
}
