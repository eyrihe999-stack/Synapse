// crypto.go agent 模块密码学工具:
//   - HMAC-SHA256 签名(sayso → agent 的鉴权)
//   - AES-GCM secret 加密/解密(master key 来自环境变量,不入 yaml)
//   - nonce / timestamp 生成与校验
//
// 独立可测:不依赖 repository / service,只依赖 Go stdlib 和 google/uuid。
package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// MasterKey AES-GCM 主密钥的封装。
//
// 构造时校验长度必须是 32 字节(AES-256)。实际值来自环境变量
// SAYSO_AGENT_SECRET_KEY(建议 base64 编码,由调用方解码后传入)。
type MasterKey struct {
	raw []byte
}

// NewMasterKey 构造一个 MasterKey。长度必须是 32 字节,否则返回 ErrAgentCryptoFailed。
func NewMasterKey(raw []byte) (*MasterKey, error) {
	if len(raw) != 32 {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("master key length must be 32 bytes, got %d: %w", len(raw), ErrAgentCryptoFailed)
	}
	// 拷贝一份,避免调用方后续修改
	buf := make([]byte, 32)
	copy(buf, raw)
	return &MasterKey{raw: buf}, nil
}

// GenerateSecret 生成一个 SecretByteLength 字节的随机 secret,返回 hex 字符串(128 字节)。
// secret 的明文只在创建/rotate 的 API 响应里返回一次,数据库只存加密形态。
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretByteLength)
	//sayso-lint:ignore err-swallow
	if _, err := io.ReadFull(rand.Reader, buf); err != nil { // n 字节数无需关心
		//sayso-lint:ignore log-coverage
		return "", fmt.Errorf("generate secret: %w: %w", err, ErrAgentCryptoFailed)
	}
	return hex.EncodeToString(buf), nil
}

// EncryptSecret 用 AES-GCM 加密 secret 明文。
// 输出格式:nonce(12 字节) || ciphertext || tag(16 字节)。
//
// 错误:key 为空、cipher 构造失败或随机 nonce 读取失败均返回 ErrAgentCryptoFailed。
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
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil { // n 字节数无需关心
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("encrypt read nonce: %w: %w", err, ErrAgentCryptoFailed)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// DecryptSecret 与 EncryptSecret 配对,接收 nonce||ciphertext||tag 格式的 blob。
//
// 错误:key 为空、blob 过短或 GCM 校验/解密失败均返回 ErrAgentCryptoFailed。
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

// ─── HMAC 签名 ────────────────────────────────────────────────────────────────

// SignatureInput 是 ComputeSignature 的输入参数。
type SignatureInput struct {
	// Secret agent 的 HMAC secret 明文(hex 字符串)
	Secret string
	// Timestamp Unix 秒
	Timestamp int64
	// Nonce 本次请求的唯一随机数(建议 UUID v4)
	Nonce string
	// Body HTTP 请求 body 原始字节(签名对 sha256 结果)
	Body []byte
}

// ComputeSignature 根据 SignatureInput 计算 HMAC-SHA256 签名。
//
// 签名原文:
//
//	hmac(secret, timestamp + "\n" + nonce + "\n" + sha256Hex(body))
//
// 返回 hex 编码的签名字符串,供 X-Sayso-Signature header 使用。
func ComputeSignature(in SignatureInput) string {
	bodyHash := sha256.Sum256(in.Body)
	bodyHex := hex.EncodeToString(bodyHash[:])

	mac := hmac.New(sha256.New, []byte(in.Secret))
	mac.Write([]byte(strconv.FormatInt(in.Timestamp, 10)))
	mac.Write([]byte("\n"))
	mac.Write([]byte(in.Nonce))
	mac.Write([]byte("\n"))
	mac.Write([]byte(bodyHex))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature 使用 constant-time 比较校验签名是否匹配。
func VerifySignature(in SignatureInput, given string) bool {
	expected := ComputeSignature(in)
	return hmac.Equal([]byte(expected), []byte(given))
}

// ─── Nonce / Timestamp ───────────────────────────────────────────────────────

// GenerateNonce 返回一个 UUID v4 字符串,用于一次性请求 nonce。
func GenerateNonce() string {
	return uuid.NewString()
}

// GenerateInvocationID 返回一个 invocation UUID(和 nonce 共用格式)。
func GenerateInvocationID() string {
	return uuid.NewString()
}

// CheckTimestampSkew 判断 ts 是否在当前时间 ±skewSeconds 的窗口内。
// 超出窗口返回 error(调用方翻译为 ErrGatewayAgentAuthFailed)。
func CheckTimestampSkew(ts int64, skewSeconds int) error {
	if skewSeconds <= 0 {
		skewSeconds = DefaultHMACTimestampSkewSeconds
	}
	now := time.Now().UTC().Unix()
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > int64(skewSeconds) {
		//sayso-lint:ignore log-coverage
		return errors.New("timestamp skew exceeded")
	}
	return nil
}
