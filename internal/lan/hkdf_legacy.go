//go:build !go1.25

package lan

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

// hkdfKey 与 hkdf.go 相同，供 go1.25 以下工具链使用
// （Win7 兼容 agent 以 go1.21 构建，x/crypto 作 HKDF 实现）。
func hkdfKey(secret, salt []byte, info string) ([]byte, error) {
	key := make([]byte, 32)
	_, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, []byte(info)), key)
	return key, err
}
