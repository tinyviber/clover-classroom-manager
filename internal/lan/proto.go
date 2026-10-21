package lan

// 协议定义：发现层 UDP 广播 + 控制层 TCP 加密长连接。
// 握手之后线路上只跑 Msg JSON（AEAD 封装）。
//
// 发现：客户端 UDP 广播 "LABH_DISCOVER_V1" 到 UDPPort；
// 管理端单播回复 "LABH_BEACON_V1" || tcpPort(2B) || adminPub(32B)，
// 客户端拿信标里的公钥做 pin，再连 TCP。

import (
	"encoding/binary"
	"time"
)

const (
	// UDPPort 是发现广播端口；TCPPort 是管理端控制连接端口。
	UDPPort = 47822
	TCPPort = 47823

	DiscoverMagic = "LABH_DISCOVER_V1"
	BeaconMagic   = "LABH_BEACON_V1"

	// 心跳与离线判定
	HeartbeatInterval = 3 * time.Second
	OfflineAfter      = 12 * time.Second
	CommandTimeout    = 5 * time.Second
)

// MsgType 枚举。
type MsgType string

const (
	MsgHello     MsgType = "hello"        // C→S 注册（握手后第一条）
	MsgStatus    MsgType = "status"       // C→S 周期心跳 + 状态
	MsgCommand   MsgType = "command"      // S→C 下发指令
	MsgAck       MsgType = "ack"          // 双向确认
	MsgAlert     MsgType = "alert"        // C→S 脱控/异常事件上报
	MsgScreen    MsgType = "screen.frame" // C→S 屏幕缩略图帧
	MsgCast      MsgType = "cast.frame"   // S→C 屏幕广播帧
	MsgSignReply MsgType = "signin.reply" // C→S 签到回执
	MsgFile      MsgType = "file"         // 双向文件传输
	MsgHelp      MsgType = "class.help"   // C→S 举手/求助
	MsgPollReply MsgType = "poll.reply"   // C→S 单题投票
	MsgDemo      MsgType = "demo.control" // 演示同意/停止与只读画面帧
)

// Msg 是线路上唯一的信封类型。
type Msg struct {
	Type   MsgType       `json:"t"`
	Hello  *HelloMsg     `json:"h,omitempty"`
	Status *StatusMsg    `json:"s,omitempty"`
	Cmd    *CommandMsg   `json:"c,omitempty"`
	Ack    *AckMsg       `json:"a,omitempty"`
	Alert  *AlertMsg     `json:"e,omitempty"`
	Screen *ScreenMsg    `json:"sc,omitempty"`
	Cast   *CastMsg      `json:"cc,omitempty"`
	Sign   *SignReplyMsg `json:"sr,omitempty"`
	File   *FileMsg      `json:"f,omitempty"`
	Help   *HelpMsg      `json:"hp,omitempty"`
	Poll   *PollReplyMsg `json:"pr,omitempty"`
	Demo   *DemoMsg      `json:"dm,omitempty"`
}

// HelloMsg 握手后客户端首条消息：自报家门。
type HelloMsg struct {
	ID      string `json:"id"`    // 机器唯一 ID（装机生成，持久化）
	Name    string `json:"name"`  // 计算机名
	Label   string `json:"label"` // 机器编号（取计算机名后几位，或由管理端分配）
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Version string `json:"version"`
	Token   string `json:"token"` // enrollment token：首次上线由管理端记住
}

// StatusMsg 周期上报。
type StatusMsg struct {
	HelperOn bool     `json:"on"`
	Features []string `json:"features"`
	Uptime   int64    `json:"uptime"`
}

// CommandMsg 管理端下发。Op 取值见下方常量。
type CommandMsg struct {
	ID   string `json:"id"` // 指令 ID，ack 回带
	Op   string `json:"op"`
	Args any    `json:"args,omitempty"`
}

const (
	OpSetHelper   = "helper.set"   // args: {"on": true}
	OpSetFeatures = "features.set" // args: {"keys": [...]}
	OpLockScreen  = "screen.lock"   // args: {"reason": "..."}——黑屏+锁键鼠
	OpUnlockScreen = "screen.unlock"
	OpRestart     = "helper.restart" // 学生机重启（shutdown /r）
	OpShutdown    = "helper.shutdown" // 学生机关机（shutdown /s）

	OpScreenWatch  = "screen.watch" // args: {"on": bool}——开/关缩略图流
	OpCastStart    = "cast.start"   // args: CastStartArgs（含组播会话参数）
	OpCastStop     = "cast.stop"
	OpSignInOpen   = "signin.open" // args: {"session": "...", "deadline": unix}
	OpSignInClose  = "signin.close"
	OpClearHelp    = "help.clear"
	OpSetHelpState = "help.state"
	OpPollOpen     = "poll.open"
	OpPollClose    = "poll.close"
	OpDemoRequest  = "demo.request"
	OpDemoStart    = "demo.start"
	OpDemoViewer   = "demo.viewer"
	OpDemoStop     = "demo.stop"
	OpCollectOpen  = "collect.open" // 收取文件：学生提交到管理端接收目录
)

type SetHelperArgs struct {
	On bool `json:"on"`
}

type SetFeaturesArgs struct {
	Keys []string `json:"keys"`
}

type LockScreenArgs struct {
	Reason string `json:"reason"`
}

// AckMsg 对 Command 的应答。Detail 携带可选补充信息
// （如 cast.start 应答里的 "tcp"=组播不可用、走 TCP 兜底）。
type AckMsg struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Err    string `json:"err,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AlertMsg 客户端主动上报的脱控事件。
type AlertMsg struct {
	Kind   string `json:"kind"` // net_down / proc_killed / usb_detected / ...
	Detail string `json:"detail"`
}

// ---- v0.0.2：屏幕 / 广播 / 签到 / 文件 ----

// ScreenWatchArgs 开关客户端的缩略图上报。
type ScreenWatchArgs struct {
	On bool `json:"on"`
}

// ScreenMsg C→S 缩略图帧（JPEG，base64）。
type ScreenMsg struct {
	JPEG string `json:"j"`
}

// CastStartArgs 广播模式 + 组播会话参数。
// Group 为空表示本次广播走纯 TCP（管理端侧组播也不可用）。
// Gen 是单调递增的广播代数：学生端拒绝比当前代数旧的指令，
// 解决「停掉旧广播立刻开新广播」时旧 cast.start 晚到造成的复活。
type CastStartArgs struct {
	Gen     uint32 `json:"gen"`               // 广播代数（服务端单调递增）
	Mode    string `json:"mode"`              // "fullscreen" | "window"
	Group   string `json:"group,omitempty"`   // 组播地址
	Port    int    `json:"port,omitempty"`    // 组播端口
	Session uint32 `json:"session,omitempty"` // 组播会话号（帧 nonce 的一部分）
	Key     string `json:"key,omitempty"`     // base64(AES-256 会话密钥)
}

// CastStopArgs 结束广播；Gen 匹配当前代才生效（0 = 无条件停，兼容旧端）。
type CastStopArgs struct {
	Gen uint32 `json:"gen,omitempty"`
}

// CollectOpenArgs 收取文件请求：学生端弹出提交界面。
type CollectOpenArgs struct {
	Session string `json:"session"`
}

// CastMsg S→C 广播帧（JPEG，base64）。
type CastMsg struct {
	JPEG string `json:"j"`
}

// SignInOpenArgs 发起签到。
type SignInOpenArgs struct {
	Session  string `json:"ss"`
	Deadline int64  `json:"dl"` // unix 秒；0 = 不限时
}

// SignReplyMsg C→S 学生点击签到后的回执。
type SignReplyMsg struct {
	Session string `json:"ss"`
	Name    string `json:"n,omitempty"` // 学生自填姓名（可空）
}

// HelpMsg C→S 发起举手。管理端以客户端身份去重，教师清除后客户端可再次举手。
type HelpMsg struct {
	Kind string `json:"kind"` // raise
}

type HelpStateArgs struct {
	Raised bool `json:"raised"`
}

type PollOpenArgs struct {
	Session  string   `json:"session"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Deadline int64    `json:"deadline"` // Unix 秒；0 = 手动关闭
}

type PollReplyMsg struct {
	Session string `json:"session"`
	Option  string `json:"option"`
}

// DemoRequestArgs 只请求来源学生同意，不在同意前采集或转发画面。
type DemoRequestArgs struct {
	Session      string   `json:"session"`
	TargetIDs    []string `json:"targetIds"`
	TargetLabels []string `json:"targetLabels"`
}

type DemoStartArgs struct {
	Session string `json:"session"`
}

// DemoMsg 客户端向管理端表达同意/拒绝/来源停止，或发送一帧来源画面。
type DemoMsg struct {
	Session string `json:"session"`
	Phase   string `json:"phase"` // accept|decline|stop|frame
	JPEG    string `json:"jpeg,omitempty"`
}

// FileMsg 双向文件传输。发送方走 offer → ready → chunk* → done；
// 接收方回 progress 与 complete/error 回执。
type FileMsg struct {
	ID     string `json:"id"`               // 传输会话 ID
	Phase  string `json:"ph"`               // offer|ready|chunk|done|progress|complete|error
	Name   string `json:"n,omitempty"`      // offer：文件名
	Size   int64  `json:"sz,omitempty"`     // offer：总字节
	Seq    int    `json:"q,omitempty"`      // chunk 序号 / progress 已收序号
	Count  int    `json:"count,omitempty"`  // 文件块总数（done/complete）
	SHA256 string `json:"sha256,omitempty"` // 文件内容完整性校验
	Data   string `json:"d,omitempty"`      // chunk：base64
	Err    string `json:"e,omitempty"`      // error：原因
}

// MaxCastJPEG 是 TCP 兜底广播帧的 JPEG 上限（b64 后仍 < maxFrame）。
const MaxCastJPEG = 2600 << 10

// FileChunk 每片原始字节数（b64 后 ~1.4 倍，留足 maxFrame 余量）。
const (
	FileChunk            = 48 << 10
	MaxTransferSize      = 1 << 30
	MaxStudentUploadSize = 128 << 20
)

// encodeBeacon 打包发现信标：magic || tcpPort || adminPub。
func encodeBeacon(tcpPort int, pub []byte) []byte {
	b := make([]byte, 0, len(BeaconMagic)+2+len(pub))
	b = append(b, BeaconMagic...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(tcpPort))
	b = append(b, p[:]...)
	return append(b, pub...)
}

// decodeBeacon 解析信标，返回 (tcpPort, adminPub, ok)。
func decodeBeacon(b []byte) (int, []byte, bool) {
	if len(b) != len(BeaconMagic)+2+32 || string(b[:len(BeaconMagic)]) != BeaconMagic {
		return 0, nil, false
	}
	port := int(binary.BigEndian.Uint16(b[len(BeaconMagic) : len(BeaconMagic)+2]))
	pub := append([]byte{}, b[len(BeaconMagic)+2:]...)
	return port, pub, true
}

func deadline(sec int) time.Time { return time.Now().Add(time.Duration(sec) * time.Second) }
func noDeadline() time.Time      { return time.Time{} }
