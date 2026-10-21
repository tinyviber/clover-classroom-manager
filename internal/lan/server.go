package lan

// 管理端服务端：
//   - UDP :47822 监听发现广播，回复带公钥的信标
//   - TCP :47823 接受客户端连接，完成签名握手后进入消息循环
//   - Registry 维护设备表（含离线残留），变更时回调 OnChange
//   - SendCommand 下发指令并等待 Ack

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Device 是注册表里的设备条目（含离线）。
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	IP        string    `json:"ip"`
	MAC       string    `json:"mac"`
	Version   string    `json:"version"`
	Online    bool      `json:"online"`
	HelperOn  bool      `json:"helperOn"`
	Features  []string  `json:"features"`
	Alerts    int       `json:"alerts"`
	TokenHash string    `json:"tokenHash,omitempty"`
	LastSeen  time.Time `json:"lastSeen"`
}

// LastSeenText 给 UI 的友好时间。
func (d Device) LastSeenText() string {
	if d.Online {
		return "刚刚"
	}
	dt := time.Since(d.LastSeen)
	switch {
	case dt < time.Minute:
		return fmt.Sprintf("%d 秒前", int(dt.Seconds()))
	case dt < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(dt.Minutes()))
	case dt < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(dt.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(dt.Hours()/24))
	}
}

type clientConn struct {
	sess    *session
	devID   string
	pending sync.Map // cmdID -> chan AckMsg
}

// Server 是管理端控制服务器。
type Server struct {
	ID      Identity
	TCPPort int

	mu        sync.Mutex
	devices   map[string]*Device     // id -> device
	sessions  map[string]*clientConn // id -> 在线会话
	OnChange  func()                 // 注册表/签到/文件变化回调（UI 用）
	OnScreens func()                 // 缩略图帧到达回调（高频，UI 自节流）
	cmdSeq    atomic.Uint64

	persistPath string
	persistT    *time.Timer

	screens        map[string]ScreenFrame // 各设备最新缩略图 JPEG
	sign           *signState             // 当前签到会话
	poll           *pollState             // 当前单题投票
	demo           *demoState             // 唯一学生演示活动
	helpRequests   map[string]HelpRequest
	transfers      map[string]*FileProgress // 文件传输记录
	inFiles        map[string]*incoming     // 接收中的学生提交
	inReservations map[string]incomingReservation
	fileAcks       map[string]chan FileMsg
	recvDir        string
	frameWorkers   map[string]*frameWorker
	screenWatch    map[string]bool // requested thumbnail subscriptions; includes offline targets
	cast           *serverCast     // 当前教师广播会话（nil=未广播）
}

// serverCast 是一次屏幕广播：组播主通道 + TCP 兜底目标集合。
type serverCast struct {
	args    CastStartArgs   // 下发给客户端的会话参数；Group=="" 表示纯 TCP
	sender  *CastSender     // 组播发送端（nil 表示管理端侧也组播不出去）
	targets map[string]bool // 全部目标（含离线，重连恢复用）
	tcpOnly map[string]bool // 组播加入失败、改走 TCP 的目标
}

func NewServer(id Identity, tcpPort int) *Server {
	return &Server{
		ID:             id,
		TCPPort:        tcpPort,
		devices:        map[string]*Device{},
		sessions:       map[string]*clientConn{},
		screens:        map[string]ScreenFrame{},
		transfers:      map[string]*FileProgress{},
		inFiles:        map[string]*incoming{},
		inReservations: map[string]incomingReservation{},
		fileAcks:       map[string]chan FileMsg{},
		helpRequests:   map[string]HelpRequest{},
		frameWorkers:   map[string]*frameWorker{},
		screenWatch:    map[string]bool{},
	}
}

// SetPersistPath 设置注册表持久化文件并立即加载（已有设备标记离线）。
func (s *Server) SetPersistPath(path string) {
	s.mu.Lock()
	s.persistPath = path
	s.mu.Unlock()
	s.loadRegistry()
}

func (s *Server) loadRegistry() {
	b, err := os.ReadFile(s.persistPath)
	if err != nil {
		return
	}
	var devs []Device
	if json.Unmarshal(b, &devs) != nil {
		return
	}
	s.mu.Lock()
	for _, d := range devs {
		if _, ok := s.devices[d.ID]; !ok {
			dd := d
			dd.Online = false // 重启后一切按离线算，等 agent 重连
			s.devices[d.ID] = &dd
		}
	}
	s.mu.Unlock()
	s.changed()
}

// schedulePersist 变更后 1s 防抖落盘（tmp+rename 原子写）。
func (s *Server) schedulePersist() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistPath == "" {
		return
	}
	if s.persistT != nil {
		s.persistT.Stop()
	}
	s.persistT = time.AfterFunc(time.Second, s.saveRegistry)
}

func (s *Server) saveRegistry() {
	s.mu.Lock()
	path := s.persistPath
	devs := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		devs = append(devs, *d)
	}
	s.mu.Unlock()
	if path == "" {
		return
	}
	b, err := json.Marshal(devs)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	os.MkdirAll(filepath.Dir(path), 0o755)
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, path)
	}
}

// Seed 预置已知设备（首次上线前显示为离线）。
func (s *Server) Seed(d Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[d.ID]; !ok {
		d.Online = false
		d.LastSeen = time.Now()
		s.devices[d.ID] = &d
	}
}

// Devices 返回注册表快照。
func (s *Server) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, *d)
	}
	return out
}

// GetDevice 取单台设备。
func (s *Server) GetDevice(id string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return Device{}, false
	}
	return *d, true
}

// Run 启动 TCP + UDP 与离线巡检，直到 ctx 结束。
// 端口被占用时后台持续重试（开发热重启时新旧实例短暂重叠；
// 或另一台机器上的管理端退出后本机接管）。
func (s *Server) Run(ctx context.Context) error {
	go s.tcpLoop(ctx)
	go s.discoverLoop(ctx)
	go s.sweeper(ctx)
	return nil
}

// tcpLoop 反复尝试监听 TCP，成功后 accept 到 ctx 结束或监听失败再重试。
func (s *Server) tcpLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.TCPPort))
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		log.Printf("lan: TCP 监听 :%d", s.TCPPort)
		s.acceptLoop(ctx, ln)
		// acceptLoop 返回说明监听失败或 ctx 结束
	}
}

// acceptLoop 接受连接直到监听关闭或 ctx 结束。
func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-done:
		}
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			ln.Close()
			return
		}
		go s.serve(conn)
	}
}

// ---- UDP 发现应答 ----

func (s *Server) discoverLoop(ctx context.Context) {
	beacon := encodeBeacon(s.TCPPort, s.ID.Pub())
	buf := make([]byte, 256)
	for ctx.Err() == nil {
		// 与 tcpLoop 同理：端口被占就等一会儿再抢
		c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: UDPPort})
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		log.Printf("lan: UDP 发现监听 :%d", UDPPort)
		for ctx.Err() == nil {
			c.SetReadDeadline(deadline(2))
			n, src, err := c.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			if string(buf[:min(n, len(DiscoverMagic))]) == DiscoverMagic {
				c.WriteToUDP(beacon, src)
			}
		}
		c.Close()
	}
}

// ---- TCP 会话 ----

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	sess, err := serverHandshake(conn, s.ID)
	if err != nil {
		log.Printf("lan: 握手失败 %v: %v", conn.RemoteAddr(), err)
		return
	}
	// 第一条必须是 hello
	var m Msg
	if err := sess.recv(&m); err != nil || m.Type != MsgHello || m.Hello == nil {
		log.Printf("lan: 首条消息不是 hello: %v %v", conn.RemoteAddr(), m.Type)
		return
	}
	h := m.Hello
	if h.ID == "" || len(h.ID) > 96 || h.Token == "" || len(h.Token) < 32 || len(h.Token) > 512 || len(h.Name) > 256 || len(h.Label) > 128 {
		log.Printf("lan: 拒绝无效 hello %v", conn.RemoteAddr())
		return
	}
	cc := &clientConn{sess: sess, devID: h.ID}

	s.mu.Lock()
	d, ok := s.devices[h.ID]
	if !ok {
		d = &Device{ID: h.ID}
		s.devices[h.ID] = d
	}
	tokenHash := sha256.Sum256([]byte(h.Token))
	if d.TokenHash == "" {
		// TOFU：首次会话绑定随机客户端 token 的哈希；这并不是 enrollment 审批。
		d.TokenHash = hex.EncodeToString(tokenHash[:])
	} else {
		stored, decodeErr := hex.DecodeString(d.TokenHash)
		if decodeErr != nil || len(stored) != len(tokenHash) || subtle.ConstantTimeCompare(stored, tokenHash[:]) != 1 {
			s.mu.Unlock()
			log.Printf("lan: 拒绝设备 %s：客户端 TOFU token 不匹配", h.ID)
			return
		}
	}
	d.Name, d.Label, d.IP, d.MAC, d.Version = h.Name, h.Label, h.IP, h.MAC, h.Version
	d.Online, d.LastSeen = true, time.Now()
	// 若已有同名旧会话（重连/多开），关掉旧的
	if old := s.sessions[h.ID]; old != nil && old != cc {
		old.sess.conn.Close()
	}
	s.sessions[h.ID] = cc
	s.mu.Unlock()
	s.changed()
	s.syncClientActivities(h.ID)

	log.Printf("lan: %s(%s) 上线 %v", h.Name, h.ID, conn.RemoteAddr())

	for {
		var m Msg
		if err := sess.recv(&m); err != nil {
			s.offline(h.ID, cc)
			return
		}
		s.handle(cc, m)
	}
}

func (s *Server) handle(cc *clientConn, m Msg) {
	s.mu.Lock()
	d := s.devices[cc.devID]
	if d != nil {
		d.LastSeen = time.Now()
	}
	s.mu.Unlock()

	switch m.Type {
	case MsgStatus:
		if m.Status == nil {
			return
		}
		s.mu.Lock()
		if d := s.devices[cc.devID]; d != nil {
			d.HelperOn = m.Status.HelperOn
			d.Features = m.Status.Features
		}
		s.mu.Unlock()
		s.changed()
	case MsgAck:
		if m.Ack == nil {
			return
		}
		if ch, ok := cc.pending.Load(m.Ack.ID); ok {
			select {
			case ch.(chan AckMsg) <- *m.Ack:
			default: // 重复 ack 不阻塞读循环
			}
		}
	case MsgAlert:
		if m.Alert == nil {
			return
		}
		if m.Alert.Kind == "cast_tcp" {
			// 组播帧收不到：把这机改走 TCP 兜底，不计入纪律告警。
			// 已是 TCP 兜底的目标重复上报直接忽略（告警可被伪造）。
			s.mu.Lock()
			dup := s.cast != nil && s.cast.tcpOnly[cc.devID]
			s.mu.Unlock()
			if !dup {
				s.MarkCastTCP(cc.devID)
				log.Printf("lan: %s 组播收不到帧，改走 TCP 兜底", cc.devID)
			}
			return
		}
		s.mu.Lock()
		if d := s.devices[cc.devID]; d != nil {
			d.Alerts++
		}
		s.mu.Unlock()
		log.Printf("lan: 告警 %s %s: %s", cc.devID, m.Alert.Kind, m.Alert.Detail)
		s.changed()
	case MsgScreen:
		if m.Screen != nil {
			s.onScreen(cc.devID, m.Screen)
		}
	case MsgSignReply:
		if m.Sign != nil {
			s.onSignReply(cc.devID, m.Sign)
		}
	case MsgFile:
		if m.File != nil {
			s.onFileMsg(cc.devID, m.File)
		}
	case MsgHelp:
		if m.Help != nil {
			s.onHelp(cc.devID, m.Help)
		}
	case MsgPollReply:
		if m.Poll != nil {
			s.onPollReply(cc.devID, m.Poll)
		}
	case MsgDemo:
		if m.Demo != nil {
			s.onDemo(cc.devID, m.Demo)
		}
	}
}

func (s *Server) offline(id string, cc *clientConn) {
	s.mu.Lock()
	// 防止把新会话踢下线
	current := s.sessions[id] == cc
	if current {
		delete(s.sessions, id)
		delete(s.screens, id)
		if d := s.devices[id]; d != nil {
			d.Online = false
			d.LastSeen = time.Now()
		}
		if worker := s.frameWorkers[id]; worker != nil && worker.cc == cc {
			worker.cancel()
			delete(s.frameWorkers, id)
		}
	}
	s.mu.Unlock()
	if current {
		s.cleanupIncomingForDevice(id)
		s.stopDemoForDevice(id)
		s.changed()
		log.Printf("lan: %s 离线", id)
	}
}

// sweeper 周期把超时的在线设备标记离线。
func (s *Server) sweeper(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		changed := false
		var offlineIDs []string
		for id, d := range s.devices {
			if d.Online && time.Since(d.LastSeen) > OfflineAfter {
				d.Online = false
				changed = true
				offlineIDs = append(offlineIDs, id)
				if cc := s.sessions[id]; cc != nil {
					cc.sess.conn.Close()
					delete(s.sessions, id)
				}
				delete(s.screens, id)
				if worker := s.frameWorkers[id]; worker != nil {
					worker.cancel()
					delete(s.frameWorkers, id)
				}
			}
		}
		s.mu.Unlock()
		for _, id := range offlineIDs {
			s.cleanupIncomingForDevice(id)
			s.stopDemoForDevice(id)
		}
		s.expireIncoming()
		s.expireClassroomSessions()
		s.mu.Lock()
		prunedTransfers := s.pruneTransfersLocked(time.Now())
		s.mu.Unlock()
		if prunedTransfers {
			s.changed()
		}
		if changed {
			s.changed()
		}
	}
}

// SendCommand 给在线设备下发指令并等待 Ack；离线/超时返回错误。
func (s *Server) SendCommand(id string, op string, args any) (AckMsg, error) {
	s.mu.Lock()
	cc := s.sessions[id]
	s.mu.Unlock()
	if cc == nil {
		return AckMsg{}, errors.New("设备不在线")
	}
	cid := fmt.Sprintf("c%d", s.cmdSeq.Add(1))
	ch := make(chan AckMsg, 1)
	cc.pending.Store(cid, ch)
	defer cc.pending.Delete(cid)
	if err := cc.sess.send(Msg{Type: MsgCommand, Cmd: &CommandMsg{ID: cid, Op: op, Args: args}}); err != nil {
		return AckMsg{}, err
	}
	select {
	case ack := <-ch:
		return ack, nil
	case <-time.After(CommandTimeout):
		return AckMsg{}, errors.New("等待确认超时")
	}
}

// ClearAlerts 清零设备告警计数。
func (s *Server) ClearAlerts(id string) {
	s.mu.Lock()
	if d := s.devices[id]; d != nil {
		d.Alerts = 0
	}
	s.mu.Unlock()
	s.changed()
}

// InjectAlert 直接给设备记一条告警（演示/测试用；
// 线上告警由 agent 的 MsgAlert 驱动）。
func (s *Server) InjectAlert(id, kind, detail string) {
	s.mu.Lock()
	if d := s.devices[id]; d != nil {
		d.Alerts++
	}
	s.mu.Unlock()
	log.Printf("lan: 告警 %s %s: %s", id, kind, detail)
	s.changed()
}

func (s *Server) changed() {
	s.schedulePersist()
	if s.OnChange != nil {
		s.OnChange()
	}
}

// ---- 客户端发现辅助（供 agent 与测试用）----

// EncodeDiscover 生成发现广播载荷。
func EncodeDiscover() []byte { return []byte(DiscoverMagic) }

// BeaconReply 是发现回复。
type BeaconReply struct {
	TCPPort int
	Pub     []byte
}

// DecodeBeacon 公开解析信标（agent 用）。
func DecodeBeacon(b []byte) (BeaconReply, bool) {
	port, pub, ok := decodeBeacon(b)
	if !ok {
		return BeaconReply{}, false
	}
	return BeaconReply{TCPPort: port, Pub: pub}, true
}
