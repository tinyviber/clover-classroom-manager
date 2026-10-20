package lan

// 分发与持久化：
//   - 管理端身份 Ed25519 密钥对持久化到 dataDir/admin.key（公钥稳定 = 管理端身份）
//   - 设备注册表持久化到 dataDir/registry.json（重启不丢离线设备）
//   - 客户端配置 client.json 随安装包下发：管理端地址 + 公钥 pin +
//     机器 ID/token（首启自动生成并回写——在还原软件的非还原态运行一次即固化）

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ---- 管理端身份 ----

// LoadOrCreateIdentity 从 path 读管理端私钥，不存在则生成并写入。
func LoadOrCreateIdentity(path string) (Identity, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) == ed25519.PrivateKeySize {
			return Identity{Priv: ed25519.PrivateKey(b)}, nil
		}
		return Identity{}, fmt.Errorf("%s: 身份文件损坏", path)
	}
	id, err := GenIdentity()
	if err != nil {
		return id, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return id, err
	}
	if err := os.WriteFile(path, id.Priv, 0o600); err != nil {
		return id, err
	}
	return id, nil
}

// Fingerprint 公钥指纹（SHA256 前 9 字节 hex），用于人工核对。
func (id Identity) Fingerprint() string {
	h := sha256.Sum256(id.Pub())
	return "SHA256:" + hex.EncodeToString(h[:9])
}

// PubB64 公钥 base64，写进客户端配置。
func (id Identity) PubB64() string {
	return base64.StdEncoding.EncodeToString(id.Pub())
}

// DecodePub 解析 base64 公钥。
func DecodePub(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("非法管理端公钥")
	}
	return ed25519.PublicKey(b), nil
}

// ---- 客户端配置 ----

// AgentConfig 是随安装包下发到学生机的配置。
type AgentConfig struct {
	// 管理端连接信息（生成安装包时写入）
	AdminAddr string `json:"adminAddr"` // "192.168.31.1:47823"，可空=纯发现
	AdminPub  string `json:"adminPub"`  // base64 管理端公钥（pin），可空=TOFU
	Room      string `json:"room"`      // 机房名，如 "301"

	// 首启自动生成并回写（每台机器唯一）
	ID    string `json:"id"`    // 稳定机器 ID
	Token string `json:"token"` // enrollment token

	// 可选覆盖：默认取主机名/DefaultLabel
	Name  string `json:"name,omitempty"`
	Label string `json:"label,omitempty"`
}

// LoadAgentConfig 读客户端配置；文件不存在返回空配置（不报错）。
func LoadAgentConfig(path string) (*AgentConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &AgentConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c AgentConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: 配置损坏: %w", path, err)
	}
	return &c, nil
}

// Save 写回配置（首启生成 id/token 后固化）。
func (c *AgentConfig) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// DemoToken returns a stable, per-device token for agents spawned inside the
// management process. It is keyed by the persisted admin identity and is for
// local demos only; deployed clients must keep their own random client.json token.
func DemoToken(id Identity, deviceID string) string {
	if len(id.Priv) != ed25519.PrivateKeySize || deviceID == "" {
		return ""
	}
	h := hmac.New(sha256.New, id.Priv)
	_, _ = h.Write([]byte("lab-assistant/demo-agent/token/v1\x00"))
	_, _ = h.Write([]byte(deviceID))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// EnsureIdentity 缺 id/token 时生成（不回写，调用方决定何时 Save）。
func (c *AgentConfig) EnsureIdentity() bool {
	changed := false
	if c.ID == "" {
		c.ID = "pc-" + hex.EncodeToString(randBytes(6))
		changed = true
	}
	if c.Token == "" {
		c.Token = hex.EncodeToString(randBytes(16))
		changed = true
	}
	return changed
}

// PubKey 解析 pin 的管理端公钥；空或非法返回 nil（调用方按 TOFU 处理）。
func (c *AgentConfig) PubKey() ed25519.PublicKey {
	pub, err := DecodePub(c.AdminPub)
	if err != nil {
		return nil
	}
	return pub
}

// ToAgent 配置 → Agent（Name 缺省取主机名）。
func (c *AgentConfig) ToAgent() *Agent {
	name := c.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	a := NewAgent(c.ID, name)
	a.Label = c.Label
	if a.Label == "" {
		a.Label = DefaultLabel(name)
	}
	a.Token = c.Token
	a.DirectAddr = c.AdminAddr
	a.PinnedPub = c.PubKey()
	a.MAC, a.IP = firstNIC()
	return a
}

// RandHex 生成 n 字节随机 hex 串。
func RandHex(n int) string { return hex.EncodeToString(randBytes(n)) }
