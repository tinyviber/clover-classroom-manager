package lan

// 加密层：X25519 临时密钥交换 + 管理端 Ed25519 签名 + HKDF-SHA256 密钥导出
// + AES-256-GCM 帧封装。只用标准库，无外部依赖。
//
// 握手流程（客户端主动连接管理端 TCP）：
//   C→S: magic(8B) || ephPubC(32B) || nonceC(16B)
//   S→C: ephPubS(32B) || nonceS(16B) || ed25519.Sign(transcript)
// transcript = SHA256(双方握手字节)——签名防中间人；客户端 pin 管理端公钥。
// 之后每条消息 = u32 长度前缀 || AES-GCM(ciphertext)，nonce = 4B 0 || u64 seq。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

var hsMagic = []byte("LABHS001")

// Identity 是管理端的长期身份密钥对。
type Identity struct {
	Priv ed25519.PrivateKey
}

// GenIdentity 生成新的管理端身份（首次运行时调用并存盘）。
func GenIdentity() (Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return Identity{Priv: priv}, err
}

// Pub 返回管理端公钥（客户端 pin 它）。
func (id Identity) Pub() ed25519.PublicKey {
	return id.Priv.Public().(ed25519.PublicKey)
}

// session 是一条加密后的消息通道。
type session struct {
	conn    net.Conn
	enc     cipher.AEAD
	dec     cipher.AEAD
	seqSend uint64
	seqRecv uint64
	wmu     sync.Mutex
	rmu     sync.Mutex
}

// maxFrame 是单条加密消息的上限。广播 TCP 兜底帧可能到 ~2.6MB
// （b64 后 ~3.5MB），故留 4MiB。
const maxFrame = 4 << 20

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func seqNonce(seq uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[4:], seq)
	return n[:]
}

func transcriptHash(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// serverHandshake 在管理端侧完成握手，返回加密会话。
func serverHandshake(conn net.Conn, id Identity) (*session, error) {
	conn.SetDeadline(deadline(10))
	defer conn.SetDeadline(noDeadline())

	hdr := make([]byte, len(hsMagic)+32+16)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, fmt.Errorf("握手读取失败: %w", err)
	}
	if string(hdr[:len(hsMagic)]) != string(hsMagic) {
		return nil, errors.New("错误的握手魔数")
	}
	ephCPubBytes := hdr[len(hsMagic) : len(hsMagic)+32]
	// nonceC = hdr[len(hsMagic)+32:]，已含在 hdr 里进 transcript，无需单独引用

	ephS, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ephCPub, err := ecdh.X25519().NewPublicKey(ephCPubBytes)
	if err != nil {
		return nil, fmt.Errorf("非法客户端公钥: %w", err)
	}
	nonceS := randBytes(16)

	// transcript 固定顺序：C 头部 || S 响应前 48 字节
	transcript := transcriptHash(hdr, ephS.PublicKey().Bytes(), nonceS)
	sig := ed25519.Sign(id.Priv, transcript)

	resp := append(append(append([]byte{}, ephS.PublicKey().Bytes()...), nonceS...), sig...)
	if _, err := conn.Write(resp); err != nil {
		return nil, fmt.Errorf("握手响应发送失败: %w", err)
	}

	shared, err := ephS.ECDH(ephCPub)
	if err != nil {
		return nil, err
	}
	return buildSession(conn, shared, transcript, false)
}

// clientHandshake 在客户端侧完成握手；pinned 为管理端公钥（nil = TOFU 接受任意）。
func clientHandshake(conn net.Conn, pinned ed25519.PublicKey) (*session, ed25519.PublicKey, error) {
	conn.SetDeadline(deadline(10))
	defer conn.SetDeadline(noDeadline())

	ephC, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	nonceC := randBytes(16)
	hello := append(append(append([]byte{}, hsMagic...), ephC.PublicKey().Bytes()...), nonceC...)
	if _, err := conn.Write(hello); err != nil {
		return nil, nil, fmt.Errorf("握手发送失败: %w", err)
	}

	resp := make([]byte, 32+16+64)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, nil, fmt.Errorf("握手响应读取失败: %w", err)
	}
	ephSPubBytes := resp[:32]
	nonceS := resp[32:48]
	sig := resp[48:]

	transcript := transcriptHash(hello, ephSPubBytes, nonceS)
	if pinned == nil {
		return nil, nil, errors.New("缺少管理端公钥，无法验证签名")
	}
	if !ed25519.Verify(pinned, transcript, sig) {
		return nil, nil, errors.New("管理端签名验证失败（非本机房管理端或中间人）")
	}

	ephSPub, err := ecdh.X25519().NewPublicKey(ephSPubBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("非法管理端公钥: %w", err)
	}
	shared, err := ephC.ECDH(ephSPub)
	if err != nil {
		return nil, nil, err
	}
	s, err := buildSession(conn, shared, transcript, true)
	return s, pinned, err
}

// buildSession 由共享密钥 + 转录导出双向 AEAD 密钥。
// client=true 表示本端是客户端：c2s 为发送方向。
func buildSession(conn net.Conn, shared, transcript []byte, client bool) (*session, error) {
	kC2S, err := hkdfKey(shared, transcript, "lab-c2s")
	if err != nil {
		return nil, err
	}
	kS2C, err := hkdfKey(shared, transcript, "lab-s2c")
	if err != nil {
		return nil, err
	}
	encK, decK := kS2C, kC2S // 服务端视角
	if client {
		encK, decK = kC2S, kS2C
	}
	enc, err := newAEAD(encK)
	if err != nil {
		return nil, err
	}
	dec, err := newAEAD(decK)
	if err != nil {
		return nil, err
	}
	return &session{conn: conn, enc: enc, dec: dec}, nil
}

// send 加密并发出一条 Msg。
func (s *session) send(v any) error {
	return s.sendWithTimeout(v, 10*time.Second)
}

func (s *session) sendWithTimeout(v any, timeout time.Duration) error {
	pt, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if timeout > 0 {
		if err := s.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		defer s.conn.SetWriteDeadline(time.Time{})
	}
	ct := s.enc.Seal(nil, seqNonce(s.seqSend), pt, nil)
	frame := make([]byte, 4+len(ct))
	binary.BigEndian.PutUint32(frame, uint32(len(ct)))
	copy(frame[4:], ct)
	n, err := s.conn.Write(frame)
	if err != nil {
		_ = s.conn.Close()
		return err
	}
	if n != len(frame) {
		_ = s.conn.Close()
		return io.ErrShortWrite
	}
	s.seqSend++
	return nil
}

// recv 读取并解密一条 Msg。
func (s *session) recv(v any) error {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	var lb [4]byte
	if _, err := io.ReadFull(s.conn, lb[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n == 0 || n > maxFrame {
		return fmt.Errorf("非法帧长度 %d", n)
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(s.conn, ct); err != nil {
		return err
	}
	pt, err := s.dec.Open(nil, seqNonce(s.seqRecv), ct, nil)
	if err != nil {
		return fmt.Errorf("帧解密失败（seq %d）: %w", s.seqRecv, err)
	}
	s.seqRecv++
	return json.Unmarshal(pt, v)
}
