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
	MsgHello   MsgType = "hello"   // C→S 注册（握手后第一条）
	MsgStatus  MsgType = "status"  // C→S 周期心跳 + 状态
	MsgCommand MsgType = "command" // S→C 下发指令
	MsgAck     MsgType = "ack"     // 双向确认
	MsgAlert   MsgType = "alert"   // C→S 脱控/异常事件上报
)

// Msg 是线路上唯一的信封类型。
type Msg struct {
	Type   MsgType     `json:"t"`
	Hello  *HelloMsg   `json:"h,omitempty"`
	Status *StatusMsg  `json:"s,omitempty"`
	Cmd    *CommandMsg `json:"c,omitempty"`
	Ack    *AckMsg     `json:"a,omitempty"`
	Alert  *AlertMsg   `json:"e,omitempty"`
}

// HelloMsg 握手后客户端首条消息：自报家门。
type HelloMsg struct {
	ID      string `json:"id"`      // 机器唯一 ID（装机生成，持久化）
	Name    string `json:"name"`    // 计算机名
	Label   string `json:"label"`   // 机器编号（取计算机名后几位，或由管理端分配）
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Version string `json:"version"`
	Token   string `json:"token"`   // enrollment token：首次上线由管理端记住
}

// StatusMsg 周期上报。
type StatusMsg struct {
	HelperOn bool     `json:"on"`
	Features []string `json:"features"`
	Uptime   int64    `json:"uptime"`
}

// CommandMsg 管理端下发。Op 取值见下方常量。
type CommandMsg struct {
	ID   string `json:"id"`   // 指令 ID，ack 回带
	Op   string `json:"op"`
	Args any    `json:"args,omitempty"`
}

const (
	OpSetHelper   = "helper.set"   // args: {"on": true}
	OpSetFeatures = "features.set" // args: {"keys": [...]}
	OpLockScreen  = "screen.lock"  // args: {"reason": "..."}
	OpRestart     = "helper.restart"
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

// AckMsg 对 Command 的应答。
type AckMsg struct {
	ID  string `json:"id"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// AlertMsg 客户端主动上报的脱控事件。
type AlertMsg struct {
	Kind   string `json:"kind"` // net_down / proc_killed / usb_detected / ...
	Detail string `json:"detail"`
}

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

func deadline(sec int) time.Time  { return time.Now().Add(time.Duration(sec) * time.Second) }
func noDeadline() time.Time       { return time.Time{} }
