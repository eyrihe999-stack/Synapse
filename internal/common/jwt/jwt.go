package jwt

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrInvalidToken token 解析或签名校验失败(格式错 / 签名错 / iss 不匹配)。
	ErrInvalidToken = errors.New("invalid token")
	// ErrExpiredToken token 已过期。中间件据此返回 401 + reason=token_expired 让客户端刷新。
	ErrExpiredToken = errors.New("token has expired")
	// ErrInvalidTokenClaims token 解析成功但 claims 类型不是 CustomClaims。
	ErrInvalidTokenClaims = errors.New("invalid token claims")
)

// TokenType 区分 access token 和 refresh token。写进 claims.Type,中间件据此拒绝类型不符的请求。
type TokenType string

const (
	AccessToken  TokenType = "access"
	RefreshToken TokenType = "refresh"
)

// CustomClaims JWT payload。除标准 RegisteredClaims 外塞 user_id / email / device_id 等业务字段。
type CustomClaims struct {
	UserID   uint64    `json:"user_id"`
	Email    string    `json:"email"`
	DeviceID string    `json:"device_id,omitempty"`
	Type     TokenType `json:"type"`
	Role     string    `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// JWTConfig JWTManager 构造参数。SecretKey 必填,其余零值走默认(access 15m / refresh 7d / issuer=synapse)。
type JWTConfig struct {
	SecretKey            string
	AccessTokenDuration  time.Duration
	RefreshTokenDuration time.Duration
	Issuer               string
}

// JWTManager 负责 access / refresh token 的签发和校验。构造走 NewJWTManager。
type JWTManager struct {
	config JWTConfig
}

func NewJWTManager(config JWTConfig) *JWTManager {
	if config.Issuer == "" {
		config.Issuer = "synapse"
	}
	if config.AccessTokenDuration == 0 {
		config.AccessTokenDuration = 15 * time.Minute
	}
	if config.RefreshTokenDuration == 0 {
		config.RefreshTokenDuration = 7 * 24 * time.Hour
	}
	return &JWTManager{config: config}
}

func (m *JWTManager) GenerateToken(userID uint64, email, deviceID string, tokenType TokenType) (string, time.Time, error) {
	var duration time.Duration
	if tokenType == AccessToken {
		duration = m.config.AccessTokenDuration
	} else {
		duration = m.config.RefreshTokenDuration
	}

	now := time.Now().UTC()
	expiresAt := now.Add(duration)

	jti, err := generateJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate JTI: %w", err)
	}

	claims := CustomClaims{
		UserID:   userID,
		Email:    email,
		DeviceID: deviceID,
		Type:     tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    m.config.Issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(m.config.SecretKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, expiresAt, nil
}

func (m *JWTManager) GenerateAccessToken(userID uint64, email, deviceID string) (string, time.Time, error) {
	return m.GenerateToken(userID, email, deviceID, AccessToken)
}

func (m *JWTManager) GenerateRefreshToken(userID uint64, email, deviceID string) (string, time.Time, error) {
	return m.GenerateToken(userID, email, deviceID, RefreshToken)
}

func (m *JWTManager) ValidateToken(tokenString string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(m.config.SecretKey), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*CustomClaims)
	if !ok || !token.Valid {
		return nil, ErrInvalidTokenClaims
	}

	return claims, nil
}

func (m *JWTManager) ValidateAccessToken(tokenString string) (*CustomClaims, error) {
	claims, err := m.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.Type != AccessToken {
		return nil, fmt.Errorf("%w: expected access token, got %s", ErrInvalidToken, claims.Type)
	}
	return claims, nil
}

func (m *JWTManager) ValidateRefreshToken(tokenString string) (*CustomClaims, error) {
	claims, err := m.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.Type != RefreshToken {
		return nil, fmt.Errorf("%w: expected refresh token, got %s", ErrInvalidToken, claims.Type)
	}
	return claims, nil
}

//sayso-lint:ignore unused-export
func (m *JWTManager) GetTokenExpiresIn(expiresAt time.Time) int {
	duration := time.Until(expiresAt)
	if duration < 0 {
		return 0
	}
	return int(duration.Seconds())
}

func (m *JWTManager) GetAccessTokenDuration() int {
	return int(m.config.AccessTokenDuration.Seconds())
}

//sayso-lint:ignore unused-export
func (m *JWTManager) GetRefreshTokenDuration() int {
	return int(m.config.RefreshTokenDuration.Seconds())
}

func (m *JWTManager) RefreshTokenDuration() time.Duration {
	return m.config.RefreshTokenDuration
}

func generateJTI() (string, error) {
	b := make([]byte, 16)
	//sayso-lint:ignore err-swallow
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

//sayso-lint:ignore unused-export,godoc-error-undoc
func ExtractUserIDFromToken(tokenString string) (uint64, error) {
	//sayso-lint:ignore err-swallow
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, &CustomClaims{})
	if err != nil {
		return 0, err
	}
	claims, ok := token.Claims.(*CustomClaims)
	if !ok {
		return 0, ErrInvalidTokenClaims
	}
	return claims.UserID, nil
}
