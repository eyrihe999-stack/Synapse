// crypto.go agent 模块密码学工具:
//   - AES-GCM 加密/解密(用于 auth token 存储)
//   - UUID 生成(session ID)
//   - auth token 生成
package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// MasterKey AES-GCM 主密钥的封装。
// 构造时校验长度必须是 32 字节(AES-256)。
type MasterKey struct {
	raw []byte
}

// NewMasterKey 构造一个 MasterKey。长度必须是 32 字节,否则返回 ErrAgentCryptoFailed。
func NewMasterKey(raw []byte) (*MasterKey, error) {
	if len(raw) != 32 {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("master key length must be 32 bytes, got %d: %w", len(raw), ErrAgentCryptoFailed)
	}
	buf := make([]byte, 32)
	copy(buf, raw)
	return &MasterKey{raw: buf}, nil
}

// EncryptSecret 用 AES-GCM 加密明文。输出格式:nonce(12B) || ciphertext || tag(16B)。
// 加密失败时返回 ErrAgentCryptoFailed。
func EncryptSecret(key *MasterKey, plaintext string) ([]byte, error) {
	if key == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("encrypt: master key nil: %w", ErrAgentCryptoFailed)
	}
	block, err := aes.NewCipher(key.raw)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("encrypt new cipher: %w: %w", err, ErrAgentCryptoFailed)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("encrypt new gcm: %w: %w", err, ErrAgentCryptoFailed)
	}
	nonce := make([]byte, gcm.NonceSize())
	//sayso-lint:ignore err-swallow
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("encrypt read nonce: %w: %w", err, ErrAgentCryptoFailed)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// DecryptSecret 与 EncryptSecret 配对,接收 nonce||ciphertext||tag 格式的 blob,返回明文。
// 解密失败时返回 ErrAgentCryptoFailed。
func DecryptSecret(key *MasterKey, blob []byte) (string, error) {
	if key == nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("decrypt: master key nil: %w", ErrAgentCryptoFailed)
	}
	block, err := aes.NewCipher(key.raw)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("decrypt new cipher: %w: %w", err, ErrAgentCryptoFailed)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("decrypt new gcm: %w: %w", err, ErrAgentCryptoFailed)
	}
	if len(blob) < gcm.NonceSize() {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("decrypt: blob too short: %w", ErrAgentCryptoFailed)
	}
	nonce := blob[:gcm.NonceSize()]
	ciphertext := blob[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("decrypt open: %w: %w", err, ErrAgentCryptoFailed)
	}
	return string(plaintext), nil
}

// GenerateSessionID 返回一个 UUID v4 字符串,用于 session 标识。
func GenerateSessionID() string {
	return uuid.NewString()
}

// GenerateAuthToken 生成一个 32 字节随机 auth token,返回 hex 编码(64 字符)。
// 随机数读取失败时返回 ErrAgentCryptoFailed。
func GenerateAuthToken() (string, error) {
	buf := make([]byte, 32)
	//sayso-lint:ignore err-swallow
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("generate auth token: %w: %w", err, ErrAgentCryptoFailed)
	}
	return hex.EncodeToString(buf), nil
}
