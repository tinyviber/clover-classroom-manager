package lan

// 客户端 agent：跑在学生机上。
// 发现管理端（UDP 广播 HELLO → 信标）→ TCP 连接 → 签名握手 → hello →
// 心跳上行 + 指令下行。断线自动回到发现阶段重连。
// DirectAddr 非空时优先直连；管理端不可达时用固定公钥验证 UDP 发现结果并更新地址。

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tinyviber/clover-classroom-manager/internal/capture"
	"github.com/tinyviber/clover-classroom-manager/internal/castview"
)

// Agent 是客户端代理。
type Agent struct {
	ID, Name, Label, MAC, IP, Version, Token string

	// PinnedPub 管理端公钥。nil 表示 TOFU：信任第一个应答的信标。
	PinnedPub ed25519.PublicKey
	// DirectAddr 形如 "192.168.31.1:47823"，先直连；失败后回退到 UDP 发现。
	// 只能在构造期赋值；运行期（含 UDP 发现更新）一律走 directMu。
	DirectAddr string
	directMu   sync.RWMutex
	// RecvDir 教师下发文件的落盘目录（默认 exe 旁 recv/）。
	RecvDir string

	// OnCommand 可选钩子：自定义指令处理。返回 false 表示已由 agent 内置处理。
	OnCommand func(CommandMsg) bool
	// PersistPub 可选钩子：TOFU 学到的管理端公钥（PinnedPub 原本为空时
	// 第一次握手验证通过的那个）交给调用方落盘 client.json——否则每次
	// 断线重发现都在重新「首次信任」，TOFU 名不副实。
	PersistPub func(ed25519.PublicKey)
	// PersistState 可选钩子：防护策略（helperOn + features）由管理端
	// 下发后回写 client.json，agent 重启后仍按上次配置生效。
	PersistState func(helperOn bool, features []string)
	// OnPrompt 可选钩子：有新的课堂互动（"signin"/"poll"/"demo"/"collect"）待学生处理。
	// 静默运行的客户端用它做气泡通知 + 唤起互动窗口。
	OnPrompt func(kind, session string)
	// OnNotify 可选钩子：教师发来的文字消息（气泡通知）。
	OnNotify func(text string)
	// OnConn 可选钩子：管理端会话建立/断开。
	OnConn func(connected bool)

	helperOn atomic.Bool
	features atomic.Value // []string
	featErrs atomic.Value // []string——应用失败的策略键
	watchHi  bool         // 单机高清查看模式（教师点开大图），watchMu 保护

	// guard_offline 守护：断线超时后自动锁屏。guardLocked 只记守护自己
	// 上的锁，解锁也只解自己的（教师 screen.lock 独立管理）。
	guardMu     sync.Mutex
	guardTimer  *time.Timer
	guardLocked bool

	// pendingAlerts 离线时缓存的告警（紧急解锁发生在断网时也要在
	// 重连后送达管理端），下条会话建立时 flush。
	pendMu     sync.Mutex
	pendAlerts []AlertMsg

	// castGen 当前广播代数：只接受更大代的 cast.start，小于等于当前代的
	// cast.stop 才生效。每条 TCP 会话结束时清零（服务端换代语义按会话计）。
	castGen atomic.Uint32

	// powerTimer 关机/重启的 agent 侧可取消倒计时：Windows 的
	// shutdown /t>0 隐含 /f 强关应用，所以普通关机是我们自己的定时器，
	// 到期执行不带 /f 的 shutdown /t 0（应用可拒绝）。power.cancel 停它。
	// powerGen 是排程代数：回调只在 gen 仍是当前代时才执行，防止旧
	// 定时器回调把新排程清掉或错过取消窗口。timer 一旦进入执行阶段
	// （gen 消费）就不能再取消——取消接口如实返回 false。
	powerMu    sync.Mutex
	powerTimer *time.Timer
	powerGen   uint64

	cur           atomic.Pointer[session] // 当前会话（SubmitFile 用）
	watchMu       sync.Mutex
	watchOff      context.CancelFunc
	demoOff       context.CancelFunc
	castOff       context.CancelFunc // 组播接收循环取消
	inFiles       map[string]*incoming
	inMu          sync.Mutex
	outMu         sync.Mutex
	outgoing      map[string]chan FileMsg
	uiMu          sync.Mutex
	signPrompt    *SignInOpenArgs
	pollPrompt    *PollOpenArgs
	demoPrompt    *DemoRequestArgs
	collectPrompt *CollectOpenArgs
	demoSession   string
	helpRaised    bool
}

// NewAgent 构造一个默认全开防护的 agent。
func NewAgent(id, name string) *Agent {
	a := &Agent{ID: id, Name: name, Version: "0.0.2", Token: RandHex(16), inFiles: map[string]*incoming{}, outgoing: map[string]chan FileMsg{}}
	castview.OnEmergencyUnlock = a.onEmergencyUnlock
	a.helperOn.Store(true)
	a.features.Store([]string{})
	a.featErrs.Store([]string{})
	return a
}

// HelperOn 返回当前防护开关。
func (a *Agent) HelperOn() bool { return a.helperOn.Load() }

// Features 返回当前启用功能键集合。
func (a *Agent) Features() []string { return a.features.Load().([]string) }

// SetHelper 本地开关防护（管理端指令或客户端逻辑调用）。
func (a *Agent) SetHelper(on bool) { a.helperOn.Store(on) }

// SetFeatures 替换功能集。
func (a *Agent) SetFeatures(keys []string) { a.features.Store(keys) }

func (a *Agent) hasFeature(key string) bool {
	for _, k := range a.Features() {
		if k == key {
			return true
		}
	}
	return false
}

// enforce 把已启用的功能真正落地到本机，执行失败的键记入
// featErrs 上报管理端（desired→applied→reported 分开）。目前落地：
//   - lock_taskmgr：HKCU DisableTaskMgr
//   - guard_offline：断线 15s 自动锁屏（计时在 arm/disarm 里管）
//
// 其余 catalog 项尚未实现，服务端目录里标 ready=false 不展示为已生效。
func (a *Agent) enforce() {
	on := a.helperOn.Load()
	var errs []string
	if err := setDisableTaskMgr(on && a.hasFeature("lock_taskmgr")); err != nil {
		errs = append(errs, "lock_taskmgr")
		log.Printf("agent[%s]: DisableTaskMgr 策略写入失败: %v", a.ID, err)
	}
	if !(on && a.hasFeature("guard_offline")) {
		a.disarmOfflineGuard()
	}
	a.featErrs.Store(errs)
}

// offlineGuardDelay 断线守护上弦后的锁屏延迟（测试可缩短）。
var offlineGuardDelay = 15 * time.Second

// armOfflineGuard 在会话断开后上弦：guard_offline 开启时 15s 内没
// 重连成功就锁屏（防拔网线脱控）。ctx 结束（agent 退出）不上弦。
func (a *Agent) armOfflineGuard(ctx context.Context) {
	if ctx.Err() != nil || !a.offlineGuardOn() {
		return
	}
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	if a.guardLocked || a.guardTimer != nil {
		return
	}
	a.guardTimer = time.AfterFunc(offlineGuardDelay, func() {
		a.guardMu.Lock()
		a.guardTimer = nil
		if a.offlineGuardOn() {
			a.guardLocked = true
			a.guardMu.Unlock()
			log.Printf("agent[%s]: 与管理端断线超时，锁定屏幕", a.ID)
			castview.GuardLock("与管理端的连接已断开")
			return
		}
		a.guardMu.Unlock()
	})
}

// disarmOfflineGuard 重连成功（或功能关闭）后解除守护：停计时器，
// 并解开守护自己上的锁。
func (a *Agent) disarmOfflineGuard() {
	a.guardMu.Lock()
	if a.guardTimer != nil {
		a.guardTimer.Stop()
		a.guardTimer = nil
	}
	locked := a.guardLocked
	a.guardLocked = false
	a.guardMu.Unlock()
	if locked {
		castview.GuardUnlock() // 只解守护自己的锁，教师锁不受影响
	}
}

func (a *Agent) offlineGuardOn() bool {
	return a.helperOn.Load() && a.hasFeature("guard_offline")
}

// onEmergencyUnlock 本机紧急解锁（Ctrl+Alt+Shift+F12×3，管理员恢复通道）：
// ① 同步解开 agent 侧的守护锁标记——不然 a.guardLocked 滞留会让
// armOfflineGuard 永远不再上弦，解锁就成了一次性豁免；
// ② 上报告警，离线则缓存到重连后补发。
func (a *Agent) onEmergencyUnlock() {
	log.Printf("agent[%s]: 本机触发紧急解锁组合键", a.ID)
	a.disarmOfflineGuard() // 与 castview 的解锁保持一致，守护下次断线仍生效
	a.reportAlert(AlertMsg{Kind: "emergency_unlock", Detail: "本机紧急解锁"})
	if s := a.cur.Load(); s != nil {
		_ = a.pushStatus(s)
	}
}

// ReportAlert 供 agent 宿主（cmd/client）上报自定义告警（如策略回写失败）。
func (a *Agent) ReportAlert(kind, detail string) {
	a.reportAlert(AlertMsg{Kind: kind, Detail: detail})
}

// reportAlert 上报告警；离线时缓存（上限 8 条防膨胀），重连补发。
func (a *Agent) reportAlert(alert AlertMsg) {
	if s := a.cur.Load(); s != nil {
		if err := s.send(Msg{Type: MsgAlert, Alert: &alert}); err == nil {
			return
		}
	}
	a.pendMu.Lock()
	if len(a.pendAlerts) < 8 {
		a.pendAlerts = append(a.pendAlerts, alert)
	}
	a.pendMu.Unlock()
}

// flushAlerts 逐条补发离线缓存的告警；发送失败时把【未发出】的放回
// 队列等下条会话再试——重连即断的抖动不能永久丢紧急解锁告警。
func (a *Agent) flushAlerts(s *session) {
	for {
		a.pendMu.Lock()
		if len(a.pendAlerts) == 0 {
			a.pendMu.Unlock()
			return
		}
		alert := a.pendAlerts[0]
		a.pendMu.Unlock()
		if err := s.send(Msg{Type: MsgAlert, Alert: &alert}); err != nil {
			return // 连接又挂了：整个队列（含本条）保留下次重发
		}
		a.pendMu.Lock()
		a.pendAlerts = a.pendAlerts[1:]
		a.pendMu.Unlock()
	}
}

// Run 主循环：发现→连接→会话，断线重来。
func (a *Agent) Run(ctx context.Context) {
	a.enforce() // 启动即落地本地策略（例如上次关机前残留的 taskmgr 禁用要清掉）
	for ctx.Err() == nil {
		addr, pub, err := a.discover(ctx)
		if err != nil {
			log.Printf("agent[%s]: 发现失败: %v", a.ID, err)
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if err := a.session(ctx, addr, pub); err != nil {
			log.Printf("agent[%s]: 会话结束: %v", a.ID, err)
			if ctx.Err() == nil && a.directAddr() != "" && a.PinnedPub != nil {
				discoverCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				foundAddr, foundPub, discoverErr := a.discoverBroadcast(discoverCtx, false)
				cancel()
				if discoverErr == nil && a.PinnedPub.Equal(foundPub) {
					if foundAddr != a.directAddr() {
						log.Printf("agent[%s]: 管理端地址已更新 %s → %s", a.ID, a.directAddr(), foundAddr)
						a.setDirectAddr(foundAddr)
					}
				} else if discoverErr != nil {
					log.Printf("agent[%s]: UDP 发现回退失败: %v", a.ID, discoverErr)
				}
			}
			sleepCtx(ctx, 2*time.Second)
		}
	}
}

func (a *Agent) directAddr() string {
	a.directMu.RLock()
	defer a.directMu.RUnlock()
	return a.DirectAddr
}

func (a *Agent) setDirectAddr(addr string) {
	a.directMu.Lock()
	a.DirectAddr = addr
	a.directMu.Unlock()
}

// discover 广播找管理端，返回 (tcpAddr, adminPub)。
func (a *Agent) discover(ctx context.Context) (string, ed25519.PublicKey, error) {
	if addr := a.directAddr(); addr != "" && a.PinnedPub != nil {
		return addr, a.PinnedPub, nil
	}
	return a.discoverBroadcast(ctx, true)
}

func (a *Agent) discoverBroadcast(ctx context.Context, useConfiguredAddr bool) (string, ed25519.PublicKey, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var setErr error
		c.Control(func(fd uintptr) {
			setErr = setBroadcastOpt(fd)
		})
		return setErr
	}}
	pc, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return "", nil, err
	}
	defer pc.Close()
	// 调用方的超时（断线后的 3s 回退）必须能打断 ReadFromUDP。
	// 否则一次读超时刚好卡在 ctx 到期前，循环会再挂上一个 3s deadline，
	// 发现可以拖到 6s，重连和测试都会错过窗口。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = pc.Close()
		case <-done:
		}
	}()
	uc := pc.(*net.UDPConn)

	bcast := &net.UDPAddr{IP: net.IPv4bcast, Port: UDPPort}
	buf := make([]byte, 128)
	for ctx.Err() == nil {
		// 未 pin 公钥时仍需等一个信标取得握手公钥。
		if _, err := uc.WriteToUDP([]byte(DiscoverMagic), bcast); err != nil {
			return "", nil, fmt.Errorf("广播失败: %w", err)
		}
		uc.SetReadDeadline(deadline(3))
		n, src, err := uc.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if b, ok := DecodeBeacon(buf[:n]); ok {
			pub := ed25519.PublicKey(b.Pub)
			if a.PinnedPub != nil && !a.PinnedPub.Equal(pub) {
				log.Printf("agent[%s]: 信标公钥不匹配 pin，忽略", a.ID)
				continue
			}
			addr := fmt.Sprintf("%s:%d", src.IP.String(), b.TCPPort)
			if useConfiguredAddr && a.directAddr() != "" {
				addr = a.directAddr()
			}
			return addr, pub, nil
		}
	}
	return "", nil, ctx.Err()
}

// session 维持一条管理端会话：hello → 心跳 + 指令循环。
func (a *Agent) session(ctx context.Context, addr string, pub ed25519.PublicKey) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	sess, _, err := clientHandshake(conn, pub)
	if err != nil {
		return err
	}
	if a.PinnedPub == nil {
		// TOFU：握手已验证对端持有该公钥对应的私钥——钉住并持久化，
		// 以后重连/重发现都认这把钥匙。
		a.PinnedPub = pub
		if a.PersistPub != nil {
			a.PersistPub(pub)
		}
	}
	defer func() {
		a.cur.Store(nil)
		a.emitConn(false)
		a.stopWatch()
		a.stopDemoStream()
		a.stopCast()
		a.castGen.Store(0) // 会话结束，广播代数随连接重置
		a.abortIncoming()
		a.armOfflineGuard(ctx) // 断线守护上弦（ctx 已结束则不上）
		a.uiMu.Lock()
		a.signPrompt, a.pollPrompt, a.demoPrompt, a.collectPrompt = nil, nil, nil, nil
		a.demoSession, a.helpRaised = "", false
		a.uiMu.Unlock()
		castview.Close()
	}()

	ip := a.IP
	if ip == "" {
		ip = localIP(conn)
	}
	if err := sess.send(Msg{Type: MsgHello, Hello: &HelloMsg{
		ID: a.ID, Name: a.Name, Label: a.Label, IP: ip,
		MAC: a.MAC, Version: a.Version, Token: a.Token,
	}}); err != nil {
		return err
	}
	if err := a.pushStatus(sess); err != nil {
		return err
	}
	// hello 之后再公开当前会话：抢跑窗口期的 SubmitFile 等
	// 在服务端见到 hello 前发包会被直接断连。
	a.cur.Store(sess)
	a.emitConn(true)
	a.disarmOfflineGuard() // 重连成功，解除断线锁屏守护
	a.flushAlerts(sess)    // 补发断线期间缓存的告警（紧急解锁等）

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.conn.Close()
		case <-done:
		}
	}()
	// 心跳 goroutine
	go func() {
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				if err := a.pushStatus(sess); err != nil {
					sess.conn.Close()
					return
				}
			}
		}
	}()

	for {
		var m Msg
		if err := sess.recv(&m); err != nil {
			return err
		}
		switch m.Type {
		case MsgCommand:
			if m.Cmd != nil {
				a.handleCmd(sess, *m.Cmd)
			}
		case MsgCast:
			if m.Cast != nil {
				if jpeg, err := base64.StdEncoding.DecodeString(m.Cast.JPEG); err == nil && len(jpeg) <= MaxCastJPEG {
					castview.Frame(jpeg)
				}
			}
		case MsgFile:
			if m.File != nil {
				a.onFileDown(sess, *m.File)
			}
		}
	}
}

func (a *Agent) pushStatus(s *session) error {
	return s.send(Msg{Type: MsgStatus, Status: &StatusMsg{
		HelperOn:   a.helperOn.Load(),
		Features:   a.features.Load().([]string),
		Locked:     castview.Locked(),
		FeatureErr: a.featErrs.Load().([]string),
	}})
}

func (a *Agent) handleCmd(s *session, c CommandMsg) {
	ack := AckMsg{ID: c.ID, OK: true}
	handled := true
	switch c.Op {
	case OpSetHelper:
		var args SetHelperArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.helperOn.Store(args.On)
			a.enforce()
			a.persistState()
		}
	case OpSetFeatures:
		var args SetFeaturesArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.features.Store(args.Keys)
			a.enforce()
			a.persistState()
		}
	case OpScreenWatch:
		var args ScreenWatchArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.setWatch(args.On, args.Hi)
		}
	case OpCastStart:
		var args CastStartArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			cur := a.castGen.Load()
			switch {
			case args.Gen != 0 && args.Gen < cur:
				// 过期指令：教师早已换代，回了 "stale" 服务端不做 TCP 兜底
				ack.Detail = "stale"
			case args.Gen == cur && cur != 0:
				castview.Show(args.Mode) // 同代重发：幂等只刷新模式
			case args.Mode != "window" && args.Mode != "fullscreen":
				ack.OK, ack.Err = false, "无效的广播模式"
				ack.Detail = "tcp" // 拒绝广播也走兜底，不留黑屏
			default:
				a.castGen.Store(args.Gen)
				if err := a.startCast(s, args); err != nil {
					// 组播不可用 → 回 "tcp"，管理端把这机标记为 TCP 兜底
					ack.Detail = "tcp"
				}
			}
		} else {
			ack.Detail = "tcp" // 参数解不开：退到 TCP 也不留黑屏
		}
	case OpCastStop:
		var args CastStopArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			// 只停对应代的广播；过期 stop（Gen 比当前小）直接忽略，
			// 防旧 cast.stop 晚到把新一代广播掐掉。
			if args.Gen == 0 || args.Gen >= a.castGen.Load() {
				a.stopCast()
				castview.Close()
			}
		} else {
			a.stopCast()
			castview.Close()
		}
	case OpLockScreen:
		var args LockScreenArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			castview.Lock(args.Reason)
		} else {
			castview.Lock("")
		}
	case OpUnlockScreen:
		castview.Unlock()
	case OpRestart, OpShutdown:
		var args PowerArgs
		_ = json.Unmarshal(mustJSON(c.Args), &args)
		if err := a.schedulePower(c.Op, args.Force); err != nil {
			ack.OK, ack.Err = false, err.Error()
		}
	case OpPowerCancel:
		// 两个通道都算：agent 侧普通倒计时 + 系统级 force 关机
		// （shutdown /a）。任一路径真正取消成功就算成功——教师看到的是
		// 是否还有关机在进行，而不是哪一层取消了它。
		canceled := a.cancelScheduledPower()
		sysErr := powerCancel() // /a 没有可取消的关机时返回非零
		if !canceled && sysErr != nil {
			ack.OK, ack.Err = false, "没有可取消的关机倒计时（未排程或已开始执行）"
		}
	case OpNotify:
		var args NotifyArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			text := strings.TrimSpace(args.Text)
			if text == "" || len([]rune(text)) > 200 {
				ack.OK, ack.Err = false, "消息为空或超过 200 字"
				break
			}
			if a.OnNotify != nil {
				a.OnNotify(text)
			} else {
				log.Printf("agent[%s]: 教师消息: %s", a.ID, text)
			}
		}
	case OpOpenURL:
		var args OpenURLArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			if err := validOpenURL(args.URL); err != nil {
				ack.OK, ack.Err = false, err.Error()
			} else if err := openURL(args.URL); err != nil {
				ack.OK, ack.Err = false, err.Error()
			}
		} else {
			ack.OK, ack.Err = false, "无效的 URL 参数"
		}
	case OpLaunchApp:
		var args LaunchArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			if err := validLaunchPath(args.Path); err != nil {
				ack.OK, ack.Err = false, err.Error()
			} else if err := launchApp(args.Path); err != nil {
				ack.OK, ack.Err = false, err.Error()
			}
		} else {
			ack.OK, ack.Err = false, "无效的程序路径"
		}
	case OpSignInOpen:
		var args SignInOpenArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.uiMu.Lock()
			a.signPrompt = &args
			a.uiMu.Unlock()
			a.emitPrompt("signin", args.Session)
		}
	case OpSignInClose:
		a.uiMu.Lock()
		a.signPrompt = nil
		a.uiMu.Unlock()
	case OpClearHelp:
		a.uiMu.Lock()
		a.helpRaised = false
		a.uiMu.Unlock()
	case OpSetHelpState:
		var args HelpStateArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.uiMu.Lock()
			a.helpRaised = args.Raised
			a.uiMu.Unlock()
		}
	case OpPollOpen:
		var args PollOpenArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.uiMu.Lock()
			a.pollPrompt = &args
			a.uiMu.Unlock()
			a.emitPrompt("poll", args.Session)
		}
	case OpPollClose:
		a.uiMu.Lock()
		a.pollPrompt = nil
		a.uiMu.Unlock()
	case OpCollectOpen:
		var args CollectOpenArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil && args.Session != "" {
			a.uiMu.Lock()
			a.collectPrompt = &args
			a.uiMu.Unlock()
			if args.Dir != "" {
				// 指定目录收取：不用学生动手，按 glob 匹配自动打包上传。
				go a.collectDir(s, args)
			} else {
				a.emitPrompt("collect", args.Session)
			}
		}
	case OpDemoRequest:
		var args DemoRequestArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil && args.Session != "" {
			a.uiMu.Lock()
			a.demoPrompt = &args
			a.uiMu.Unlock()
			a.emitPrompt("demo", args.Session)
		}
	case OpDemoStart:
		var args DemoStartArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.uiMu.Lock()
			if a.demoPrompt != nil && a.demoPrompt.Session == args.Session {
				a.demoSession = args.Session
				a.demoPrompt = nil
			}
			active := a.demoSession == args.Session
			a.uiMu.Unlock()
			if active {
				a.startDemoStream(s, args.Session)
			}
		}
	case OpDemoViewer:
		a.stopCast()
		castview.Show("window")
	case OpDemoStop:
		a.uiMu.Lock()
		a.demoPrompt, a.demoSession = nil, ""
		a.uiMu.Unlock()
		a.stopDemoStream()
		castview.Close()
	default:
		handled = false
	}
	if !handled && a.OnCommand != nil && a.OnCommand(c) {
		handled = true
	}
	if !handled {
		ack.OK = false
		ack.Err = "未知指令 " + c.Op
	}
	// 先推状态再回 ack：服务端按序处理消息，ack 到达时注册表已更新，
	// SendCommand 返回后 GetDevice 立刻能看到新状态（不靠竞态运气）。
	a.pushStatus(s)
	if err := s.send(Msg{Type: MsgAck, Ack: &ack}); err != nil {
		return
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func localIP(c net.Conn) string {
	if a, ok := c.LocalAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// DefaultLabel 从计算机名取后段作机器编号（去掉 PC 前缀，如 LAB301-PC07 → A-07）。
func DefaultLabel(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 && i < len(name)-1 {
		if suffix := strings.TrimPrefix(name[i+1:], "PC"); suffix != "" {
			return "A-" + suffix
		}
	}
	return name
}

// ---- Classroom client actions ----------------------------------------------

type StudentSignPrompt struct {
	Session  string `json:"session"`
	Deadline int64  `json:"deadline"`
}

type StudentPollPrompt struct {
	Session  string   `json:"session"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Deadline int64    `json:"deadline"`
}

type StudentDemoPrompt struct {
	Session      string   `json:"session"`
	TargetIDs    []string `json:"targetIds"`
	TargetLabels []string `json:"targetLabels"`
}

// StudentCollectPrompt 收取文件请求（学生提交一个文件给管理端）。
// Dir/Glob 非空表示「指定目录收取」——agent 已自动匹配上传，学生无需操作。
type StudentCollectPrompt struct {
	Session string `json:"session"`
	Dir     string `json:"dir,omitempty"`
	Glob    string `json:"glob,omitempty"`
}

type StudentUIState struct {
	Connected   bool                  `json:"connected"`
	Name        string                `json:"name"`
	Label       string                `json:"label"`
	SignIn      *StudentSignPrompt    `json:"signIn,omitempty"`
	Poll        *StudentPollPrompt    `json:"poll,omitempty"`
	Collect     *StudentCollectPrompt `json:"collect,omitempty"`
	HelpRaised  bool                  `json:"helpRaised"`
	Demo        *StudentDemoPrompt    `json:"demo,omitempty"`
	DemoRunning bool                  `json:"demoRunning"`
}

func (a *Agent) StudentState() StudentUIState {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	state := StudentUIState{Connected: a.cur.Load() != nil, Name: a.Name, Label: a.Label, HelpRaised: a.helpRaised, DemoRunning: a.demoSession != ""}
	if a.signPrompt != nil {
		copy := *a.signPrompt
		state.SignIn = &StudentSignPrompt{Session: copy.Session, Deadline: copy.Deadline}
	}
	if a.pollPrompt != nil {
		copy := *a.pollPrompt
		state.Poll = &StudentPollPrompt{Session: copy.Session, Question: copy.Question, Options: append([]string(nil), copy.Options...), Deadline: copy.Deadline}
	}
	if a.demoPrompt != nil {
		copy := *a.demoPrompt
		state.Demo = &StudentDemoPrompt{
			Session: copy.Session, TargetIDs: append([]string(nil), copy.TargetIDs...),
			TargetLabels: append([]string(nil), copy.TargetLabels...),
		}
	}
	if a.collectPrompt != nil {
		state.Collect = &StudentCollectPrompt{
			Session: a.collectPrompt.Session, Dir: a.collectPrompt.Dir, Glob: a.collectPrompt.Glob,
		}
	}
	return state
}

func (a *Agent) AcknowledgeSignIn(sessionID string) error {
	a.uiMu.Lock()
	prompt := a.signPrompt
	s := a.cur.Load()
	if prompt == nil || prompt.Session != sessionID || (prompt.Deadline > 0 && time.Now().Unix() >= prompt.Deadline) {
		a.uiMu.Unlock()
		return fmt.Errorf("签到已结束")
	}
	a.uiMu.Unlock()
	if s == nil {
		return fmt.Errorf("尚未连接管理端")
	}
	if err := s.send(Msg{Type: MsgSignReply, Sign: &SignReplyMsg{Session: sessionID, Name: a.Name}}); err != nil {
		return err
	}
	a.uiMu.Lock()
	if a.signPrompt != nil && a.signPrompt.Session == sessionID {
		a.signPrompt = nil
	}
	a.uiMu.Unlock()
	return nil
}

func (a *Agent) Vote(sessionID, option string) error {
	a.uiMu.Lock()
	prompt := a.pollPrompt
	s := a.cur.Load()
	valid := false
	if prompt != nil && prompt.Session == sessionID && (prompt.Deadline == 0 || time.Now().Unix() < prompt.Deadline) {
		for _, candidate := range prompt.Options {
			if candidate == option {
				valid = true
				break
			}
		}
	}
	a.uiMu.Unlock()
	if !valid {
		return fmt.Errorf("投票已结束或选项无效")
	}
	if s == nil {
		return fmt.Errorf("尚未连接管理端")
	}
	return s.send(Msg{Type: MsgPollReply, Poll: &PollReplyMsg{Session: sessionID, Option: option}})
}

func (a *Agent) RaiseHand() error {
	s := a.cur.Load()
	if s == nil {
		return fmt.Errorf("尚未连接管理端")
	}
	a.uiMu.Lock()
	if a.helpRaised {
		a.uiMu.Unlock()
		return nil
	}
	a.helpRaised = true
	a.uiMu.Unlock()
	if err := s.send(Msg{Type: MsgHelp, Help: &HelpMsg{Kind: "raise"}}); err != nil {
		a.uiMu.Lock()
		a.helpRaised = false
		a.uiMu.Unlock()
		return err
	}
	return nil
}

func (a *Agent) DemoConsent(sessionID string, accept bool) error {
	a.uiMu.Lock()
	prompt := a.demoPrompt
	s := a.cur.Load()
	if prompt == nil || prompt.Session != sessionID {
		a.uiMu.Unlock()
		return fmt.Errorf("演示请求已结束")
	}
	a.uiMu.Unlock()
	if s == nil {
		return fmt.Errorf("尚未连接管理端")
	}
	phase := "decline"
	if accept {
		phase = "accept"
	} else {
		a.uiMu.Lock()
		if a.demoPrompt != nil && a.demoPrompt.Session == sessionID {
			a.demoPrompt = nil
		}
		a.uiMu.Unlock()
	}
	return s.send(Msg{Type: MsgDemo, Demo: &DemoMsg{Session: sessionID, Phase: phase}})
}

func (a *Agent) StopStudentDemo() error {
	a.uiMu.Lock()
	sessionID := a.demoSession
	s := a.cur.Load()
	a.uiMu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("当前没有进行中的演示")
	}
	if s == nil {
		return fmt.Errorf("尚未连接管理端")
	}
	if err := s.send(Msg{Type: MsgDemo, Demo: &DemoMsg{Session: sessionID, Phase: "stop"}}); err != nil {
		return err
	}
	a.uiMu.Lock()
	a.demoSession = ""
	a.uiMu.Unlock()
	a.stopDemoStream()
	return nil
}

// persistState 把教师下发的防护策略回写 client.json（重启后继续生效）。
func (a *Agent) persistState() {
	if f := a.PersistState; f != nil {
		f(a.helperOn.Load(), a.Features())
	}
}

// setWatch 开/关屏幕监看。hi=true 为单机高清查看：分辨率 1600 宽、
// 质量 70、~1.4fps，帧带 Hi 标记让服务端单独路由给大图查看器。
func (a *Agent) setWatch(on, hi bool) {
	a.watchMu.Lock()
	if a.watchOff != nil {
		a.watchOff()
		a.watchOff = nil
	}
	a.watchHi = hi
	if !on {
		a.watchMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.watchOff = cancel
	a.watchMu.Unlock()
	maxW, quality, interval := 320, 45, 500*time.Millisecond
	if hi {
		maxW, quality, interval = 1600, 70, 700*time.Millisecond
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s := a.cur.Load()
			if s == nil {
				continue
			}
			jpeg, err := capture.Frame(maxW, quality)
			if err != nil {
				continue
			}
			_ = s.send(Msg{Type: MsgScreen, Screen: &ScreenMsg{
				JPEG: base64.StdEncoding.EncodeToString(jpeg), Hi: hi,
			}})
		}
	}()
}

func (a *Agent) stopWatch() {
	a.watchMu.Lock()
	if a.watchOff != nil {
		a.watchOff()
		a.watchOff = nil
	}
	a.watchMu.Unlock()
}

func (a *Agent) startDemoStream(s *session, sessionID string) {
	a.stopDemoStream()
	ctx, cancel := context.WithCancel(context.Background())
	a.watchMu.Lock()
	a.demoOff = cancel
	a.watchMu.Unlock()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			a.uiMu.Lock()
			active := a.demoSession == sessionID
			a.uiMu.Unlock()
			if !active {
				return
			}
			jpeg, err := capture.Frame(800, 45)
			if err != nil {
				continue
			}
			_ = s.send(Msg{Type: MsgDemo, Demo: &DemoMsg{Session: sessionID, Phase: "frame", JPEG: base64.StdEncoding.EncodeToString(jpeg)}})
		}
	}()
}

func (a *Agent) stopDemoStream() {
	a.watchMu.Lock()
	if a.demoOff != nil {
		a.demoOff()
		a.demoOff = nil
	}
	a.watchMu.Unlock()
}

// emitConn / emitPrompt 触发可选钩子（不持锁，避免回环）。
func (a *Agent) emitConn(on bool) {
	if f := a.OnConn; f != nil {
		f(on)
	}
}

func (a *Agent) emitPrompt(kind, session string) {
	if f := a.OnPrompt; f != nil {
		f(kind, session)
	}
}

// startCast 开始接收一次广播：有组播参数就加入组播组收 UDP 帧，
// 返回错误时调用方应回 "tcp" 让管理端走 TCP 兜底。
// 加入成功但始终收不到完整帧（网络吞组播）时，4s 后上报 cast_tcp
// 告警让管理端把本机改走 TCP 兜底。
func (a *Agent) startCast(s *session, args CastStartArgs) error {
	a.stopCast()
	castview.Show(args.Mode)
	if args.Group == "" {
		return nil // 纯 TCP 广播，帧走 MsgCast
	}
	key, err := base64.StdEncoding.DecodeString(args.Key)
	if err != nil {
		return err
	}
	rcv, err := JoinCast(CastSessionParams{
		Group: args.Group, Port: args.Port, Session: args.Session, Key: key,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.watchMu.Lock()
	a.castOff = cancel
	a.watchMu.Unlock()
	var lastFrame atomic.Int64 // 最近一次完整解码帧的时间戳
	lastFrame.Store(time.Now().UnixNano())
	go func() {
		rcv.Run(ctx, func(jpeg []byte) {
			lastFrame.Store(time.Now().UnixNano())
			castview.Frame(jpeg)
		})
		rcv.Close()
	}()
	// 滚动看门狗：静止画面也有 ≤5s 的组播心跳帧；连续 ~10s 收不到
	// 完整帧说明组播断流（丢片整帧丢、IGMP 老化等）→ 上报一次
	// cast_tcp 让管理端把本机改走 TCP，并停掉组播接收避免双路收帧。
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if time.Since(time.Unix(0, lastFrame.Load())) <= 10*time.Second {
				continue
			}
			_ = s.send(Msg{Type: MsgAlert, Alert: &AlertMsg{
				Kind: "cast_tcp", Detail: "已加入组播但收不到广播帧",
				Gen: a.castGen.Load(),
			}})
			a.stopCast()
			return
		}
	}()
	return nil
}

// StopCast 停止组播接收（学生端「退出广播」按钮经 castview.OnExit 调它）。
func (a *Agent) StopCast() { a.stopCast() }

// stopCast 退出组播组（不断言 viewer 状态，调用方管 castview）。
func (a *Agent) stopCast() {
	a.watchMu.Lock()
	if a.castOff != nil {
		a.castOff()
		a.castOff = nil
	}
	a.watchMu.Unlock()
}

func (a *Agent) onFileDown(s *session, msg FileMsg) {
	if msg.Phase == "ready" || msg.Phase == "complete" || msg.Phase == "error" || msg.Phase == "progress" {
		a.outMu.Lock()
		ack := a.outgoing[msg.ID]
		if ack != nil && msg.Phase != "progress" {
			select {
			case ack <- msg:
			default:
			}
		}
		a.outMu.Unlock()
		return
	}
	key := msg.ID
	fail := func(reason string) {
		a.inMu.Lock()
		in := a.inFiles[key]
		delete(a.inFiles, key)
		a.inMu.Unlock()
		if in != nil {
			in.abort()
		}
		_ = s.send(Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "error", Err: reason}})
	}
	switch msg.Phase {
	case "offer":
		in, err := beginIncoming(a.RecvDir, "teacher", msg.ID, msg.Name, msg.Size)
		if err != nil {
			fail(err.Error())
			return
		}
		a.inMu.Lock()
		if old := a.inFiles[key]; old != nil {
			old.abort()
		}
		a.inFiles[key] = in
		a.inMu.Unlock()
		if err := s.sendWithTimeout(Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "ready"}}, 10*time.Second); err != nil {
			fail("无法通知发送端准备接收")
			_ = s.conn.Close()
		}
	case "chunk":
		data, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			fail("数据块编码无效")
			return
		}
		a.inMu.Lock()
		in := a.inFiles[key]
		a.inMu.Unlock()
		if in == nil {
			return
		}
		if err := in.writeChunk(msg.Seq, data); err != nil {
			fail(err.Error())
			return
		}
		seq, _ := in.progress()
		if seq%16 == 0 {
			_ = s.send(Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "progress", Seq: seq}})
		}
	case "done":
		a.inMu.Lock()
		in := a.inFiles[key]
		delete(a.inFiles, key)
		a.inMu.Unlock()
		if in == nil {
			fail("接收会话不存在")
			return
		}
		name, err := in.finish(msg.Count, msg.SHA256)
		if err != nil {
			_ = s.send(Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "error", Err: err.Error()}})
			return
		}
		_ = s.send(Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "complete", Name: name, Count: msg.Count, SHA256: msg.SHA256}})
	}
}

func (a *Agent) abortIncoming() {
	a.inMu.Lock()
	files := a.inFiles
	a.inFiles = make(map[string]*incoming)
	a.inMu.Unlock()
	for _, in := range files {
		in.abort()
	}
}

// SubmitFile uploads one student-selected file and waits for the verified completion receipt.
func (a *Agent) SubmitFile(path string, displayName ...string) error {
	id := "u-" + RandHex(12)
	ack := make(chan FileMsg, 1)
	a.outMu.Lock()
	if len(a.outgoing) >= 2 {
		a.outMu.Unlock()
		return fmt.Errorf("最多同时提交 2 个文件")
	}
	a.outgoing[id] = ack
	a.outMu.Unlock()
	defer func() {
		a.outMu.Lock()
		delete(a.outgoing, id)
		a.outMu.Unlock()
	}()

	s := a.cur.Load()
	if s == nil {
		return fmt.Errorf("未连接管理端")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxTransferSize {
		return fmt.Errorf("文件类型或大小不支持")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	name := filepath.Base(path)
	if len(displayName) > 0 && strings.TrimSpace(displayName[0]) != "" {
		name = safeFilename(displayName[0])
	}
	send := func(msg FileMsg) error {
		msg.ID = id
		return s.sendWithTimeout(Msg{Type: MsgFile, File: &msg}, 20*time.Second)
	}
	if err := send(FileMsg{Phase: "offer", Name: name, Size: info.Size()}); err != nil {
		return err
	}
	select {
	case result := <-ack:
		if result.Phase != "ready" {
			if result.Err == "" {
				result.Err = "管理端拒绝接收文件"
			}
			return fmt.Errorf("%s", result.Err)
		}
	case <-time.After(45 * time.Second):
		return fmt.Errorf("等待管理端准备接收超时")
	}
	buf := make([]byte, FileChunk)
	seq := 0
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if err := send(FileMsg{Phase: "chunk", Seq: seq, Data: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				return err
			}
			seq++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := send(FileMsg{Phase: "done", Count: seq, SHA256: digest}); err != nil {
		return err
	}
	select {
	case result := <-ack:
		if result.Phase != "complete" || result.SHA256 != digest {
			if result.Err == "" {
				result.Err = "管理端未确认文件校验"
			}
			return fmt.Errorf("%s", result.Err)
		}
		a.uiMu.Lock()
		a.collectPrompt = nil // 提交完成，收取请求视为已响应
		a.uiMu.Unlock()
		return nil
	case <-time.After(transferWait(info.Size())):
		return fmt.Errorf("等待管理端完成回执超时")
	}
}

// powerDelay 普通关机/重启的 agent 侧倒计时（测试可缩短）。
var powerDelay = 60 * time.Second

// doPowerAction 是 powerAction 的调用接缝，测试可替换以免真的执行关机。
var doPowerAction = powerAction

// schedulePower 普通模式只上定时器（到期执行不带 /f 的关机）；
// force 模式直接 5s 强关。重复下发或强制模式会先作废旧排程
// （gen+1 使旧回调即使已进入也不会再执行）。
func (a *Agent) schedulePower(op string, force bool) error {
	a.powerMu.Lock()
	a.powerGen++
	gen := a.powerGen
	if a.powerTimer != nil {
		a.powerTimer.Stop()
		a.powerTimer = nil
	}
	a.powerMu.Unlock()

	if force {
		return doPowerAction(op, true)
	}
	a.powerMu.Lock()
	defer a.powerMu.Unlock()
	if a.powerGen != gen {
		return nil // 排程窗口内又被取消了
	}
	a.powerTimer = time.AfterFunc(powerDelay, func() {
		a.powerMu.Lock()
		if a.powerGen != gen || a.powerTimer == nil {
			// 已被更新排程取代或被取消：本回调作废，且绝不能把
			// 别人的 powerTimer 置 nil。
			a.powerMu.Unlock()
			return
		}
		a.powerTimer = nil
		a.powerGen++ // 进入执行阶段：此后取消返回 false
		a.powerMu.Unlock()
		if err := doPowerAction(op, false); err != nil {
			log.Printf("agent[%s]: 执行远程关机/重启失败: %v", a.ID, err)
		}
	})
	log.Printf("agent[%s]: %s 将在 %s 后执行（可用 power.cancel 取消）", a.ID, op, powerDelay)
	return nil
}

// cancelScheduledPower 停掉 agent 侧的普通关机倒计时。
// 返回 false 表示没有进行中的倒计时——可能从未排程，也可能已进入
// 执行阶段（回调一旦开始执行 doPowerAction 就无法撤回了）。
func (a *Agent) cancelScheduledPower() bool {
	a.powerMu.Lock()
	defer a.powerMu.Unlock()
	if a.powerTimer == nil {
		return false
	}
	a.powerTimer.Stop()
	a.powerTimer = nil
	a.powerGen++
	log.Printf("agent[%s]: 关机/重启倒计时已取消", a.ID)
	return true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// validOpenURL 只允许 http/https 教学网址（防 file:///javascript: 类注入）。
func validOpenURL(u string) error {
	if len(u) > 2048 {
		return fmt.Errorf("网址过长")
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return fmt.Errorf("只允许 http/https 网址")
	}
	return nil
}

// validLaunchPath 只允许 .exe 绝对路径（exec 直接调起，参数不可注入），
// 并挡掉 cmd/powershell/mshta 等高危壳程序。
func validLaunchPath(p string) error {
	if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".exe") {
		return fmt.Errorf("只允许 .exe 绝对路径")
	}
	switch strings.ToLower(filepath.Base(p)) {
	case "cmd.exe", "powershell.exe", "pwsh.exe", "wscript.exe", "cscript.exe",
		"mshta.exe", "reg.exe", "rundll32.exe", "regsvr32.exe", "bitsadmin.exe",
		"certutil.exe", "msiexec.exe", "wmic.exe", "shutdown.exe":
		return fmt.Errorf("该程序不允许远程启动")
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("程序不存在: %w", err)
	}
	return nil
}

// AllowedCollectRoots 指定目录收取允许的根目录白名单（管理端与 agent
// 双侧强制——管理端拦截只是 UX，agent 才是安全边界）。只放学校预设的
// 作业目录：不开放整个 C:\LabHelper，因为 client.json 的身份 token
// 也在安装目录里。变量形态便于测试注入临时根目录。
var AllowedCollectRoots = []string{`C:\Users\Public`, `C:\LabHelper\Homework`}

// collectPathInRoots 原始路径必须在白名单内——不解析符号链接的版本。
func collectPathInRoots(p string) bool {
	low := strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
	for _, root := range AllowedCollectRoots {
		r := strings.ToLower(filepath.ToSlash(filepath.Clean(root)))
		if low == r || strings.HasPrefix(low, r+"/") {
			return true
		}
	}
	return false
}

// collectDirAllowed 目录校验：原始路径和解析后的真实路径【都】必须
// 在白名单内，防止「白名单目录里的 junction 指出去」逃逸；目录本身
// 是链接到白名单内的快捷方式时，原始路径也必须在白名单内。
// EvalSymlinks 失败视为不可证明安全——拒绝。
func collectDirAllowed(clean string) bool {
	if !collectPathInRoots(clean) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return false
	}
	return collectPathInRoots(resolved)
}

// collectFileAllowed 文件级校验：目录合法不代表里面的文件合法——
// 文件本身可以是符号链接指向白名单外（os.Stat 会跟随，直接读目标）。
func collectFileAllowed(p string) bool {
	if !collectPathInRoots(p) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	return collectPathInRoots(resolved)
}

// collectDir 指定目录收取：按 Glob 匹配目录下文件逐个自动上传。
// 限额：尝试 ≤64 个文件、总量 ≤256MB、单文件 ≤128MB；结果经 "collect"
// 告警带 Stat 结果码回执（ok/empty/denied/quota/fail）。
func (a *Agent) collectDir(s *session, args CollectOpenArgs) {
	report := func(stat, detail string) {
		a.reportAlert(AlertMsg{Kind: "collect", Detail: detail, Stat: stat})
	}
	clean := filepath.Clean(args.Dir)
	if !filepath.IsAbs(clean) || strings.Contains(args.Dir, "..") || strings.HasPrefix(clean, `\\`) {
		report("denied", "目录收取失败：路径无效")
		return
	}
	if !collectDirAllowed(clean) {
		report("denied", fmt.Sprintf("目录收取失败：%s 不在允许范围", clean))
		return
	}
	pattern := args.Glob
	if pattern == "" {
		pattern = "*"
	}
	if strings.Contains(pattern, "..") || strings.ContainsAny(pattern, `/\\`) {
		report("denied", "目录收取失败：匹配规则无效")
		return
	}
	matches, err := filepath.Glob(filepath.Join(clean, pattern))
	if err != nil || len(matches) == 0 {
		report("empty", fmt.Sprintf("目录 %s 无匹配文件", clean))
		return
	}
	const maxFiles, maxTotal = 64, 256 << 20
	var total int64
	attempted, sent, failed, skipped := 0, 0, 0, 0
	for _, p := range matches {
		if attempted >= maxFiles || total >= maxTotal {
			break
		}
		if !collectFileAllowed(p) {
			skipped++ // 符号链接逃出白名单等：不计尝试，静默跳过
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxStudentUploadSize {
			skipped++
			continue
		}
		if total+info.Size() > maxTotal {
			break
		}
		attempted++
		if err := a.SubmitFile(p); err != nil {
			failed++
			log.Printf("agent[%s]: 收取上传 %s 失败: %v", a.ID, p, err)
			continue
		}
		sent++
		total += info.Size()
	}
	limited := attempted >= maxFiles || total >= maxTotal || attempted+skipped < len(matches)
	suffix := func() string {
		if limited {
			return fmt.Sprintf("，已达上限（匹配 %d 个）", len(matches))
		}
		return ""
	}
	switch {
	case sent == 0 && failed > 0:
		report("fail", fmt.Sprintf("目录收取失败：%d 个文件均上传失败%s", failed, suffix()))
	case sent == 0:
		report("empty", fmt.Sprintf("目录 %s 没有可收取的文件", clean))
	case failed > 0:
		// partial：部分文件上传失败——教师一眼看出不是全部完成。
		report("partial", fmt.Sprintf("目录收取部分完成：已提交 %d 个文件（%.1f MB，%d 个失败%s）", sent, float64(total)/(1<<20), failed, suffix()))
	default:
		stat := "ok"
		if limited {
			stat = "quota"
		}
		report(stat, fmt.Sprintf("目录收取完成：已提交 %d 个文件（%.1f MB%s）", sent, float64(total)/(1<<20), suffix()))
	}
}

func transferWait(size int64) time.Duration {
	// Allow for slower classroom LANs while bounding a stalled transfer.
	d := 2*time.Minute + time.Duration(size/(512<<10))*time.Second
	if d > time.Hour {
		return time.Hour
	}
	return d
}
