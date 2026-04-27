// Package uploadtoken HMAC 签名/校验"OSS 直传 commit token"。
//
// 设计:
//   - 服务端在 PresignUpload 阶段签发 token,绑定 (document_id, oss_key, expires_at)
//   - 客户端 PUT 文件到 OSS 后,带 token 回 CommitUpload
//   - 服务端用同 secret 校验:防止客户端篡改 doc_id / oss_key 把别人的 OSS 对象
//     commit 到自己的 doc 里
//
// Token 编码:`<base64url(payload_json)>.<base64url(hmac_sha256)>` —— 一个 ASCII 段,
// 安全过 URL / JSON / HTTP header。
//
// Secret 来源:进程启动时 crypto/rand 生成 32 byte,**不持久化**。
// 重启后所有 in-flight token 失效(用户最多重试一次,可接受)。
// 优点:零配置,无需 rotate 机制,泄露面只在内存。
package uploadtoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SecretLen 进程启动时随机生成的 secret 长度(字节)。
const SecretLen = 32

// Payload commit token 的明文载荷。
//
// Payload 字段对齐"server 校验时需要的全部约束":
//   - DocumentID 防跨 doc 误用(共享文档场景);附件场景置 0
//   - OSSKey 防跨对象重用
//   - ExpiresAt 防长期有效
//   - BaseVersion(可空)乐观锁:client RMW 模式拿 download 时的 version 传进来,
//     commit 校验 doc.current_version 是否仍等于这个 base — 不等就是 LOST UPDATE
//     场景,拒绝 commit 让 client re-download。空字符串跳过校验(向后兼容老 client +
//     "盲写"场景如直接 upload 不基于现有版本)。
//
// ── 附件场景(channel attachment;DocumentID/BaseVersion 不用)──
//   - ChannelID 防跨 channel 误用 + commit 时鉴权 / 落 channel_id
//   - MimeType 防 client 用一份 PUT URL 切到不同 mime(签名时绑定的 Content-Type
//     即对应 MimeType,但 token 里再存一遍方便 commit 落表 + 双重校验)
//   - Filename 透传到 commit(可空)
type Payload struct {
	DocumentID  uint64 `json:"d,omitempty"`
	OSSKey      string `json:"k"`
	ExpiresAt   int64  `json:"e"`           // unix seconds
	BaseVersion string `json:"b,omitempty"` // sha256 of the base doc version, "" = skip optimistic check

	// 附件场景专用字段(doc 场景留空)。
	ChannelID uint64 `json:"c,omitempty"`
	MimeType  string `json:"m,omitempty"`
	Filename  string `json:"f,omitempty"`
}

// Signer 单例 token 签发器,持有进程级 secret。
type Signer struct {
	secret []byte
}

// NewSigner 生成新 secret 构造 Signer。secret 来自 crypto/rand。
//
// 失败仅在 rand.Read 错(几乎不可能)。
func NewSigner() (*Signer, error) {
	s := make([]byte, SecretLen)
	if _, err := rand.Read(s); err != nil {
		return nil, fmt.Errorf("uploadtoken: gen secret: %w", err)
	}
	return &Signer{secret: s}, nil
}

// NewSignerWithSecret 给定 secret 构造(测试 / 持久化场景用)。
func NewSignerWithSecret(secret []byte) *Signer {
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return &Signer{secret: cp}
}

// Sign 生成 token。expires 必须是未来时间(本函数不校验,留给调用方决定 ttl)。
func (s *Signer) Sign(p Payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("uploadtoken: marshal payload: %w", err)
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(body)
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sum), nil
}

// ErrInvalidToken token 解析或 HMAC 校验失败。统一返这个,不细分原因(防侧信道)。
var ErrInvalidToken = errors.New("uploadtoken: invalid token")

// ErrTokenExpired token 已过期。和 ErrInvalidToken 分开 —— 调用方可以告诉用户"重抢"。
var ErrTokenExpired = errors.New("uploadtoken: token expired")

// Verify 校验 token + 返载荷。
//
// 校验顺序:
//  1. 拆 base64 — 失败 ErrInvalidToken
//  2. 重算 HMAC 比对 — 失败 ErrInvalidToken
//  3. ExpiresAt 比 now — 过期 ErrTokenExpired
//
// 注意 expires 检查在 HMAC 之后:防"猜 expires 提早绕过 HMAC"的侧信道(虽极弱)。
func (s *Signer) Verify(token string) (*Payload, error) {
	parts := splitDot(token)
	if len(parts) != 2 {
		return nil, ErrInvalidToken
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(want, sig) {
		return nil, ErrInvalidToken
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > p.ExpiresAt {
		return nil, ErrTokenExpired
	}
	return &p, nil
}

// splitDot 极简 splitN(避开 strings 包,无 import 成本)。
func splitDot(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
