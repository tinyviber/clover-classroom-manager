package lan

// 客户端 agent：跑在学生机上。
// 发现管理端（UDP 广播 HELLO → 信标）→ TCP 连接 → 签名握手 → hello →
// 心跳上行 + 指令下行。断线自动回到发现阶段重连。
// DirectAddr 非空时跳过发现（演示/调试/手动指定管理端）。

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Agent 是客户端代理。
type Agent struct {
	ID, Name, Label, MAC, IP, Version, Token string

	// PinnedPub 管理端公钥。nil 表示 TOFU：信任第一个应答的信标。
	PinnedPub ed25519.PublicKey
	// DirectAddr 形如 "192.168.31.1:47823"，非空则跳过 UDP 发现。
	DirectAddr string

	// OnCommand 可选钩子：自定义指令处理。返回 false 表示已由 agent 内置处理。
	OnCommand func(CommandMsg) bool

	helperOn atomic.Bool
	features atomic.Value // []string
}

// NewAgent 构造一个默认全开防护的 agent。
func NewAgent(id, name string) *Agent {
	a := &Agent{ID: id, Name: name, Version: "13.1"}
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
			sleepCtx(ctx, 2*time.Second)
		}
	}
}

// discover 广播找管理端，返回 (tcpAddr, adminPub)。
func (a *Agent) discover(ctx context.Context) (string, ed25519.PublicKey, error) {
	if a.DirectAddr != "" && a.PinnedPub != nil {
		return a.DirectAddr, a.PinnedPub, nil
	}
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
		// DirectAddr 但没公钥：仍需等一个信标拿公钥
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
			if a.DirectAddr != "" {
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

	done := make(chan struct{})
	defer close(done)
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
		if m.Type == MsgCommand && m.Cmd != nil {
			go a.handleCmd(sess, *m.Cmd)
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

// DefaultLabel 从计算机名取后段作机器编号（- 后两位，如 LAB301-PC07 → A-07）。
func DefaultLabel(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 && i < len(name)-1 {
		return "A-" + name[i+3:]
	}
	return name
}
