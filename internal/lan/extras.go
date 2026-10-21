package lan

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScreenFrame is the newest low-resolution screen image reported by a client.
type ScreenFrame struct {
	DeviceID string    `json:"deviceId"`
	Label    string    `json:"label"`
	Name     string    `json:"name"`
	JPEG     []byte    `json:"-"`
	At       time.Time `json:"at"`
}

// SignReply is one attendance response.
type SignReply struct {
	DeviceID string    `json:"deviceId"`
	Label    string    `json:"label"`
	Name     string    `json:"name"`
	At       time.Time `json:"at"`
}

// SignSession is the current attendance activity.
type SignSession struct {
	Open      bool        `json:"open"`
	ID        string      `json:"id"`
	Deadline  int64       `json:"deadline"`
	Sent      int         `json:"sent"`
	TargetIDs []string    `json:"targetIds"`
	Replies   []SignReply `json:"replies"`
	Online    int         `json:"online"`
}

// FileProgress describes one device/file transfer.
type FileProgress struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"deviceId"`
	Label    string    `json:"label"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Sent     int64     `json:"sent"`
	Done     bool      `json:"done"`
	In       bool      `json:"in"`
	Err      string    `json:"err,omitempty"`
	At       time.Time `json:"at"`
}

type HelpRequest struct {
	DeviceID string    `json:"deviceId"`
	Label    string    `json:"label"`
	Name     string    `json:"name"`
	At       time.Time `json:"at"`
}

type PollReply struct {
	DeviceID string    `json:"deviceId"`
	Label    string    `json:"label"`
	Option   string    `json:"option"`
	At       time.Time `json:"at"`
}

type PollSession struct {
	Open      bool        `json:"open"`
	ID        string      `json:"id"`
	Question  string      `json:"question"`
	Options   []string    `json:"options"`
	Deadline  int64       `json:"deadline"`
	Sent      int         `json:"sent"`
	TargetIDs []string    `json:"targetIds"`
	Replies   []PollReply `json:"replies"`
}

type DemoSession struct {
	State       string    `json:"state"`
	SessionID   string    `json:"sessionId"`
	SourceID    string    `json:"sourceId"`
	SourceLabel string    `json:"sourceLabel"`
	TargetIDs   []string  `json:"targetIds"`
	RequestedAt time.Time `json:"requestedAt"`
	Err         string    `json:"err,omitempty"`
}

type signState struct {
	id         string
	deadline   int64
	sent       int
	closed     bool
	recipients map[string]bool
	replies    map[string]SignReply
}

type pollState struct {
	id         string
	question   string
	options    []string
	deadline   int64
	sent       int
	closed     bool
	recipients map[string]bool
	replies    map[string]PollReply
}

type demoState struct {
	DemoSession
	lastFrame time.Time
}

const (
	maxIncomingTransfersGlobal    = 16
	maxIncomingTransfersPerDevice = 2
	maxIncomingBytesGlobal        = 512 << 20
	maxIncomingBytesPerDevice     = 256 << 20
	maxReceivedFilesGlobal        = 1000
	maxReceivedFilesPerDevice     = 100
	maxReceivedBytesGlobal        = 10 << 30
	maxReceivedBytesPerDevice     = 1 << 30
	fileProgressRetention         = 7 * 24 * time.Hour
)

type incomingReservation struct {
	deviceID string
	size     int64
	at       time.Time
}

// sendMsg sends a message without waiting for an acknowledgement.
func (s *Server) sendMsg(id string, m Msg) bool {
	s.mu.Lock()
	cc := s.sessions[id]
	s.mu.Unlock()
	if cc == nil {
		return false
	}
	if err := cc.sess.send(m); err != nil {
		_ = cc.sess.conn.Close()
		return false
	}
	return true
}

// Broadcast sends a message to all online clients or the selected subset.
func (s *Server) Broadcast(m Msg, only []string) int {
	s.mu.Lock()
	var targets []*clientConn
	seen := make(map[*clientConn]bool)
	if only == nil {
		for _, cc := range s.sessions {
			if !seen[cc] {
				seen[cc] = true
				targets = append(targets, cc)
			}
		}
	} else {
		for _, id := range only {
			if cc := s.sessions[id]; cc != nil && !seen[cc] {
				seen[cc] = true
				targets = append(targets, cc)
			}
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	results := make(chan bool, len(targets))
	for _, cc := range targets {
		wg.Add(1)
		go func(cc *clientConn) {
			defer wg.Done()
			if err := cc.sess.send(m); err != nil {
				_ = cc.sess.conn.Close()
				results <- false
				return
			}
			results <- true
		}(cc)
	}
	wg.Wait()
	close(results)
	n := 0
	for ok := range results {
		if ok {
			n++
		}
	}
	return n
}

// SetScreenWatch controls low-resolution previews and remembers selected IDs
// across client reconnects. An empty list enables all currently online clients;
// an empty list when disabling clears every remembered subscription.
func (s *Server) SetScreenWatch(ids []string, on bool) {
	requested := normalizeIDs(ids)
	s.mu.Lock()
	if len(requested) == 0 {
		if on {
			for id := range s.sessions {
				requested = append(requested, id)
			}
		} else {
			requested = sortedIDs(s.screenWatch)
		}
	}
	for _, id := range requested {
		if on {
			s.screenWatch[id] = true
		} else {
			delete(s.screenWatch, id)
		}
	}
	targets := make([]string, 0, len(requested))
	for _, id := range requested {
		if s.sessions[id] != nil {
			targets = append(targets, id)
		}
	}
	s.mu.Unlock()
	args := ScreenWatchArgs{On: on}
	cmd := Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("w%d", s.cmdSeq.Add(1)), Op: OpScreenWatch, Args: args,
	}}
	s.Broadcast(cmd, targets)
	s.changed()
}

// ScreenWatchTargets returns remembered thumbnail subscriptions, including
// devices that are currently offline.
func (s *Server) ScreenWatchTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedIDs(s.screenWatch)
}

// StartCast 开一次广播会话：尝试起组播发送端（失败则整局退回纯 TCP），
// 记录目标集合供断线重连恢复。调用方随后对每台目标机
// SendCommand(OpCastStart, CastArgs(id))。
func (s *Server) StartCast(p CastSessionParams, mode string, ids []string) {
	if mode != "window" && mode != "fullscreen" {
		return
	}
	args := CastStartArgs{Mode: mode}
	var sender *CastSender
	if p.Group != "" {
		if cs, err := NewCastSender(p, nil); err == nil {
			sender = cs
			args.Group = p.Group
			args.Port = p.Port
			args.Session = p.Session
			args.Key = base64.StdEncoding.EncodeToString(p.Key)
		}
	}
	s.mu.Lock()
	if s.cast != nil && s.cast.sender != nil {
		s.cast.sender.Close() // 防重复调用泄漏旧发送端
	}
	s.cast = &serverCast{
		args:    args,
		sender:  sender,
		targets: map[string]bool{},
		tcpOnly: map[string]bool{},
	}
	for _, id := range normalizeIDs(ids) {
		s.cast.targets[id] = true
		if sender == nil {
			s.cast.tcpOnly[id] = true // 管理端组播不出去 → 全员 TCP
		}
	}
	s.mu.Unlock()
	s.changed()
}

// StopCast 关闭当前广播会话并返回旧目标集合（调用方负责逐个发 cast.stop）。
func (s *Server) StopCast() []string {
	s.mu.Lock()
	old := s.cast
	s.cast = nil
	s.mu.Unlock()
	if old == nil {
		return nil
	}
	if old.sender != nil {
		old.sender.Close()
	}
	s.mu.Lock()
	for id := range old.tcpOnly {
		if w := s.frameWorkers[id]; w != nil {
			w.cancel()
			delete(s.frameWorkers, id)
		}
	}
	s.mu.Unlock()
	s.changed()
	return sortedIDs(old.targets)
}

// CastArgs 返回当前广播要下发给某台客户端的会话参数；id 不在目标里则 false。
// 已标 TCP 兜底的目标下发剥离组播参数的 args（Group="" → 客户端纯 TCP），
// 避免重连恢复时又加入组播造成双路收帧。
func (s *Server) CastArgs(id string) (CastStartArgs, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cast == nil || !s.cast.targets[id] {
		return CastStartArgs{}, false
	}
	args := s.cast.args
	if s.cast.tcpOnly[id] {
		args.Group, args.Key, args.Session, args.Port = "", "", 0, 0
	}
	return args, true
}

// CastMode 返回当前广播模式（"" = 未广播）。
func (s *Server) CastMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cast == nil {
		return ""
	}
	return s.cast.args.Mode
}

// CastTargets returns targets in the active teacher broadcast, including
// devices that are currently offline; an empty list means no broadcast.
func (s *Server) CastTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cast == nil {
		return []string{}
	}
	return sortedIDs(s.cast.targets)
}

// MarkCastTCP 标记某台目标机改走 TCP 兜底（组播加入失败/丢包时）。
func (s *Server) MarkCastTCP(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cast != nil && s.cast.targets[id] {
		s.cast.tcpOnly[id] = true
	}
}

// CastFrame 分发一帧广播：组播一次性发出 + 只对 TCP 兜底目标逐个排队。
func (s *Server) CastFrame(jpeg []byte) {
	s.mu.Lock()
	cast := s.cast
	s.mu.Unlock()
	if cast == nil {
		return
	}
	if cast.sender != nil {
		// UDP 尽力投递；单帧失败不影响后续
		_, _ = cast.sender.Send(jpeg)
	}
	s.mu.Lock()
	tcpIDs := make([]string, 0, len(cast.tcpOnly))
	for id := range cast.tcpOnly {
		if _, ok := s.sessions[id]; ok {
			tcpIDs = append(tcpIDs, id)
		}
	}
	s.mu.Unlock()
	s.QueueCastFrame(tcpIDs, jpeg)
}

// Screens returns an immutable snapshot of the most recent frame per device.
func (s *Server) Screens() map[string]ScreenFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ScreenFrame, len(s.screens))
	for id, frame := range s.screens {
		frame.JPEG = append([]byte(nil), frame.JPEG...)
		out[id] = frame
	}
	return out
}

func (s *Server) onScreen(devID string, m *ScreenMsg) {
	jpeg, err := base64.StdEncoding.DecodeString(m.JPEG)
	if err != nil || len(jpeg) == 0 || len(jpeg) > 700<<10 {
		return
	}
	s.mu.Lock()
	device := s.devices[devID]
	frame := ScreenFrame{DeviceID: devID, JPEG: jpeg, At: time.Now()}
	if device != nil {
		frame.Label, frame.Name = device.Label, device.Name
	}
	s.screens[devID] = frame
	s.mu.Unlock()
	if s.OnScreens != nil {
		s.OnScreens()
	}
}

// OpenSignIn broadcasts a new attendance session. seconds=0 means manual close.
func (s *Server) OpenSignIn(seconds int, ids []string) (string, int) {
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 24*60*60 {
		seconds = 24 * 60 * 60
	}
	targets := s.OnlineIDs(ids)
	recipients := make(map[string]bool, len(targets))
	for _, id := range targets {
		recipients[id] = true
	}
	ss := &signState{id: "s-" + RandHex(12), recipients: recipients, replies: make(map[string]SignReply)}
	if seconds > 0 {
		ss.deadline = time.Now().Add(time.Duration(seconds) * time.Second).Unix()
	}
	s.mu.Lock()
	var oldTargets []string
	if s.sign != nil && !s.sign.closed {
		s.sign.closed = true
		oldTargets = sortedIDs(s.sign.recipients)
	}
	s.sign = ss
	s.mu.Unlock()
	if oldTargets != nil {
		s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInClose,
		}}, oldTargets)
	}
	n := s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInOpen,
		Args: SignInOpenArgs{Session: ss.id, Deadline: ss.deadline},
	}}, targets)
	s.mu.Lock()
	if s.sign == ss {
		ss.sent = n
	}
	s.mu.Unlock()
	s.changed()
	return ss.id, n
}

func (s *Server) CloseSignIn() {
	s.mu.Lock()
	var targets []string
	if s.sign != nil {
		s.sign.closed = true
		targets = sortedIDs(s.sign.recipients)
	}
	s.mu.Unlock()
	s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInClose,
	}}, targets)
	s.changed()
}

func (s *Server) SignInState() SignSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := SignSession{Online: len(s.sessions)}
	if s.sign == nil {
		return state
	}
	state.ID, state.Deadline = s.sign.id, s.sign.deadline
	state.TargetIDs = sortedIDs(s.sign.recipients)
	state.Sent = s.sign.sent
	state.Open = !s.sign.closed && (s.sign.deadline == 0 || time.Now().Unix() < s.sign.deadline)
	for _, reply := range s.sign.replies {
		state.Replies = append(state.Replies, reply)
	}
	sort.Slice(state.Replies, func(i, j int) bool { return state.Replies[i].At.Before(state.Replies[j].At) })
	return state
}

func (s *Server) onSignReply(devID string, msg *SignReplyMsg) {
	s.mu.Lock()
	if s.sign == nil || s.sign.closed || s.sign.id != msg.Session || !s.sign.recipients[devID] || (s.sign.deadline > 0 && time.Now().Unix() >= s.sign.deadline) {
		s.mu.Unlock()
		return
	}
	if _, exists := s.sign.replies[devID]; !exists {
		label, name := devID, msg.Name
		if d := s.devices[devID]; d != nil {
			label, name = d.Label, d.Name
		}
		s.sign.replies[devID] = SignReply{DeviceID: devID, Label: label, Name: name, At: time.Now()}
	}
	s.mu.Unlock()
	s.changed()
}

// StartPoll validates and broadcasts one multiple-choice classroom question.
func (s *Server) StartPoll(question string, options []string, seconds int, ids []string) (string, int, error) {
	question = strings.TrimSpace(question)
	if question == "" || len([]rune(question)) > 240 || len(options) < 2 || len(options) > 8 {
		return "", 0, fmt.Errorf("题目不能为空，选项数需为 2–8 个")
	}
	clean := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" || len([]rune(option)) > 100 || seen[option] {
			return "", 0, fmt.Errorf("投票选项不能为空、不能重复且最多 100 字")
		}
		seen[option] = true
		clean = append(clean, option)
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 24*60*60 {
		seconds = 24 * 60 * 60
	}
	targets := s.OnlineIDs(ids)
	recipients := make(map[string]bool, len(targets))
	for _, id := range targets {
		recipients[id] = true
	}
	ps := &pollState{id: "p-" + RandHex(12), question: question, options: clean, recipients: recipients, replies: make(map[string]PollReply)}
	if seconds > 0 {
		ps.deadline = time.Now().Add(time.Duration(seconds) * time.Second).Unix()
	}
	s.mu.Lock()
	var oldTargets []string
	if s.poll != nil && !s.poll.closed {
		s.poll.closed = true
		oldTargets = sortedIDs(s.poll.recipients)
	}
	s.poll = ps
	s.mu.Unlock()
	if oldTargets != nil {
		s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollClose,
		}}, oldTargets)
	}
	n := s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollOpen,
		Args: PollOpenArgs{Session: ps.id, Question: question, Options: clean, Deadline: ps.deadline},
	}}, targets)
	s.mu.Lock()
	if s.poll == ps {
		ps.sent = n
	}
	s.mu.Unlock()
	s.changed()
	return ps.id, n, nil
}

func (s *Server) StopPoll() {
	s.mu.Lock()
	var targets []string
	if s.poll != nil {
		s.poll.closed = true
		targets = sortedIDs(s.poll.recipients)
	}
	s.mu.Unlock()
	s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollClose,
	}}, targets)
	s.changed()
}

func (s *Server) PollState() PollSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := PollSession{}
	if s.poll == nil {
		return state
	}
	state.ID, state.Question, state.Deadline = s.poll.id, s.poll.question, s.poll.deadline
	state.TargetIDs = sortedIDs(s.poll.recipients)
	state.Sent = s.poll.sent
	state.Options = append([]string(nil), s.poll.options...)
	state.Open = !s.poll.closed && (s.poll.deadline == 0 || time.Now().Unix() < s.poll.deadline)
	for _, reply := range s.poll.replies {
		state.Replies = append(state.Replies, reply)
	}
	sort.Slice(state.Replies, func(i, j int) bool { return state.Replies[i].At.Before(state.Replies[j].At) })
	return state
}

func (s *Server) onPollReply(devID string, msg *PollReplyMsg) {
	s.mu.Lock()
	if s.poll == nil || s.poll.closed || s.poll.id != msg.Session || !s.poll.recipients[devID] || (s.poll.deadline > 0 && time.Now().Unix() >= s.poll.deadline) {
		s.mu.Unlock()
		return
	}
	valid := false
	for _, option := range s.poll.options {
		if option == msg.Option {
			valid = true
			break
		}
	}
	if !valid {
		s.mu.Unlock()
		return
	}
	label := devID
	if d := s.devices[devID]; d != nil {
		label = d.Label
	}
	if _, exists := s.poll.replies[devID]; !exists {
		s.poll.replies[devID] = PollReply{DeviceID: devID, Label: label, Option: msg.Option, At: time.Now()}
	}
	s.mu.Unlock()
	s.changed()
}

func (s *Server) HelpRequests() []HelpRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HelpRequest, 0, len(s.helpRequests))
	for _, request := range s.helpRequests {
		out = append(out, request)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func (s *Server) onHelp(devID string, msg *HelpMsg) {
	if msg.Kind != "raise" {
		return
	}
	s.mu.Lock()
	if _, exists := s.helpRequests[devID]; !exists {
		request := HelpRequest{DeviceID: devID, At: time.Now()}
		if d := s.devices[devID]; d != nil {
			request.Label, request.Name = d.Label, d.Name
		}
		s.helpRequests[devID] = request
	}
	s.mu.Unlock()
	s.changed()
}

func (s *Server) ClearHelp(id string) {
	s.mu.Lock()
	delete(s.helpRequests, id)
	s.mu.Unlock()
	s.sendMsg(id, Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("help-%d", s.cmdSeq.Add(1)), Op: OpClearHelp,
	}})
	s.changed()
}

// syncClientActivities restores current classroom prompts after a client
// reconnects and removes stale prompts that were closed while it was offline.
func (s *Server) syncClientActivities(deviceID string) {
	// A student demo is not resumed after a transport reconnect. Stop its
	// viewer first, then restore any teacher-owned activities below.
	s.stopDemoForDevice(deviceID)
	now := time.Now().Unix()
	s.mu.Lock()
	var signArgs *SignInOpenArgs
	if current := s.sign; current != nil && !current.closed && current.recipients[deviceID] &&
		(current.deadline == 0 || now < current.deadline) && current.replies[deviceID].DeviceID == "" {
		signArgs = &SignInOpenArgs{Session: current.id, Deadline: current.deadline}
	}
	var pollArgs *PollOpenArgs
	if current := s.poll; current != nil && !current.closed && current.recipients[deviceID] &&
		(current.deadline == 0 || now < current.deadline) && current.replies[deviceID].DeviceID == "" {
		pollArgs = &PollOpenArgs{Session: current.id, Question: current.question,
			Options: append([]string(nil), current.options...), Deadline: current.deadline}
	}
	_, raised := s.helpRequests[deviceID]
	watch := s.screenWatch[deviceID]
	s.mu.Unlock()

	if signArgs != nil {
		s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInOpen, Args: *signArgs,
		}})
	} else {
		s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInClose,
		}})
	}
	if pollArgs != nil {
		s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollOpen, Args: *pollArgs,
		}})
	} else {
		s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollClose,
		}})
	}
	s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("help-%d", s.cmdSeq.Add(1)), Op: OpSetHelpState, Args: HelpStateArgs{Raised: raised},
	}})
	s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("watch-%d", s.cmdSeq.Add(1)), Op: OpScreenWatch, Args: ScreenWatchArgs{On: watch},
	}})
	// 广播恢复要走 SendCommand 等 ack：客户端组播加入失败会回 "tcp" 明细，
	// 届时把它标记为 TCP 兜底目标。异步派发，不阻塞 serve 读循环。
	if args, ok := s.CastArgs(deviceID); ok {
		go func() {
			ack, err := s.SendCommand(deviceID, OpCastStart, args)
			if err != nil || !ack.OK || ack.Detail == "tcp" {
				s.MarkCastTCP(deviceID)
			}
			// 发送期间广播可能已结束/换会话：复查，失效则补 cast.stop，
			// 否则客户端会守着僵尸组播与全屏窗。
			if _, still := s.CastArgs(deviceID); !still {
				s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
					ID: fmt.Sprintf("cast-%d", s.cmdSeq.Add(1)), Op: OpCastStop,
				}})
			}
		}()
	} else {
		s.sendMsg(deviceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("cast-%d", s.cmdSeq.Add(1)), Op: OpCastStop,
		}})
	}
}

func (s *Server) DemoState() DemoSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.demo == nil {
		return DemoSession{}
	}
	state := s.demo.DemoSession
	state.TargetIDs = append([]string(nil), state.TargetIDs...)
	return state
}

func (s *Server) StartDemo(sourceID string, targetIDs []string) (DemoSession, error) {
	targets := normalizeIDs(targetIDs)
	if len(targets) == 0 || sourceID == "" {
		return DemoSession{}, fmt.Errorf("请指定演示来源和至少一台接收设备")
	}
	s.mu.Lock()
	if s.demo != nil && s.demo.State != "" {
		s.mu.Unlock()
		return DemoSession{}, fmt.Errorf("已有学生演示正在进行")
	}
	source := s.sessions[sourceID]
	if source == nil {
		s.mu.Unlock()
		return DemoSession{}, fmt.Errorf("演示来源不在线")
	}
	for _, id := range targets {
		if id == sourceID || s.sessions[id] == nil {
			s.mu.Unlock()
			return DemoSession{}, fmt.Errorf("接收设备必须在线且不能是演示来源")
		}
	}
	label := sourceID
	targetLabels := make([]string, 0, len(targets))
	if d := s.devices[sourceID]; d != nil {
		label = d.Label
	}
	for _, id := range targets {
		recipientLabel := id
		if d := s.devices[id]; d != nil {
			recipientLabel = d.Label
			if d.Name != "" && d.Name != d.Label {
				recipientLabel += " · " + d.Name
			}
		}
		targetLabels = append(targetLabels, recipientLabel)
	}
	state := &demoState{DemoSession: DemoSession{
		State: "pending", SessionID: "d-" + RandHex(12), SourceID: sourceID,
		SourceLabel: label, TargetIDs: targets, RequestedAt: time.Now(),
	}}
	s.demo = state
	s.mu.Unlock()
	if !s.sendMsg(sourceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoRequest,
		Args: DemoRequestArgs{Session: state.SessionID, TargetIDs: targets, TargetLabels: targetLabels},
	}}) {
		s.mu.Lock()
		if s.demo == state {
			s.demo = nil
		}
		s.mu.Unlock()
		return DemoSession{}, fmt.Errorf("无法联系演示来源")
	}
	s.changed()
	return state.DemoSession, nil
}

func (s *Server) StopDemo() {
	s.mu.Lock()
	state := s.demo
	s.demo = nil
	s.mu.Unlock()
	if state == nil {
		return
	}
	s.sendMsg(state.SourceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
		ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoStop,
	}})
	for _, id := range state.TargetIDs {
		s.sendMsg(id, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoStop,
		}})
	}
	s.StopCastFrames(state.TargetIDs)
	s.changed()
}

func (s *Server) stopDemoForDevice(deviceID string) {
	s.mu.Lock()
	state := s.demo
	stop := state != nil && (state.State == "pending" || state.State == "active") && state.SourceID == deviceID
	if !stop && state != nil && (state.State == "pending" || state.State == "active") {
		for _, id := range state.TargetIDs {
			if id == deviceID {
				stop = true
				break
			}
		}
	}
	s.mu.Unlock()
	if stop {
		s.StopDemo()
	}
}

func (s *Server) onDemo(devID string, msg *DemoMsg) {
	s.mu.Lock()
	state := s.demo
	if state == nil || state.SessionID != msg.Session || state.SourceID != devID {
		s.mu.Unlock()
		return
	}
	switch msg.Phase {
	case "accept":
		if state.State != "pending" {
			s.mu.Unlock()
			return
		}
		state.State = "active"
		targets := append([]string(nil), state.TargetIDs...)
		s.mu.Unlock()
		s.sendMsg(devID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoStart, Args: DemoStartArgs{Session: msg.Session},
		}})
		for _, id := range targets {
			s.sendMsg(id, Msg{Type: MsgCommand, Cmd: &CommandMsg{
				ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoViewer, Args: CastStartArgs{Mode: "window"},
			}})
		}
		s.changed()
	case "decline":
		if state.State != "pending" {
			s.mu.Unlock()
			return
		}
		state.State, state.Err = "", "学生拒绝了演示请求"
		s.mu.Unlock()
		s.changed()
	case "stop":
		s.mu.Unlock()
		s.StopDemo()
	case "frame":
		if state.State != "active" || time.Since(state.lastFrame) < 500*time.Millisecond {
			s.mu.Unlock()
			return
		}
		state.lastFrame = time.Now()
		targets := append([]string(nil), state.TargetIDs...)
		s.mu.Unlock()
		jpeg, err := base64.StdEncoding.DecodeString(msg.JPEG)
		if err == nil && len(jpeg) > 0 && len(jpeg) <= 700<<10 {
			s.QueueCastFrame(targets, jpeg)
		}
	}
}

// FileTransfers returns a sorted snapshot of current/recent transfers.
func (s *Server) FileTransfers() []FileProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileProgress, 0, len(s.transfers))
	for _, transfer := range s.transfers {
		out = append(out, *transfer)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// SendFile starts one managed transfer per online target; progress is reported asynchronously.
func (s *Server) SendFile(ids []string, path string) int {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxTransferSize {
		return 0
	}
	queued := 0
	for _, id := range s.OnlineIDs(ids) {
		s.mu.Lock()
		cc := s.sessions[id]
		dev := s.devices[id]
		s.mu.Unlock()
		if cc == nil {
			continue
		}
		tid := "f-" + RandHex(12)
		label := id
		if dev != nil && dev.Label != "" {
			label = dev.Label
		}
		s.mu.Lock()
		s.pruneTransfersLocked(time.Now())
		s.transfers[tid] = &FileProgress{ID: tid, DeviceID: id, Label: label,
			Name: filepath.Base(path), Size: info.Size(), At: time.Now()}
		ack := make(chan FileMsg, 1)
		s.fileAcks[tid] = ack
		s.mu.Unlock()
		queued++
		go s.fileSend(cc, tid, path, info.Size(), ack)
	}
	if queued > 0 {
		s.changed()
	}
	return queued
}

func (s *Server) fileSend(cc *clientConn, tid, path string, size int64, ack <-chan FileMsg) {
	mark := func(err string) {
		s.mu.Lock()
		if transfer := s.transfers[tid]; transfer != nil {
			if err != "" {
				transfer.Err = err
			}
			transfer.Done = true
		}
		delete(s.fileAcks, tid)
		s.mu.Unlock()
		s.changed()
	}
	f, err := os.Open(path)
	if err != nil {
		mark(err.Error())
		return
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		mark(err.Error())
		return
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		mark(err.Error())
		return
	}
	if !s.fileMsg(cc, FileMsg{ID: tid, Phase: "offer", Name: filepath.Base(path), Size: size}) {
		mark("发送 offer 失败")
		return
	}
	select {
	case result := <-ack:
		if result.Phase != "ready" {
			if result.Err == "" {
				result.Err = "客户端拒绝接收文件"
			}
			mark(result.Err)
			return
		}
	case <-time.After(45 * time.Second):
		mark("等待客户端准备接收超时")
		return
	}
	buf := make([]byte, FileChunk)
	seq, sent := 0, int64(0)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if !s.fileMsg(cc, FileMsg{ID: tid, Phase: "chunk", Seq: seq, Data: base64.StdEncoding.EncodeToString(buf[:n])}) {
				mark("发送数据块失败")
				return
			}
			sent += int64(n)
			seq++
			if seq%16 == 0 {
				s.mu.Lock()
				if transfer := s.transfers[tid]; transfer != nil {
					transfer.Sent = sent
				}
				s.mu.Unlock()
				s.changed()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			mark(readErr.Error())
			return
		}
	}
	if !s.fileMsg(cc, FileMsg{ID: tid, Phase: "done", Count: seq, SHA256: digest}) {
		mark("发送完成标记失败")
		return
	}
	select {
	case result := <-ack:
		if result.Phase != "complete" || !strings.EqualFold(result.SHA256, digest) {
			if result.Err == "" {
				result.Err = "客户端未确认文件校验"
			}
			mark(result.Err)
			return
		}
		s.mu.Lock()
		if transfer := s.transfers[tid]; transfer != nil {
			transfer.Sent = size
		}
		s.mu.Unlock()
		mark("")
	case <-time.After(transferWait(size)):
		mark("等待客户端完成回执超时")
	}
}

func (s *Server) fileMsg(cc *clientConn, fm FileMsg) bool {
	if err := cc.sess.sendWithTimeout(Msg{Type: MsgFile, File: &fm}, 15*time.Second); err != nil {
		_ = cc.sess.conn.Close()
		return false
	}
	return true
}

func (s *Server) onFileMsg(devID string, msg *FileMsg) {
	switch msg.Phase {
	case "ready":
		s.mu.Lock()
		ack := s.fileAcks[msg.ID]
		transfer := s.transfers[msg.ID]
		if transfer != nil && transfer.DeviceID == devID && !transfer.In && ack != nil {
			select {
			case ack <- *msg:
			default:
			}
		}
		s.mu.Unlock()
	case "progress":
		s.mu.Lock()
		if transfer := s.transfers[msg.ID]; transfer != nil && transfer.DeviceID == devID && !transfer.In && msg.Seq >= 0 {
			maxSeq := int((transfer.Size + FileChunk - 1) / FileChunk)
			if msg.Seq <= maxSeq {
				transfer.Sent = min(transfer.Size, int64(msg.Seq)*FileChunk)
			}
		}
		s.mu.Unlock()
	case "complete":
		s.mu.Lock()
		ack := s.fileAcks[msg.ID]
		transfer := s.transfers[msg.ID]
		if transfer != nil && transfer.DeviceID == devID && !transfer.In {
			select {
			case ack <- *msg:
			default:
			}
		}
		s.mu.Unlock()
	case "error":
		s.mu.Lock()
		ack := s.fileAcks[msg.ID]
		transfer := s.transfers[msg.ID]
		if transfer != nil && transfer.DeviceID == devID && !transfer.In {
			transfer.Err, transfer.Done = msg.Err, true
			if ack != nil {
				select {
				case ack <- *msg:
				default:
				}
			}
		}
		s.mu.Unlock()
		s.changed()
	case "offer":
		s.recvBegin(devID, msg)
	case "chunk":
		s.recvChunk(devID, msg)
	case "done":
		s.recvDone(devID, msg)
	}
}

func (s *Server) recvBegin(devID string, msg *FileMsg) {
	if msg.Size < 0 || msg.Size > MaxStudentUploadSize {
		s.rejectIncomingOffer(devID, msg, fmt.Errorf("学生提交文件不能超过 128 MiB"))
		return
	}
	key := incomingKey(devID, msg.ID)
	if err := s.reserveIncoming(devID, msg.ID, msg.Size); err != nil {
		s.rejectIncomingOffer(devID, msg, err)
		return
	}
	in, err := beginIncoming(s.RecvDir(), devID, msg.ID, msg.Name, msg.Size)
	if err != nil {
		s.releaseIncomingReservation(key)
		s.rejectIncomingOffer(devID, msg, err)
		return
	}
	label := devID
	s.mu.Lock()
	if _, reserved := s.inReservations[key]; !reserved || s.sessions[devID] == nil {
		delete(s.inReservations, key)
		s.mu.Unlock()
		in.abort()
		return
	}
	delete(s.inReservations, key)
	if d := s.devices[devID]; d != nil && d.Label != "" {
		label = d.Label
	}
	s.inFiles[key] = in
	s.pruneTransfersLocked(time.Now())
	tid := incomingProgressID(devID, msg.ID)
	s.transfers[tid] = &FileProgress{ID: tid, DeviceID: devID, Label: label,
		Name: in.name, Size: msg.Size, In: true, At: time.Now()}
	s.mu.Unlock()
	s.changed()
	if !s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "ready"}}) {
		s.failIncoming(devID, msg.ID, "无法通知接收端准备文件")
	}
}

func (s *Server) reserveIncoming(devID, transferID string, size int64) error {
	if !safeTransferID.MatchString(transferID) {
		return fmt.Errorf("非法传输编号")
	}
	ownerDir, err := safeReceiveDir(s.RecvDir(), devID)
	if err != nil {
		return err
	}
	stored, byOwner, err := scanReceiveUsage(filepath.Dir(ownerDir))
	if err != nil {
		return fmt.Errorf("无法检查接收目录配额: %w", err)
	}
	owner := filepath.Base(ownerDir)
	key := incomingKey(devID, transferID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFiles[key] != nil {
		return fmt.Errorf("该文件传输编号已在使用")
	}
	if _, reserved := s.inReservations[key]; reserved {
		return fmt.Errorf("该文件传输编号已在使用")
	}

	activeFiles, activeBytes := 0, int64(0)
	deviceFiles, deviceBytes := 0, int64(0)
	for _, in := range s.inFiles {
		activeFiles++
		activeBytes += in.expectedSize
		if in.devID == devID {
			deviceFiles++
			deviceBytes += in.expectedSize
		}
	}
	for _, reservation := range s.inReservations {
		activeFiles++
		activeBytes += reservation.size
		if reservation.deviceID == devID {
			deviceFiles++
			deviceBytes += reservation.size
		}
	}
	if activeFiles >= maxIncomingTransfersGlobal || deviceFiles >= maxIncomingTransfersPerDevice {
		return fmt.Errorf("文件接收并发已满，请稍后重试")
	}
	if activeBytes+size > maxIncomingBytesGlobal || deviceBytes+size > maxIncomingBytesPerDevice {
		return fmt.Errorf("文件接收暂存空间已满，请稍后重试")
	}
	ownerUsage := byOwner[owner]
	if stored.files+activeFiles+1 > maxReceivedFilesGlobal || ownerUsage.files+deviceFiles+1 > maxReceivedFilesPerDevice {
		return fmt.Errorf("文件保留数量已达上限，请先清理已接收文件")
	}
	if stored.bytes+activeBytes+size > maxReceivedBytesGlobal || ownerUsage.bytes+deviceBytes+size > maxReceivedBytesPerDevice {
		return fmt.Errorf("文件保留空间已达上限，请先清理已接收文件")
	}
	s.inReservations[key] = incomingReservation{deviceID: devID, size: size, at: time.Now()}
	return nil
}

func (s *Server) releaseIncomingReservation(key string) {
	s.mu.Lock()
	delete(s.inReservations, key)
	s.mu.Unlock()
}

func (s *Server) rejectIncomingOffer(devID string, msg *FileMsg, cause error) {
	reason := cause.Error()
	if safeTransferID.MatchString(msg.ID) {
		key := incomingKey(devID, msg.ID)
		progressID := incomingProgressID(devID, msg.ID)
		s.mu.Lock()
		if s.inFiles[key] == nil {
			if _, reserved := s.inReservations[key]; !reserved {
				s.pruneTransfersLocked(time.Now())
				label := devID
				if d := s.devices[devID]; d != nil && d.Label != "" {
					label = d.Label
				}
				s.transfers[progressID] = &FileProgress{
					ID: progressID, DeviceID: devID, Label: label, Name: safeFilename(msg.Name),
					Size: max(0, msg.Size), Done: true, In: true, Err: reason, At: time.Now(),
				}
			}
		}
		s.mu.Unlock()
	}
	s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "error", Err: reason}})
	s.changed()
}

func (s *Server) recvChunk(devID string, msg *FileMsg) {
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		s.failIncoming(devID, msg.ID, "数据块编码无效")
		return
	}
	key := incomingKey(devID, msg.ID)
	s.mu.Lock()
	in := s.inFiles[key]
	s.mu.Unlock()
	if in == nil {
		return
	}
	if err := in.writeChunk(msg.Seq, data); err != nil {
		s.failIncoming(devID, msg.ID, err.Error())
		return
	}
	seq, written := in.progress()
	tid := incomingProgressID(devID, msg.ID)
	s.mu.Lock()
	if transfer := s.transfers[tid]; transfer != nil {
		transfer.Sent = written
	}
	s.mu.Unlock()
	if seq%16 == 0 {
		s.changed()
	}
}

func (s *Server) recvDone(devID string, msg *FileMsg) {
	key := incomingKey(devID, msg.ID)
	s.mu.Lock()
	in := s.inFiles[key]
	tid := incomingProgressID(devID, msg.ID)
	transfer := s.transfers[tid]
	s.mu.Unlock()
	if in == nil {
		s.mu.Lock()
		if transfer != nil {
			transfer.Done, transfer.Err = true, "接收会话不存在"
		}
		s.mu.Unlock()
		s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "error", Err: "接收会话不存在"}})
		s.changed()
		return
	}
	name, err := in.finish(msg.Count, msg.SHA256)
	s.mu.Lock()
	if s.inFiles[key] == in {
		delete(s.inFiles, key)
	}
	if transfer != nil {
		transfer.Done = true
		if err != nil {
			transfer.Err = err.Error()
		} else {
			transfer.Sent = transfer.Size
			transfer.Name = name
		}
	}
	s.mu.Unlock()
	if err != nil {
		s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "error", Err: err.Error()}})
	} else {
		s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: msg.ID, Phase: "complete", Name: name, Count: msg.Count, SHA256: msg.SHA256}})
		log.Printf("lan: 学生提交完成 %s <- %s", name, devID)
	}
	s.changed()
}

func (s *Server) failIncoming(devID, transferID, reason string) {
	key := incomingKey(devID, transferID)
	s.mu.Lock()
	in := s.inFiles[key]
	delete(s.inFiles, key)
	transfer := s.transfers[incomingProgressID(devID, transferID)]
	if transfer != nil {
		transfer.Done, transfer.Err = true, reason
	}
	s.mu.Unlock()
	if in != nil {
		in.abort()
	}
	s.sendMsg(devID, Msg{Type: MsgFile, File: &FileMsg{ID: transferID, Phase: "error", Err: reason}})
	s.changed()
}

func incomingKey(deviceID, transferID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(deviceID)) + ":" + transferID
}

func incomingProgressID(deviceID, transferID string) string {
	return "in-" + incomingKey(deviceID, transferID)
}

func (s *Server) SetRecvDir(dir string) {
	s.mu.Lock()
	s.recvDir = dir
	s.mu.Unlock()
	cleanupPartialFiles(dir)
}

func (s *Server) RecvDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recvDir
}

func (s *Server) cleanupIncomingForDevice(devID string) {
	s.mu.Lock()
	var removed []*incoming
	for key, in := range s.inFiles {
		if in.devID == devID {
			removed = append(removed, in)
			delete(s.inFiles, key)
			if transfer := s.transfers[incomingProgressID(in.devID, in.transferID)]; transfer != nil {
				transfer.Done, transfer.Err = true, "客户端断开，未完成文件已清理"
			}
		}
	}
	for key, reservation := range s.inReservations {
		if reservation.deviceID == devID {
			delete(s.inReservations, key)
		}
	}
	s.mu.Unlock()
	for _, in := range removed {
		in.abort()
	}
	if len(removed) > 0 {
		s.changed()
	}
}

func (s *Server) expireIncoming() {
	now := time.Now()
	s.mu.Lock()
	var expired []*incoming
	for key, in := range s.inFiles {
		if now.Sub(in.at) > time.Hour {
			expired = append(expired, in)
			delete(s.inFiles, key)
			if transfer := s.transfers[incomingProgressID(in.devID, in.transferID)]; transfer != nil {
				transfer.Done, transfer.Err = true, "文件接收超时，未完成文件已清理"
			}
		}
	}
	for key, reservation := range s.inReservations {
		if now.Sub(reservation.at) > time.Hour {
			delete(s.inReservations, key)
		}
	}
	s.mu.Unlock()
	for _, in := range expired {
		in.abort()
	}
	if len(expired) > 0 {
		s.changed()
	}
}

// expireClassroomSessions closes timed sessions and preserves their results
// for the management UI. Replies are rejected as soon as the deadline passes.
func (s *Server) expireClassroomSessions() {
	now := time.Now().Unix()
	s.mu.Lock()
	var signTargets, pollTargets []string
	var demoExpired *demoState
	if s.sign != nil && !s.sign.closed && s.sign.deadline > 0 && now >= s.sign.deadline {
		s.sign.closed = true
		signTargets = sortedIDs(s.sign.recipients)
	}
	if s.poll != nil && !s.poll.closed && s.poll.deadline > 0 && now >= s.poll.deadline {
		s.poll.closed = true
		pollTargets = sortedIDs(s.poll.recipients)
	}
	// 学生 60s 未响应演示请求视为拒绝：否则 pending 会一直卡住广播。
	if s.demo != nil && s.demo.State == "pending" && time.Since(s.demo.RequestedAt) > 60*time.Second {
		s.demo.State, s.demo.Err = "", "学生未响应演示请求"
		demoExpired = s.demo
	}
	s.mu.Unlock()
	if demoExpired != nil {
		// 清掉学生端残留的待响应提示。
		s.sendMsg(demoExpired.SourceID, Msg{Type: MsgCommand, Cmd: &CommandMsg{
			ID: fmt.Sprintf("demo-%d", s.cmdSeq.Add(1)), Op: OpDemoStop,
		}})
		s.changed()
	}
	if signTargets != nil {
		s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{ID: fmt.Sprintf("signin-%d", s.cmdSeq.Add(1)), Op: OpSignInClose}}, signTargets)
		s.changed()
	}
	if pollTargets != nil {
		s.Broadcast(Msg{Type: MsgCommand, Cmd: &CommandMsg{ID: fmt.Sprintf("poll-%d", s.cmdSeq.Add(1)), Op: OpPollClose}}, pollTargets)
		s.changed()
	}
}

func (s *Server) pruneTransfersLocked(now time.Time) bool {
	changed := false
	type oldTransfer struct {
		id string
		at time.Time
	}
	var done []oldTransfer
	for id, transfer := range s.transfers {
		if transfer.Done {
			done = append(done, oldTransfer{id: id, at: transfer.At})
			if now.Sub(transfer.At) > fileProgressRetention {
				delete(s.transfers, id)
				changed = true
			}
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].at.Before(done[j].at) })
	for _, transfer := range done {
		if len(s.transfers) < 500 {
			break
		}
		if _, ok := s.transfers[transfer.id]; ok {
			delete(s.transfers, transfer.id)
			changed = true
		}
	}
	return changed
}

// NormalizeIDs 去空去重并保持顺序；nil/空输入返回 nil。
func NormalizeIDs(ids []string) []string { return normalizeIDs(ids) }

func normalizeIDs(ids []string) []string {
	if ids == nil {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// OnlineIDs resolves an empty selection to every online client and filters stale IDs.
func (s *Server) OnlineIDs(ids []string) []string {
	selected := normalizeIDs(ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(selected) == 0 {
		selected = make([]string, 0, len(s.sessions))
		for id := range s.sessions {
			selected = append(selected, id)
		}
		sort.Strings(selected)
		return selected
	}
	out := make([]string, 0, len(selected))
	for _, id := range selected {
		if s.sessions[id] != nil {
			out = append(out, id)
		}
	}
	return out
}

func sortedIDs(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
