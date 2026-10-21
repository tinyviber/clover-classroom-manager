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
	DirectAddr string
	// RecvDir 教师下发文件的落盘目录（默认 exe 旁 recv/）。
	RecvDir string

	// OnCommand 可选钩子：自定义指令处理。返回 false 表示已由 agent 内置处理。
	OnCommand func(CommandMsg) bool
	// OnPrompt 可选钩子：有新的课堂互动（"signin"/"poll"/"demo"/"collect"）待学生处理。
	// 静默运行的客户端用它做气泡通知 + 唤起互动窗口。
	OnPrompt func(kind, session string)
	// OnConn 可选钩子：管理端会话建立/断开。
	OnConn func(connected bool)

	helperOn atomic.Bool
	features atomic.Value // []string

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
	a.helperOn.Store(true)
	a.features.Store([]string{})
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

// Run 主循环：发现→连接→会话，断线重来。
func (a *Agent) Run(ctx context.Context) {
	for ctx.Err() == nil {
		addr, pub, err := a.discover(ctx)
		if err != nil {
			log.Printf("agent[%s]: 发现失败: %v", a.ID, err)
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if err := a.session(ctx, addr, pub); err != nil {
			log.Printf("agent[%s]: 会话结束: %v", a.ID, err)
			if ctx.Err() == nil && a.DirectAddr != "" && a.PinnedPub != nil {
				discoverCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				foundAddr, foundPub, discoverErr := a.discoverBroadcast(discoverCtx, false)
				cancel()
				if discoverErr == nil && a.PinnedPub.Equal(foundPub) {
					if foundAddr != a.DirectAddr {
						log.Printf("agent[%s]: 管理端地址已更新 %s → %s", a.ID, a.DirectAddr, foundAddr)
						a.DirectAddr = foundAddr
					}
				} else if discoverErr != nil {
					log.Printf("agent[%s]: UDP 发现回退失败: %v", a.ID, discoverErr)
				}
			}
			sleepCtx(ctx, 2*time.Second)
		}
	}
}

// discover 广播找管理端，返回 (tcpAddr, adminPub)。
func (a *Agent) discover(ctx context.Context) (string, ed25519.PublicKey, error) {
	if a.DirectAddr != "" && a.PinnedPub != nil {
		return a.DirectAddr, a.PinnedPub, nil
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
			if useConfiguredAddr && a.DirectAddr != "" {
				addr = a.DirectAddr
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
	defer func() {
		a.cur.Store(nil)
		a.emitConn(false)
		a.stopWatch()
		a.stopDemoStream()
		a.stopCast()
		a.abortIncoming()
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
		HelperOn: a.helperOn.Load(),
		Features: a.features.Load().([]string),
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
		}
	case OpSetFeatures:
		var args SetFeaturesArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.features.Store(args.Keys)
		}
	case OpScreenWatch:
		var args ScreenWatchArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			a.setWatch(args.On)
		}
	case OpCastStart:
		var args CastStartArgs
		if b, _ := json.Marshal(c.Args); json.Unmarshal(b, &args) == nil {
			if args.Mode == "window" || args.Mode == "fullscreen" {
				if err := a.startCast(s, args); err != nil {
					// 组播不可用 → 回 "tcp"，管理端把这机标记为 TCP 兜底
					ack.Detail = "tcp"
				}
			} else {
				ack.OK, ack.Err = false, "无效的广播模式"
				ack.Detail = "tcp" // 拒绝广播也走兜底，不留黑屏
			}
		} else {
			ack.Detail = "tcp" // 参数解不开：退到 TCP 也不留黑屏
		}
	case OpCastStop:
		a.stopCast()
		castview.Close()
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
			a.emitPrompt("collect", args.Session)
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
	if err := s.send(Msg{Type: MsgAck, Ack: &ack}); err != nil {
		return
	}
	a.pushStatus(s) // 指令落地后立即回报新状态
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
type StudentCollectPrompt struct {
	Session string `json:"session"`
}

type StudentUIState struct {
	Connected   bool                   `json:"connected"`
	Name        string                 `json:"name"`
	Label       string                 `json:"label"`
	SignIn      *StudentSignPrompt     `json:"signIn,omitempty"`
	Poll        *StudentPollPrompt     `json:"poll,omitempty"`
	Collect     *StudentCollectPrompt  `json:"collect,omitempty"`
	HelpRaised  bool                   `json:"helpRaised"`
	Demo        *StudentDemoPrompt     `json:"demo,omitempty"`
	DemoRunning bool                   `json:"demoRunning"`
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
		state.Collect = &StudentCollectPrompt{Session: a.collectPrompt.Session}
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

func (a *Agent) setWatch(on bool) {
	a.watchMu.Lock()
	if a.watchOff != nil {
		a.watchOff()
		a.watchOff = nil
	}
	if !on {
		a.watchMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.watchOff = cancel
	a.watchMu.Unlock()
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
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
			jpeg, err := capture.Frame(320, 45)
			if err != nil {
				continue
			}
			_ = s.send(Msg{Type: MsgScreen, Screen: &ScreenMsg{JPEG: base64.StdEncoding.EncodeToString(jpeg)}})
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

func transferWait(size int64) time.Duration {
	// Allow for slower classroom LANs while bounding a stalled transfer.
	d := 2*time.Minute + time.Duration(size/(512<<10))*time.Second
	if d > time.Hour {
		return time.Hour
	}
	return d
}
