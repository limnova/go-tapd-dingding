// crypto 为持久化凭据提供带认证的加密能力。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const keyEnv = "APP_ENCRYPTION_KEY"

// Box 使用 AES-256-GCM 加密和解密数据。
type Box struct{ aead cipher.AEAD }

// FromEnvironment 从 APP_ENCRYPTION_KEY 创建加密盒。
func FromEnvironment() (*Box, error) {
	raw := strings.TrimSpace(os.Getenv(keyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is required and must contain a base64 or hex encoded 32-byte key", keyEnv)
	}
	key, err := decodeKey(raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", keyEnv, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create encryption box: %w", err)
	}
	return &Box{aead: aead}, nil
}

// GenerateKey 生成一个经过 Base64 编码的随机 32 字节密钥。
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(key), nil
}

// Encrypt 为明文添加认证并进行加密。
func (b *Box) Encrypt(plaintext string) (string, error) {
	if b == nil || b.aead == nil {
		return "", errors.New("encryption box is not initialized")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate encryption nonce: %w", err)
	}
	sealed := make([]byte, 0, len(nonce)+len(plaintext)+b.aead.Overhead())
	sealed = append(sealed, nonce...)
	sealed = b.aead.Seal(sealed, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 校验并解密由 Encrypt 生成的密文。
func (b *Box) Decrypt(encoded string) (string, error) {
	if b == nil || b.aead == nil {
		return "", errors.New("encryption box is not initialized")
	}
	data, err := decodeBase64(encoded)
	if err != nil {
		return "", fmt.Errorf("decode encrypted value: %w", err)
	}
	nonceSize := b.aead.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("encrypted value is too short")
	}
	plaintext, err := b.aead.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", errors.New("decrypt encrypted value: authentication failed")
	}
	return string(plaintext), nil
}

func decodeKey(raw string) ([]byte, error) {
	if key, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := base64.StdEncoding.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	// 不要静默地从短密码派生密钥。部署时如果确实使用口令，
	// 应先由运维人员显式计算哈希后再配置。
	return nil, errors.New("key must decode to exactly 32 bytes")
}

func decodeBase64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 encoding")
}
