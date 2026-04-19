// service.go oauth 业务逻辑实现。纯同步方法,不起 goroutine,不管 HTTP。
//
// 依赖:Repository(DB)+ jwtSigner(密码学)+ logger。构造后并发安全,内部无可变共享状态。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Config 构造参数。所有字段都必须显式传,没有默认值 —— 配置错了宁可启动 fail。
type Config struct {
	Issuer     string // 写入 JWT iss,和 /.well-known 里宣告的必须一致,如 "https://synapse.example.com"
	SigningKey []byte // HS256 密钥,≥ 32 字节;来自 config.oauth.signing_key
}

// New 构造。repo / log 非 nil,cfg 字段见 Config。
func New(cfg Config, repo repository.Repository, log logger.LoggerInterface) (Service, error) {
	if repo == nil || log == nil {
		return nil, errors.New("oauth service: repo and log required")
	}
	signer, err := newJWTSigner(cfg.SigningKey, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return &service{
		repo:   repo,
		signer: signer,
		log:    log,
		ctx:    context.Background(), // 基线;方法内部用自己的 context
	}, nil
}

type service struct {
	repo   repository.Repository
	signer *jwtSigner
	log    logger.LoggerInterface
	ctx    context.Context
}

// ─── DCR:RegisterClient ────────────────────────────────────────────────────

// RegisterClient 实现 RFC 7591 子集。强制 public client + PKCE-only。
func (s *service) RegisterClient(req ClientRegistrationReq) (*ClientRegistrationResp, error) {
	// 默认值填充 —— RFC 7591 §2 规定服务端可以给省略字段填默认。
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{"code"}
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = "none"
	}

	// ── 校验 ──
	if len(req.RedirectURIs) == 0 {
		return nil, fmt.Errorf("%w: redirect_uris required", ErrInvalidClientMetadata)
	}
	for _, uri := range req.RedirectURIs {
		if !isAllowedRedirectURI(uri) {
			return nil, fmt.Errorf("%w: redirect_uri not allowed: %s", ErrInvalidRedirectURI, uri)
		}
	}
	if req.TokenEndpointAuthMethod != "none" {
		// 只支持 public client + PKCE。confidential client 会引入 client_secret 管理复杂度,
		// 而我们目标客户端(Claude Desktop / Cursor 等)全是 public。
		return nil, fmt.Errorf("%w: only token_endpoint_auth_method=none supported", ErrInvalidClientMetadata)
	}
	if !slices.Contains(req.GrantTypes, "authorization_code") {
		return nil, fmt.Errorf("%w: authorization_code grant_type required", ErrInvalidClientMetadata)
	}
	for _, gt := range req.GrantTypes {
		if gt != "authorization_code" && gt != "refresh_token" {
			return nil, fmt.Errorf("%w: unsupported grant_type: %s", ErrInvalidClientMetadata, gt)
		}
	}
	if len(req.ResponseTypes) != 1 || req.ResponseTypes[0] != "code" {
		return nil, fmt.Errorf("%w: only response_type=code supported", ErrInvalidClientMetadata)
	}

	// ── 生成 client_id ──
	// 16 字节 = 22 chars base64url,opaque 随机,无法从中反推任何内部状态。
	cid, err := randomURLSafe(16)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	redirJSON, _ := json.Marshal(req.RedirectURIs)
	grantJSON, _ := json.Marshal(req.GrantTypes)
	respJSON, _ := json.Marshal(req.ResponseTypes)
	metaJSON := req.Metadata
	if len(metaJSON) == 0 {
		metaJSON = []byte("{}")
	}

	c := &model.OAuthClient{
		ClientID:                cid,
		ClientName:              req.ClientName,
		RedirectURIs:            datatypes.JSON(redirJSON),
		GrantTypes:              datatypes.JSON(grantJSON),
		ResponseTypes:           datatypes.JSON(respJSON),
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		Scope:                   strings.TrimSpace(req.Scope),
		Status:                  oauth.ClientStatusActive,
		CreatedByUserID:         req.CreatedByUserID,
		Metadata:                datatypes.JSON(metaJSON),
	}
	if err := s.repo.Clients().Create(s.ctx, c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	return &ClientRegistrationResp{
		ClientID:                cid,
		ClientName:              req.ClientName,
		RedirectURIs:            req.RedirectURIs,
		GrantTypes:              req.GrantTypes,
		ResponseTypes:           req.ResponseTypes,
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		Scope:                   c.Scope,
		CreatedAt:               c.CreatedAt.Unix(),
	}, nil
}

// ─── /authorize 前置校验 ────────────────────────────────────────────────────

// ValidateAuthRequest 供 handler 在展示 consent UI 前调,参数错直接返错误页,
// 避免让用户先登录再看到"参数不对"。
func (s *service) ValidateAuthRequest(req AuthorizeRequest) (*ClientInfo, error) {
	if req.ClientID == "" || req.RedirectURI == "" {
		return nil, fmt.Errorf("%w: client_id and redirect_uri required", ErrInvalidRequest)
	}
	if req.ResponseType != "code" {
		return nil, fmt.Errorf("%w: response_type must be \"code\"", ErrUnsupportedResponseType)
	}
	if req.CodeChallenge == "" {
		return nil, fmt.Errorf("%w: code_challenge required (PKCE mandatory)", ErrInvalidRequest)
	}
	if req.CodeChallengeMethod != oauth.PKCEMethodS256 {
		return nil, fmt.Errorf("%w: code_challenge_method must be S256", ErrInvalidRequest)
	}
	if len(req.CodeChallenge) < 43 || len(req.CodeChallenge) > 128 {
		return nil, fmt.Errorf("%w: code_challenge length invalid", ErrInvalidRequest)
	}

	info, err := s.lookupClient(req.ClientID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.RedirectURIs, req.RedirectURI) {
		return nil, fmt.Errorf("%w: redirect_uri does not match registered", ErrInvalidRedirectURI)
	}
	if req.Scope != "" && !scopeSubset(req.Scope, info.Scope) {
		return nil, fmt.Errorf("%w: requested scope not allowed for client", ErrInvalidScope)
	}
	return info, nil
}

// ─── /authorize consent 后发码 ─────────────────────────────────────────────

func (s *service) IssueAuthCode(req IssueAuthCodeReq) (string, error) {
	if req.UserID == 0 || req.OrgID == 0 {
		return "", fmt.Errorf("%w: user and org required", ErrInvalidRequest)
	}
	if req.CodeChallenge == "" || req.CodeChallengeMethod != oauth.PKCEMethodS256 {
		return "", fmt.Errorf("%w: PKCE S256 required", ErrInvalidRequest)
	}

	// 32 字节 = 43 chars base64url,remaining entropy 256 bits。
	code, err := randomURLSafe(32)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrServerError, err)
	}

	record := &model.OAuthAuthorizationCode{
		CodeHash:            hashToken(code),
		ClientID:            req.ClientID,
		UserID:              req.UserID,
		OrgID:               req.OrgID,
		Scope:               req.Scope,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		ExpiresAt:           time.Now().Add(oauth.AuthCodeTTL),
	}
	if err := s.repo.AuthCodes().Create(s.ctx, record); err != nil {
		return "", fmt.Errorf("%w: %v", ErrServerError, err)
	}
	return code, nil
}

// ─── /token authorization_code grant ───────────────────────────────────────

func (s *service) ExchangeAuthCode(req ExchangeAuthCodeReq) (*TokenResponse, error) {
	if req.Code == "" || req.ClientID == "" || req.RedirectURI == "" || req.CodeVerifier == "" {
		return nil, fmt.Errorf("%w: code, client_id, redirect_uri, code_verifier required", ErrInvalidRequest)
	}

	code, err := s.repo.AuthCodes().ExchangeOnce(s.ctx, hashToken(req.Code))
	if err != nil {
		if errors.Is(err, repository.ErrAuthCodeInvalid) {
			return nil, fmt.Errorf("%w: code invalid or expired", ErrInvalidGrant)
		}
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	// ── 绑定校验 ──
	if code.ClientID != req.ClientID {
		return nil, fmt.Errorf("%w: code does not belong to this client", ErrInvalidGrant)
	}
	if code.RedirectURI != req.RedirectURI {
		return nil, fmt.Errorf("%w: redirect_uri mismatch", ErrInvalidGrant)
	}

	// ── PKCE 校验 ──
	// code_challenge_method 进 DB 时已校验过是 S256,这里信任。
	if !verifyPKCES256(req.CodeVerifier, code.CodeChallenge) {
		return nil, fmt.Errorf("%w: PKCE verification failed", ErrInvalidGrant)
	}

	return s.issueTokens(code.UserID, code.OrgID, code.ClientID, code.Scope)
}

// ─── /token refresh_token grant ────────────────────────────────────────────

func (s *service) RefreshAccessToken(req RefreshReq) (*TokenResponse, error) {
	if req.RefreshToken == "" || req.ClientID == "" {
		return nil, fmt.Errorf("%w: refresh_token and client_id required", ErrInvalidRequest)
	}

	oldHash := hashToken(req.RefreshToken)

	// 先读一遍,用于 scope 继承 + client_id 绑定校验;真正的轮换在 Rotate 里走 tx。
	old, err := s.repo.RefreshTokens().GetByHash(s.ctx, oldHash)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	if old == nil {
		return nil, fmt.Errorf("%w: refresh_token not found", ErrInvalidGrant)
	}
	if old.ClientID != req.ClientID {
		return nil, fmt.Errorf("%w: refresh_token client mismatch", ErrInvalidGrant)
	}

	// 请求 scope 必须是原 scope 子集(RFC 6749 §6)。空请求 = 继承原 scope。
	newScope := old.Scope
	if req.Scope != "" {
		if !scopeSubset(req.Scope, old.Scope) {
			return nil, fmt.Errorf("%w: requested scope exceeds original", ErrInvalidScope)
		}
		newScope = req.Scope
	}

	// 构造新 refresh token,交给 Rotate 原子处理
	newToken, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	now := time.Now()
	newRec := &model.OAuthRefreshToken{
		TokenHash: hashToken(newToken),
		ClientID:  old.ClientID,
		UserID:    old.UserID,
		OrgID:     old.OrgID,
		Scope:     newScope,
		ExpiresAt: now.Add(oauth.RefreshTokenTTL),
	}

	if _, err := s.repo.RefreshTokens().Rotate(s.ctx, oldHash, newRec); err != nil {
		if errors.Is(err, repository.ErrRefreshTokenReused) {
			// 泄露信号:整链 revoke + 告警。client 收到 invalid_grant,被迫重新 consent。
			if _, rErr := s.repo.RefreshTokens().RevokeChain(s.ctx, oldHash); rErr != nil {
				s.log.ErrorCtx(s.ctx, "oauth: revoke chain failed", rErr, map[string]any{"client_id": req.ClientID})
			}
			s.log.WarnCtx(s.ctx, "oauth: refresh token reuse detected — chain revoked", map[string]any{
				"client_id": req.ClientID, "user_id": old.UserID,
			})
			return nil, fmt.Errorf("%w: refresh token reuse detected", ErrInvalidGrant)
		}
		if errors.Is(err, repository.ErrRefreshTokenInvalid) {
			return nil, fmt.Errorf("%w: refresh token invalid", ErrInvalidGrant)
		}
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	// 签新 access token。refresh 不重算,直接用刚刚生成的 newToken。
	jti, err := randomURLSafe(16)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	accessToken, exp, err := s.signer.sign(old.UserID, old.OrgID, old.ClientID, newScope, jti, oauth.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	return &TokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(time.Until(exp).Seconds()),
		RefreshToken: newToken,
		Scope:        newScope,
	}, nil
}

// ─── /revoke ───────────────────────────────────────────────────────────────

// Revoke 实现 RFC 7009。当前只支持 refresh_token,access_token 由于无状态 JWT 暂时 no-op。
// 调用方(handler)收到后始终返 200,不暴露 token 是否存在(防枚举)。
func (s *service) Revoke(token, tokenTypeHint string) error {
	if token == "" {
		return nil // 空 token = 语义上"什么都没撤销",也按 200 处理
	}
	if tokenTypeHint == "access_token" {
		// 未实现 access token blocklist — 见 const.go 注释,将来加 Redis blocklist 时改这里。
		s.log.InfoCtx(s.ctx, "oauth: access_token revoke requested (no-op until blocklist added)", nil)
		return nil
	}

	// 其他情况都按 refresh token 处理(hint 为空或 "refresh_token")。
	hash := hashToken(token)
	if _, err := s.repo.RefreshTokens().RevokeChain(s.ctx, hash); err != nil {
		return fmt.Errorf("%w: %v", ErrServerError, err)
	}
	return nil
}

// ─── ValidateAccessToken(middleware 调用) ─────────────────────────────────

func (s *service) ValidateAccessToken(token string) (*AccessTokenClaims, error) {
	if token == "" {
		return nil, errTokenInvalid
	}
	return s.signer.verify(token)
}

// ─── 内部 helpers ──────────────────────────────────────────────────────────

// lookupClient 按 client_id 取并转 ClientInfo。not-found / suspended 都返 ErrInvalidClient。
func (s *service) lookupClient(clientID string) (*ClientInfo, error) {
	c, err := s.repo.Clients().GetByClientID(s.ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	if c == nil {
		return nil, fmt.Errorf("%w: client not found or suspended", ErrInvalidClient)
	}
	var uris []string
	if err := json.Unmarshal(c.RedirectURIs, &uris); err != nil {
		return nil, fmt.Errorf("%w: corrupt client redirect_uris: %v", ErrServerError, err)
	}
	return &ClientInfo{
		ClientID:     c.ClientID,
		ClientName:   c.ClientName,
		RedirectURIs: uris,
		Scope:        c.Scope,
	}, nil
}

// issueTokens 签首发 token pair(authorization_code grant 路径)。
// access token JWT + refresh token opaque(存 DB hash)。
func (s *service) issueTokens(userID, orgID uint64, clientID, scope string) (*TokenResponse, error) {
	jti, err := randomURLSafe(16)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	accessToken, exp, err := s.signer.sign(userID, orgID, clientID, scope, jti, oauth.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	refreshToken, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}
	refreshRec := &model.OAuthRefreshToken{
		TokenHash: hashToken(refreshToken),
		ClientID:  clientID,
		UserID:    userID,
		OrgID:     orgID,
		Scope:     scope,
		ExpiresAt: time.Now().Add(oauth.RefreshTokenTTL),
	}
	if err := s.repo.RefreshTokens().Create(s.ctx, refreshRec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerError, err)
	}

	return &TokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(time.Until(exp).Seconds()),
		RefreshToken: refreshToken,
		Scope:        scope,
	}, nil
}

// isAllowedRedirectURI 硬白名单前缀。通过 = 允许注册,不通过 = 拒。
// 空 string / 非法 scheme / 非 localhost 的 http 一律拒。
func isAllowedRedirectURI(uri string) bool {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return false
	}
	for _, prefix := range oauth.AllowedRedirectURIPrefixes {
		if strings.HasPrefix(uri, prefix) {
			return true
		}
	}
	return false
}

// scopeSubset requested 的每个 scope 都必须出现在 allowed 里。
// allowed 空字符串 = "允许所有请求的 scope"(client 没设白名单的语义)。
func scopeSubset(requested, allowed string) bool {
	if strings.TrimSpace(allowed) == "" {
		return true
	}
	allowedSet := make(map[string]struct{})
	for s := range strings.FieldsSeq(allowed) {
		allowedSet[s] = struct{}{}
	}
	for s := range strings.FieldsSeq(requested) {
		if _, ok := allowedSet[s]; !ok {
			return false
		}
	}
	return true
}
