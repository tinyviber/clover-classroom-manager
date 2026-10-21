//go:build go1.25

package lan

import (
	"crypto/hkdf"
	"crypto/sha256"
)

// hkdfKey 由握手共享密钥 + transcript 盐导出 32 字节会话密钥（HKDF-SHA256）。
func hkdfKey(secret, salt []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, salt, info, 32)
}
