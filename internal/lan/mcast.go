package lan

// 屏幕广播的 UDP 组播通道（与极域电子教室同思路）：
// 教师端把每帧加密后切片，向一个管理域组播地址只发一次；
// 各学生机加入组播组接收，控制通道（TCP/AEAD）只负责下发
// 会话参数（组/端口/会话号/密钥）。组播不可用或丢包严重时，
// 个别客户端回退为 TCP 单播帧（QueueCastFrame 路径）。
//
// 线格式（多字节字段均大端）：
//   datagram = magic"CLCF"(4) | session(u32) | frame(u32) |
//              chunkIdx(u16) | chunkCnt(u16) | blobLen(u32) | chunk[] | tag(12)
//   blob = AES-256-GCM(sessionKey, nonce=session||frame) 加密的 JPEG
//   tag  = HMAC-SHA256(tagKey, header‖chunk) 截 12B，
//          tagKey = HKDF-SHA256(sessionKey, info="clcf-dgram")
// 每帧一个 GCM nonce：session(4B)||frameID(8B)，同会话内唯一。
// 缺片即整帧丢弃——视频流语义，靠下一帧恢复，不做重传。
// tag 先于重组验证：局域网内伪造的分片无法污染重组器（学生机没有密钥）。

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// CastGroup 是管理域（239.0.0.0/8）里的课堂广播组播地址。
	CastGroup   = "239.42.4.23"
	CastUDPPort = 47824

	castMagic    = 0x434C4346 // "CLCF"
	castHdrLen   = 20         // magic4 sess4 frame4 idx2 cnt2 tot4
	castChunkLen = 1360       // 单包负载上限，留足 1500 MTU 余量避免 IP 分片
	castTagLen   = 12         // 每数据报 HMAC-SHA256 截断 tag

	udpBufLen = 4 << 20 // 组播 socket 收发缓冲（单帧可达 ~1950 个分片连发）
)

// CastSessionParams 是 cast.start 指令携带的一次广播会话参数。
type CastSessionParams struct {
	Group   string
	Port    int
	Session uint32
	Key     []byte // 32B AES-256 会话密钥
}

// NewCastSession 生成一次广播的随机会话参数。
func NewCastSession() (CastSessionParams, error) {
	key := make([]byte, 32)
	var sid [4]byte
	if _, err := rand.Read(key); err != nil {
		return CastSessionParams{}, err
	}
	if _, err := rand.Read(sid[:]); err != nil {
		return CastSessionParams{}, err
	}
	return CastSessionParams{
		Group:   CastGroup,
		Port:    CastUDPPort,
		Session: binary.BigEndian.Uint32(sid[:]),
		Key:     key,
	}, nil
}

// castNonce 帧级 nonce：会话号 || 帧号。
func castNonce(session uint32, frame uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[:4], session)
	binary.BigEndian.PutUint64(n[4:], frame)
	return n[:]
}

// castTagKey 由会话密钥导出数据报认证密钥（与 GCM 帧密钥分离）。
func castTagKey(key []byte) ([]byte, error) {
	return hkdfKey(key, nil, "clcf-dgram")
}

// castTag 计算数据报认证 tag（覆盖头部+负载）。
func castTag(key, dg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(dg)
	return m.Sum(nil)[:castTagLen]
}

// ---- 发送端（管理端）----

// CastSender 把加密帧切片组播出去。
type CastSender struct {
	conn   *net.UDPConn
	dst    *net.UDPAddr
	aead   cipher.AEAD
	tagKey []byte
	sess   uint32
	mu     sync.Mutex
	frame  uint64
}

// NewCastSender 打开组播发送 socket；ifIP 指定出站网卡地址。
// 传 nil 时先按路由表探测组播出口 IP（多网卡/VPN 环境选错网卡会
// sendto: no route to host），探测失败再退回第一张局域网网卡。
func NewCastSender(p CastSessionParams, ifIP net.IP) (*CastSender, error) {
	if len(p.Key) != 32 {
		return nil, fmt.Errorf("组播会话密钥需 32 字节")
	}
	group := net.ParseIP(p.Group).To4()
	if group == nil {
		return nil, fmt.Errorf("非法组播地址 %q", p.Group)
	}
	aead, err := newAEAD(p.Key)
	if err != nil {
		return nil, err
	}
	tagKey, err := castTagKey(p.Key)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, err
	}
	if ifIP == nil {
		ifIP = multicastOutIP(conn, group)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		setErr = setMulticastOutOpt(fd, ifIP)
		setUDPBufOpt(fd, true, udpBufLen)
	}); err != nil {
		conn.Close()
		return nil, err
	}
	if setErr != nil {
		conn.Close()
		return nil, setErr
	}
	return &CastSender{
		conn: conn, aead: aead, tagKey: tagKey, sess: p.Session,
		dst: &net.UDPAddr{IP: group, Port: p.Port},
	}, nil
}

// multicastOutIP 用 UDP connect 探测内核会给该组播组选的源地址
// （即默认路由的出口 IP）；失败时退回第一张局域网 IPv4。
func multicastOutIP(conn *net.UDPConn, group net.IP) net.IP {
	probe, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: group, Port: CastUDPPort})
	if err == nil {
		if ip, ok := probe.LocalAddr().(*net.UDPAddr); ok {
			_ = probe.Close()
			if ip4 := ip.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() {
				return ip4
			}
		} else {
			_ = probe.Close()
		}
	}
	_ = conn // 保留签名：未来若需要结合既有 socket 的网卡探测
	if ips := LocalIPv4s(); len(ips) > 0 {
		return net.ParseIP(ips[0])
	}
	return nil
}

// Send 加密一帧并按 MTU 切片组播。返回发出的分片数。
func (s *CastSender) Send(jpeg []byte) (int, error) {
	if len(jpeg) == 0 || len(jpeg) > MaxCastJPEG {
		return 0, fmt.Errorf("帧大小 %d 非法", len(jpeg))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frame++
	blob := s.aead.Seal(nil, castNonce(s.sess, s.frame), jpeg, []byte("CLCF"))
	cnt := (len(blob) + castChunkLen - 1) / castChunkLen
	if cnt > 0xFFFF {
		return 0, fmt.Errorf("帧过大（%d 字节）", len(blob))
	}
	dgram := make([]byte, castHdrLen+castChunkLen+castTagLen)
	binary.BigEndian.PutUint32(dgram[0:4], castMagic)
	binary.BigEndian.PutUint32(dgram[4:8], s.sess)
	binary.BigEndian.PutUint32(dgram[8:12], uint32(s.frame))
	binary.BigEndian.PutUint16(dgram[14:16], uint16(cnt))
	binary.BigEndian.PutUint32(dgram[16:20], uint32(len(blob)))
	sent := 0
	for i := 0; i < cnt; i++ {
		end := (i + 1) * castChunkLen
		if end > len(blob) {
			end = len(blob)
		}
		n := copy(dgram[castHdrLen:], blob[i*castChunkLen:end])
		binary.BigEndian.PutUint16(dgram[12:14], uint16(i))
		d := dgram[:castHdrLen+n]
		copy(dgram[castHdrLen+n:], castTag(s.tagKey, d))
		if _, err := s.conn.WriteToUDP(d[:castHdrLen+n+castTagLen], s.dst); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

func (s *CastSender) Close() { s.conn.Close() }

// ---- 接收端（学生机）----

// CastReceiver 在若干网卡上加入组播组并重组分片。
type CastReceiver struct {
	conns []*net.UDPConn
	asm   *castAssembler
}

// JoinCast 在所有可用组播网卡上加入广播组（含回环，便于本机演示）。
func JoinCast(p CastSessionParams) (*CastReceiver, error) {
	if len(p.Key) != 32 {
		return nil, fmt.Errorf("组播会话密钥需 32 字节")
	}
	group := net.ParseIP(p.Group).To4()
	if group == nil {
		return nil, fmt.Errorf("非法组播地址 %q", p.Group)
	}
	aead, err := newAEAD(p.Key)
	if err != nil {
		return nil, err
	}
	tagKey, err := castTagKey(p.Key)
	if err != nil {
		return nil, err
	}
	gaddr := &net.UDPAddr{IP: group, Port: p.Port}
	var ifaces []*net.Interface
	if all, err := net.Interfaces(); err == nil {
		for i := range all {
			it := &all[i]
			if it.Flags&net.FlagUp != 0 && it.Flags&net.FlagMulticast != 0 {
				ifaces = append(ifaces, it)
			}
		}
	}
	ifaces = append(ifaces, nil) // 兜底：内核默认组播接口
	r := &CastReceiver{asm: &castAssembler{aead: aead, tagKey: tagKey, sess: p.Session}}
	var lastErr error
	for _, ifi := range ifaces {
		conn, err := net.ListenMulticastUDP("udp4", ifi, gaddr)
		if err != nil {
			lastErr = err
			continue
		}
		if raw, err := conn.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) { setUDPBufOpt(fd, false, udpBufLen) })
		}
		r.conns = append(r.conns, conn)
	}
	if len(r.conns) == 0 {
		return nil, fmt.Errorf("加入组播组失败: %w", lastErr)
	}
	return r, nil
}

// Run 读取组播数据报并把完整解密帧交给 onFrame，直到 ctx 结束。
func (r *CastReceiver) Run(ctx context.Context, onFrame func(jpeg []byte)) {
	done := make(chan struct{})
	defer close(done)
	var wg sync.WaitGroup
	for _, conn := range r.conns {
		wg.Add(1)
		go func(c *net.UDPConn) {
			defer wg.Done()
			go func() {
				select {
				case <-ctx.Done():
				case <-done:
				}
				c.Close()
			}()
			buf := make([]byte, castHdrLen+castChunkLen+castTagLen+64)
			for {
				n, _, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if jpeg, ok := r.asm.add(buf[:n]); ok {
					onFrame(jpeg)
				}
			}
		}(conn)
	}
	wg.Wait()
}

// Close 退出组播组。
func (r *CastReceiver) Close() {
	for _, c := range r.conns {
		c.Close()
	}
}

// castAssembler 按帧号重组分片：只保留最新帧，旧帧分片一律丢弃。
type castAssembler struct {
	aead   cipher.AEAD
	tagKey []byte
	sess   uint32
	mu     sync.Mutex
	frame  uint32 // 当前重组中的帧号
	cnt    int    // 分片总数
	tot    int    // blob 总长
	buf    []byte
	got    []bool
	gotN   int
	at     time.Time // 首片到达时间（久等不全就丢弃重置）
}

func (a *castAssembler) add(d []byte) ([]byte, bool) {
	// 先验 tag：未认证的数据报不能触碰重组状态（防局域网伪造断流）。
	if len(d) < castHdrLen+castTagLen {
		return nil, false
	}
	body, tag := d[:len(d)-castTagLen], d[len(d)-castTagLen:]
	if !hmac.Equal(castTag(a.tagKey, body), tag) {
		return nil, false
	}
	if binary.BigEndian.Uint32(body[0:4]) != castMagic {
		return nil, false
	}
	sess := binary.BigEndian.Uint32(body[4:8])
	if sess != a.sess {
		return nil, false
	}
	frame := binary.BigEndian.Uint32(body[8:12])
	idx := int(binary.BigEndian.Uint16(body[12:14]))
	cnt := int(binary.BigEndian.Uint16(body[14:16]))
	tot := int(binary.BigEndian.Uint32(body[16:20]))
	payload := body[castHdrLen:]
	if cnt <= 0 || cnt > 4096 || idx >= cnt || tot <= 0 || tot > MaxCastJPEG+16 || len(payload) == 0 || len(payload) > castChunkLen {
		return nil, false
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if frame < a.frame {
		return nil, false // 迟到的旧帧分片
	}
	if frame > a.frame || a.buf == nil || time.Since(a.at) > 2*time.Second {
		// 新帧（或上次重组停滞）→ 重置缓冲
		a.frame, a.cnt, a.tot, a.gotN = frame, cnt, tot, 0
		a.buf = make([]byte, tot)
		a.got = make([]bool, cnt)
		a.at = time.Now()
	}
	if a.cnt != cnt || a.tot != tot || a.got[idx] {
		return nil, false
	}
	off := idx * castChunkLen
	if off+len(payload) > a.tot {
		return nil, false
	}
	copy(a.buf[off:], payload)
	a.got[idx] = true
	a.gotN++
	if a.gotN != a.cnt {
		return nil, false
	}
	blob := a.buf
	a.buf, a.got = nil, nil
	a.frame++ // 之后同帧迟到分片按旧帧丢弃
	jpeg, err := a.aead.Open(nil, castNonce(a.sess, uint64(frame)), blob, []byte("CLCF"))
	if err != nil {
		return nil, false
	}
	return jpeg, true
}
