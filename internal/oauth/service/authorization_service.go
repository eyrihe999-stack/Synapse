package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
)

// AuthorizationService 生成 authorization_code + token exchange + refresh rotate。
//
// 所有 token 明文**仅在返回时**存在;DB 只存 sha256 hash。
type AuthorizationService interface {
	// IssueCode consent 同意后生成 authorization_code。
	IssueCode(ctx context.Context, in IssueCodeInput) (code string, err error)

	// ExchangeCode /oauth/token?grant_type=authorization_code 的实现:
	// - 消费 code(原子;重放返错误 + 吊销该 code 签发的所有 token,RFC 6749 §10.5)
	// - 校验 PKCE verifier
	// - 校验 redirect_uri 和 code 保存的一致
	// - 校验 client credentials(authMethod=none 时免 secret)
	// - 生成 access_token + refresh_token
	ExchangeCode(ctx context.Context, in ExchangeCodeInput) (*TokenPair, error)

	// RefreshToken /oauth/token?grant_type=refresh_token:rotate 出新 access+refresh,
	// 旧 access/refresh 立即吊销。
	RefreshToken(ctx context.Context, in RefreshTokenInput) (*TokenPair, error)

	// RevokeByToken /oauth/revoke:按 access_token / refresh_token hash 路由,吊销。
	RevokeByToken(ctx context.Context, in RevokeTokenInput) error
}

// IssueCodeInput 生成 code 的参数(由 consent handler 构造)。
type IssueCodeInput struct {
	ClientID             string
	UserID               uint64
	AgentID              uint64 // 此 OAuth flow 绑定的 agent
	RedirectURI          string
	Scope                string
	PKCEChallenge        string
	PKCEChallengeMethod  string
	CodeTTL              time.Duration // 传入即可覆盖默认
}

// ExchangeCodeInput token exchange 参数。
type ExchangeCodeInput struct {
	Code          string
	RedirectURI   string
	ClientID      string
	ClientSecret  string // 可空(token_auth_method=none 的 public client)
	CodeVerifier  string // PKCE
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

// RefreshTokenInput refresh_token grant 参数。
type RefreshTokenInput struct {
	RefreshToken  string
	ClientID      string
	ClientSecret  string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

// RevokeTokenInput /oauth/revoke 参数。
type RevokeTokenInput struct {
	Token        string // 明文
	TokenType    string // "access_token" / "refresh_token";空时两种都尝试
	ClientID     string
	ClientSecret string
}

// TokenPair token 端点返给客户端的内容。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	Scope        string
	TokenType    string // 永远 "Bearer"
}

type authorizationService struct {
	repo          repository.Repository
	clientService ClientService
	log           logger.LoggerInterface
}

func newAuthorizationService(repo repository.Repository, clientService ClientService, log logger.LoggerInterface) AuthorizationService {
	return &authorizationService{repo: repo, clientService: clientService, log: log}
}

// IssueCode 入参假定已被 authorize handler 校验(client 存在、redirect_uri 属于该 client 等)。
func (s *authorizationService) IssueCode(ctx context.Context, in IssueCodeInput) (string, error) {
	if in.ClientID == "" || in.UserID == 0 || in.AgentID == 0 || in.RedirectURI == "" {
		return "", fmt.Errorf("issue code: missing required: %w", oauth.ErrOAuthInternal)
	}
	if in.PKCEChallenge == "" {
		return "", oauth.ErrPKCERequired
	}
	if in.PKCEChallengeMethod != oauth.PKCEMethodS256 {
		return "", oauth.ErrPKCEMethodInvalid
	}

	plain, err := randomBase64URL(oauth.TokenRandomBytes)
	if err != nil {
		return "", fmt.Errorf("gen code: %w: %w", err, oauth.ErrOAuthInternal)
	}
	plain = oauth.AuthorizationCodePfx + plain
	ttl := in.CodeTTL
	if ttl <= 0 {
		ttl = oauth.DefaultAuthorizationCodeTTL
	}
	row := &model.OAuthAuthorizationCode{
		CodeHash:            sha256Hex(plain),
		ClientID:            in.ClientID,
		UserID:              in.UserID,
		AgentID:             in.AgentID,
		RedirectURI:         in.RedirectURI,
		Scope:               in.Scope,
		PKCEChallenge:       in.PKCEChallenge,
		PKCEChallengeMethod: in.PKCEChallengeMethod,
		ExpiresAt:           time.Now().UTC().Add(ttl),
	}
	if err := s.repo.CreateAuthorizationCode(ctx, row); err != nil {
		return "", fmt.Errorf("insert code: %w: %w", err, oauth.ErrOAuthInternal)
	}
	return plain, nil
}

// ExchangeCode /oauth/token 的 grant_type=authorization_code 实现。
func (s *authorizationService) ExchangeCode(ctx context.Context, in ExchangeCodeInput) (*TokenPair, error) {
	// 1. 定位 client + 验凭证
	client, err := s.authClient(ctx, in.ClientID, in.ClientSecret)
	if err != nil {
		return nil, err
	}

	// 2. 找 code
	codeHash := sha256Hex(in.Code)
	codeRow, err := s.repo.FindAuthorizationCodeByHash(ctx, codeHash)
	if err != nil {
		return nil, fmt.Errorf("find code: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if codeRow == nil {
		return nil, oauth.ErrAuthorizationCodeExpired
	}

	// 3. 重放检测 —— 已消费再 exchange → RFC 6749 §10.5 要求吊销同链 token
	if codeRow.ConsumedAt != nil {
		s.log.WarnCtx(ctx, "oauth: authorization_code replay detected", map[string]any{
			"client_id": client.ClientID, "code_id": codeRow.ID,
		})
		// 吊销该 client 的 active tokens(保守策略,宁可错杀)
		now := time.Now().UTC()
		_, _ = s.repo.RevokeAccessTokensByClient(ctx, client.ClientID, now)
		_, _ = s.repo.RevokeRefreshTokensByClient(ctx, client.ClientID, now)
		return nil, oauth.ErrAuthorizationCodeAlreadyUsed
	}

	// 4. 过期
	if time.Now().UTC().After(codeRow.ExpiresAt) {
		return nil, oauth.ErrAuthorizationCodeExpired
	}

	// 5. client_id / redirect_uri 一致校验
	if codeRow.ClientID != client.ClientID {
		return nil, oauth.ErrInvalidClient
	}
	if codeRow.RedirectURI != in.RedirectURI {
		return nil, oauth.ErrOAuthInternal // redirect_uri mismatch;不暴露原因给 client
	}

	// 6. PKCE verifier 校验(S256)
	if err := verifyPKCE(codeRow.PKCEChallenge, in.CodeVerifier); err != nil {
		return nil, err
	}

	// 7. 原子消费 code
	affected, err := s.repo.ConsumeAuthorizationCode(ctx, codeRow.ID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("consume code: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if affected == 0 {
		return nil, oauth.ErrAuthorizationCodeAlreadyUsed
	}

	// 8. 签发 access + refresh
	return s.mintTokenPair(ctx, client.ClientID, codeRow.UserID, codeRow.AgentID, codeRow.Scope, in.AccessTTL, in.RefreshTTL)
}

// RefreshToken grant_type=refresh_token 的实现,含 token rotation。
func (s *authorizationService) RefreshToken(ctx context.Context, in RefreshTokenInput) (*TokenPair, error) {
	client, err := s.authClient(ctx, in.ClientID, in.ClientSecret)
	if err != nil {
		return nil, err
	}

	rtHash := sha256Hex(in.RefreshToken)
	rtRow, err := s.repo.FindRefreshTokenByHash(ctx, rtHash)
	if err != nil {
		return nil, fmt.Errorf("find refresh token: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if rtRow == nil {
		return nil, oauth.ErrRefreshTokenRevoked
	}
	if rtRow.ClientID != client.ClientID {
		return nil, oauth.ErrInvalidClient
	}
	if rtRow.RevokedAt != nil {
		// 重放:一个已被 rotate 的 refresh token 又被使用 → 吊销整条链
		s.log.WarnCtx(ctx, "oauth: refresh_token replay detected; revoking chain", map[string]any{
			"client_id": client.ClientID, "rt_id": rtRow.ID,
		})
		now := time.Now().UTC()
		_, _ = s.repo.RevokeAccessTokensByClient(ctx, client.ClientID, now)
		_, _ = s.repo.RevokeRefreshTokensByClient(ctx, client.ClientID, now)
		return nil, oauth.ErrRefreshTokenRevoked
	}
	if time.Now().UTC().After(rtRow.ExpiresAt) {
		return nil, oauth.ErrRefreshTokenExpired
	}

	// 先签发新 pair,再 revoke 旧 access/refresh(标记 rotated_to 指向新 refresh hash)
	pair, err := s.mintTokenPair(ctx, client.ClientID, rtRow.UserID, rtRow.AgentID, rtRow.Scope, in.AccessTTL, in.RefreshTTL)
	if err != nil {
		return nil, err
	}

	// 旧 access_token:revoke
	now := time.Now().UTC()
	if err := s.repo.RevokeAccessToken(ctx, rtRow.AccessTokenID, now); err != nil {
		s.log.WarnCtx(ctx, "oauth: revoke old access_token failed (non-fatal)", map[string]any{
			"at_id": rtRow.AccessTokenID, "err": err.Error(),
		})
	}
	// 旧 refresh_token:revoke + 填 rotated_to_token_hash
	newRTHash := sha256Hex(pair.RefreshToken)
	if err := s.repo.RevokeRefreshToken(ctx, rtRow.ID, now, newRTHash); err != nil {
		s.log.WarnCtx(ctx, "oauth: revoke old refresh_token failed (non-fatal)", map[string]any{
			"rt_id": rtRow.ID, "err": err.Error(),
		})
	}
	return pair, nil
}

// RevokeByToken /oauth/revoke。按 token_type_hint 找,找不到 fall back 另一种。
// 按 RFC 7009 §2.2,"revoke 不存在的 token 也返 200" —— 我们内部做 best-effort。
func (s *authorizationService) RevokeByToken(ctx context.Context, in RevokeTokenInput) error {
	client, err := s.authClient(ctx, in.ClientID, in.ClientSecret)
	if err != nil {
		return err
	}

	hash := sha256Hex(in.Token)
	now := time.Now().UTC()

	tryAccessToken := func() (bool, error) {
		row, err := s.repo.FindAccessTokenByHash(ctx, hash)
		if err != nil {
			return false, err
		}
		if row == nil || row.ClientID != client.ClientID {
			return false, nil
		}
		if err := s.repo.RevokeAccessToken(ctx, row.ID, now); err != nil {
			return false, err
		}
		return true, nil
	}
	tryRefreshToken := func() (bool, error) {
		row, err := s.repo.FindRefreshTokenByHash(ctx, hash)
		if err != nil {
			return false, err
		}
		if row == nil || row.ClientID != client.ClientID {
			return false, nil
		}
		if err := s.repo.RevokeRefreshToken(ctx, row.ID, now, ""); err != nil {
			return false, err
		}
		// 同时 revoke 对应 access_token
		_ = s.repo.RevokeAccessToken(ctx, row.AccessTokenID, now)
		return true, nil
	}

	switch in.TokenType {
	case "access_token":
		if _, err := tryAccessToken(); err != nil {
			return err
		}
	case "refresh_token":
		if _, err := tryRefreshToken(); err != nil {
			return err
		}
	default:
		if ok, err := tryAccessToken(); err != nil {
			return err
		} else if !ok {
			if _, err := tryRefreshToken(); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────

// authClient 定位 client + 验 secret(token_auth_method=none 的 public client 免 secret)。
func (s *authorizationService) authClient(ctx context.Context, clientID, clientSecret string) (*model.OAuthClient, error) {
	client, err := s.clientService.FindActiveByClientID(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("find client: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if client == nil {
		return nil, oauth.ErrInvalidClient
	}
	if client.TokenAuthMethod == oauth.TokenAuthNone {
		return client, nil // public client,只靠 PKCE 防 code 截获
	}
	if !s.clientService.VerifyClientSecret(client, clientSecret) {
		return nil, oauth.ErrInvalidClient
	}
	return client, nil
}

// mintTokenPair 批量签发 access + refresh,事务保证同成功 / 同失败。
func (s *authorizationService) mintTokenPair(ctx context.Context, clientID string, userID, agentID uint64, scope string, accessTTL, refreshTTL time.Duration) (*TokenPair, error) {
	if accessTTL <= 0 {
		accessTTL = oauth.DefaultAccessTokenTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = oauth.DefaultRefreshTokenTTL
	}
	accessPlain, err := randomBase64URL(oauth.TokenRandomBytes)
	if err != nil {
		return nil, fmt.Errorf("gen access_token: %w: %w", err, oauth.ErrOAuthInternal)
	}
	accessPlain = oauth.AccessTokenPrefix + accessPlain
	refreshPlain, err := randomBase64URL(oauth.TokenRandomBytes)
	if err != nil {
		return nil, fmt.Errorf("gen refresh_token: %w: %w", err, oauth.ErrOAuthInternal)
	}
	refreshPlain = oauth.RefreshTokenPrefix + refreshPlain

	now := time.Now().UTC()
	var atRow model.OAuthAccessToken
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		at := &model.OAuthAccessToken{
			TokenHash: sha256Hex(accessPlain),
			ClientID:  clientID,
			UserID:    userID,
			AgentID:   agentID,
			Scope:     scope,
			ExpiresAt: now.Add(accessTTL),
		}
		if err := tx.CreateAccessToken(ctx, at); err != nil {
			return err
		}
		atRow = *at

		rt := &model.OAuthRefreshToken{
			TokenHash:     sha256Hex(refreshPlain),
			AccessTokenID: at.ID,
			ClientID:      clientID,
			UserID:        userID,
			AgentID:       agentID,
			Scope:         scope,
			ExpiresAt:     now.Add(refreshTTL),
		}
		return tx.CreateRefreshToken(ctx, rt)
	})
	if err != nil {
		return nil, fmt.Errorf("mint tokens tx: %w: %w", err, oauth.ErrOAuthInternal)
	}
	return &TokenPair{
		AccessToken:  accessPlain,
		RefreshToken: refreshPlain,
		ExpiresIn:    int(atRow.ExpiresAt.Sub(now).Seconds()),
		Scope:        scope,
		TokenType:    oauth.TokenTypeBearer,
	}, nil
}

// verifyPKCE RFC 7636 S256:BASE64URL(SHA256(code_verifier)) == code_challenge
func verifyPKCE(challenge, verifier string) error {
	if verifier == "" {
		return oauth.ErrPKCEVerifierMismatch
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	if got != challenge {
		return oauth.ErrPKCEVerifierMismatch
	}
	return nil
}

// 备用的错误导出,供测试检查;在 service 包内不直接用。
var _ = errors.Is
