// crypto.go oauth 用到的密码学原语:PKCE S256 校验、随机 token 生成、JWT 签发与验证。
//
// 设计取向:全部依赖 stdlib + 已有 jwt 库,不引新依赖。JWT 用 HS256 对称签名 —— 当前
// 只有 synapse 自己既签发又验证,非对称没必要(将来若要把验证放到独立资源服务器或
// 供外部联邦验证,再切 RS256/ES256)。
package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
)

// randomURLSafe 生成 n 字节加密随机后 base64url encode(no padding)。
// 标准输出长度 = ceil(n * 4 / 3) 去掉 "=" 填充。n=32 → 43 chars,n=16 → 22 chars。
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken SHA256 hex。统一 64 char,和 schema 里 CHAR(64) 对齐。
// 用于把 code / refresh token 的原文哈希后落库,防 DB 泄露即凭证泄露。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// verifyPKCES256 验证 challenge == base64url(sha256(verifier))。
//
// PKCE(RFC 7636):
//   verifier  : 客户端保留的随机串,长度 43-128 chars,[A-Z a-z 0-9 - . _ ~]
//   challenge : base64url(sha256(verifier)) no padding,长度恒为 43
//
// 只接受 S256 method,plain 已在 OAuth 2.1 中废弃,此处不做 fallback。
func verifyPKCES256(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	// 长度已知定长,可以直接 ==;上层分支无分支差异不走时序攻击路径。
	return computed == challenge
}

// ─── JWT access token ──────────────────────────────────────────────────────

// oauthJWTClaims 自定义 claims。扩展 RegisteredClaims 拿 iss/exp/iat/jti 通用字段。
type oauthJWTClaims struct {
	OrgID    uint64 `json:"org_id"`
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
	jwt.RegisteredClaims
}

// jwtSigner 包装 signing key + issuer,方法集覆盖 sign / verify。无可变状态,并发安全。
type jwtSigner struct {
	secret []byte // HMAC-SHA256 密钥
	issuer string // 发到 iss claim 里;必须和 .well-known 里宣告的一致
}

// newJWTSigner secret 必须 ≥ 32 字节(HS256 建议);issuer 必须非空。
func newJWTSigner(secret []byte, issuer string) (*jwtSigner, error) {
	if len(secret) < 32 {
		return nil, errors.New("oauth: jwt signing key must be >= 32 bytes")
	}
	if issuer == "" {
		return nil, errors.New("oauth: jwt issuer must be non-empty")
	}
	return &jwtSigner{secret: secret, issuer: issuer}, nil
}

// sign 签一个 access token。
// typ header 固定为 oauth.AccessTokenJWTType,防和 web 登录 JWT 混用 —— verify 时会校验。
func (s *jwtSigner) sign(userID, orgID uint64, clientID, scope, jti string, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	claims := oauthJWTClaims{
		OrgID:    orgID,
		ClientID: clientID,
		Scope:    scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   strconv.FormatUint(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = oauth.AccessTokenJWTType
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, exp, nil
}

// verify 解析 + 验签 + 校验 exp / iss / typ header。
// 所有校验失败返统一 errTokenInvalid —— middleware 不需要区分具体原因。
func (s *jwtSigner) verify(tokenStr string) (*AccessTokenClaims, error) {
	tok, err := jwt.ParseWithClaims(tokenStr, &oauthJWTClaims{}, func(t *jwt.Token) (any, error) {
		// 强制 HS256,防 alg=none / alg=RS256 混淆攻击
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Method.Alg())
		}
		if typ, _ := t.Header["typ"].(string); typ != oauth.AccessTokenJWTType {
			return nil, fmt.Errorf("unexpected token type: %v", typ)
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, errTokenInvalid
	}
	if !tok.Valid {
		return nil, errTokenInvalid
	}
	c, ok := tok.Claims.(*oauthJWTClaims)
	if !ok || c.Issuer != s.issuer {
		return nil, errTokenInvalid
	}
	sub, err := strconv.ParseUint(c.Subject, 10, 64)
	if err != nil {
		return nil, errTokenInvalid
	}
	out := &AccessTokenClaims{
		Subject:   sub,
		OrgID:     c.OrgID,
		ClientID:  c.ClientID,
		Scope:     c.Scope,
		JTI:       c.ID,
		IssuedAt:  c.IssuedAt.Time,
		ExpiresAt: c.ExpiresAt.Time,
	}
	return out, nil
}

// errTokenInvalid middleware 返给调用侧的统一错误。不泄漏具体原因(签名错 vs 过期 vs typ 错),
// 外部 observation 不应该能区分,避免攻击者摸清服务器校验链。
var errTokenInvalid = errors.New("oauth: access token invalid")
