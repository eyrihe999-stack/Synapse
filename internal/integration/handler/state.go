// state.go OAuth state 参数的 HMAC 签名 + 校验。
//
// OAuth 回调是 Feishu 端 302 过来的,没带我们的 JWT —— 用户身份必须靠 state 参数回传。
// 为了防 CSRF(攻击者诱导用户点自己的授权 URL,让受害者账号绑上攻击者的飞书账号),
// state 里要:
//   1. 携带"这次授权是给哪个 Synapse user / org 的"
//   2. 用服务端密钥签名,防止篡改
//   3. 有过期时间(5-10 分钟),防重放
//
// 设计选择:不依赖 Redis / DB 临时存 state —— 无状态 HMAC 自包含,
// 部署一个 Synapse 进程或多实例负载均衡都一样工作,无状态迁移问题。
package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// stateTTL state 的有效期。5 分钟 = 用户从点"连接飞书"到授权完成的合理上限,
// 超过极可能是攻击者在用过期 state 重放。
const stateTTL = 5 * time.Minute

// stateVersion 预留一个字节做 state 格式版本号,未来改格式时能区分。
const stateVersion byte = 1

// StateSigner HMAC-SHA256 签名器。secret 通常复用 JWT secret(已是强随机),
// 也可以传独立 secret 做域隔离。
type StateSigner struct {
	secret []byte
}

// NewStateSigner secret 为空或过短(<16 字节)返 error,避免部署期漏填。
func NewStateSigner(secret []byte) (*StateSigner, error) {
	if len(secret) < 16 {
		return nil, errors.New("state signer: secret must be >= 16 bytes")
	}
	s := make([]byte, len(secret))
	copy(s, secret)
	return &StateSigner{secret: s}, nil
}

// statePayload 签名前的 plain bytes 布局:
//   [0]       version (1 byte)
//   [1..8]    user_id (big-endian uint64)
//   [9..16]   org_id  (big-endian uint64)
//   [17..24]  exp_unix (big-endian int64)
//   [25..40]  csrf random (16 bytes)
//   [41..72]  hmac-sha256 signature(over bytes[0..41])
//
// 整体 73 bytes → base64url 约 98 字符,URL 友好。
const (
	statePayloadLen   = 41
	stateSignatureLen = 32
	stateTotalLen     = statePayloadLen + stateSignatureLen
)

// Sign 生成可嵌入 OAuth URL 的 state 字符串。
// csrf 随机值是为了让同一 (user, org, exp) 每次生成不同 state,迫使攻击者无法预测。
func (s *StateSigner) Sign(userID, orgID uint64) (string, error) {
	payload := make([]byte, statePayloadLen)
	payload[0] = stateVersion
	binary.BigEndian.PutUint64(payload[1:9], userID)
	binary.BigEndian.PutUint64(payload[9:17], orgID)
	binary.BigEndian.PutUint64(payload[17:25], uint64(time.Now().Add(stateTTL).Unix()))
	if _, err := rand.Read(payload[25:41]); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	sig := mac.Sum(nil)

	full := make([]byte, stateTotalLen)
	copy(full, payload)
	copy(full[statePayloadLen:], sig)
	return base64.RawURLEncoding.EncodeToString(full), nil
}

// Verify 反向:解码 + 检签名 + 检过期 → 返 (userID, orgID)。
// 失败统一返 ErrInvalidState —— 不给攻击者细粒度错误提示。
func (s *StateSigner) Verify(state string) (uint64, uint64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil || len(raw) != stateTotalLen {
		return 0, 0, ErrInvalidState
	}
	payload := raw[:statePayloadLen]
	sig := raw[statePayloadLen:]

	if payload[0] != stateVersion {
		return 0, 0, ErrInvalidState
	}

	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, sig) {
		return 0, 0, ErrInvalidState
	}

	userID := binary.BigEndian.Uint64(payload[1:9])
	orgID := binary.BigEndian.Uint64(payload[9:17])
	exp := int64(binary.BigEndian.Uint64(payload[17:25]))
	if time.Now().Unix() > exp {
		return 0, 0, ErrStateExpired
	}
	return userID, orgID, nil
}

// ErrInvalidState / ErrStateExpired handler 根据此区分 400 / 410 语义。
var (
	ErrInvalidState = errors.New("invalid oauth state")
	ErrStateExpired = errors.New("oauth state expired")
)
