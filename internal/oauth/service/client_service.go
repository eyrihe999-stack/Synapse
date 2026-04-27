// Package service OAuth AS 业务层。
//
// 拆分:
//   - client_service.go          OAuth client 管理(manual / DCR 共用)
//   - authorization_service.go   authorize + token 端点业务(PR#5'-5/-6)
//   - pat_service.go             Personal Access Token 管理(PR#5'-9)
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
)

// ClientService OAuth client 管理(manual + DCR 共用核心逻辑)。
//
// 产出:client_id(明文)+ client_secret(明文,**仅创建时返回一次**)。
// DB 里只存 client_secret 的 sha256 hash,后续鉴权按 hash 匹配。
type ClientService interface {
	// Create 新建 client。actorUserID=0 表示 DCR 匿名注册;>0 表示用户后台建。
	// 返 (明文 client_id, 明文 client_secret, *OAuthClient record, err)。
	Create(ctx context.Context, in CreateClientInput) (*ClientCredentials, error)

	// ListByUser 列某 user 建的所有 client(手动 + 绑定该 user 的 DCR)。
	ListByUser(ctx context.Context, userID uint64) ([]model.OAuthClient, error)

	// Disable 禁用 client,同时吊销该 client 的所有 access/refresh tokens。
	// actorUserID>0 时只能禁用自己建的 client(DCR 客户端 actorUserID=0 的不允许 disable
	// 除非专门的 admin 路径 —— MVP 不做 admin 超级禁用)。
	Disable(ctx context.Context, id, actorUserID uint64) error

	// FindActiveByClientID 按 client_id 查;disabled / 不存在 → (nil, nil)。
	FindActiveByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error)

	// VerifyClientSecret 验 client_secret 明文是否匹配存储 hash。
	VerifyClientSecret(c *model.OAuthClient, plainSecret string) bool
}

// CreateClientInput 建 client 参数。
type CreateClientInput struct {
	ActorUserID     uint64   // 0 = DCR,>0 = 用户后台手动建
	ClientName      string
	RedirectURIs    []string
	GrantTypes      []string // 默认 [authorization_code, refresh_token]
	TokenAuthMethod string   // 默认 client_secret_post
	RegisteredVia   string   // "manual" / "dcr"
}

// ClientCredentials 明文凭证(仅创建时返给调用方一次)。
type ClientCredentials struct {
	Client              *model.OAuthClient
	ClientIDPlain       string // 也存在 c.ClientID,冗余方便调用方直接使用
	ClientSecretPlain   string // DB 里是 hash,明文只在此返回一次
}

type clientService struct {
	repo repository.Repository
	log  logger.LoggerInterface
}

func newClientService(repo repository.Repository, log logger.LoggerInterface) ClientService {
	return &clientService{repo: repo, log: log}
}

// Create 生成凭证并落库。
func (s *clientService) Create(ctx context.Context, in CreateClientInput) (*ClientCredentials, error) {
	name := strings.TrimSpace(in.ClientName)
	if name == "" || len(name) > oauth.ClientNameMaxLen {
		return nil, oauth.ErrClientNameInvalid
	}

	uris, err := validateRedirectURIs(in.RedirectURIs)
	if err != nil {
		return nil, err
	}

	grantTypes := in.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{oauth.GrantTypeAuthorizationCode, oauth.GrantTypeRefreshToken}
	}
	for _, gt := range grantTypes {
		if gt != oauth.GrantTypeAuthorizationCode && gt != oauth.GrantTypeRefreshToken {
			return nil, oauth.ErrGrantTypeInvalid
		}
	}

	authMethod := in.TokenAuthMethod
	if authMethod == "" {
		authMethod = oauth.TokenAuthClientSecretPost
	}
	if !validTokenAuthMethod(authMethod) {
		return nil, oauth.ErrTokenAuthMethodInvalid
	}

	registeredVia := in.RegisteredVia
	if registeredVia == "" {
		if in.ActorUserID == 0 {
			registeredVia = oauth.RegisteredViaDCR
		} else {
			registeredVia = oauth.RegisteredViaManual
		}
	}

	// 生成凭证
	idSuffix, err := randomHex(oauth.ClientIDRandomBytes)
	if err != nil {
		return nil, fmt.Errorf("gen client_id: %w: %w", err, oauth.ErrOAuthInternal)
	}
	clientID := oauth.ClientIDPrefix + idSuffix

	secretSuffix, err := randomBase64URL(oauth.ClientSecretBytes)
	if err != nil {
		return nil, fmt.Errorf("gen client_secret: %w: %w", err, oauth.ErrOAuthInternal)
	}
	plainSecret := oauth.ClientSecretPrefix + secretSuffix
	secretHash := sha256Hex(plainSecret)

	// redirect_uris / grant_types 存 JSON
	redirectURIsJSON, err := json.Marshal(uris)
	if err != nil {
		return nil, fmt.Errorf("marshal redirect_uris: %w: %w", err, oauth.ErrOAuthInternal)
	}
	grantTypesJSON, err := json.Marshal(grantTypes)
	if err != nil {
		return nil, fmt.Errorf("marshal grant_types: %w: %w", err, oauth.ErrOAuthInternal)
	}

	row := &model.OAuthClient{
		ClientID:           clientID,
		ClientSecretHash:   secretHash,
		ClientName:         name,
		RedirectURIsJSON:   string(redirectURIsJSON),
		GrantTypesJSON:     string(grantTypesJSON),
		TokenAuthMethod:    authMethod,
		RegisteredVia:      registeredVia,
		RegisteredByUserID: in.ActorUserID,
		Disabled:           false,
	}
	if err := s.repo.CreateClient(ctx, row); err != nil {
		return nil, fmt.Errorf("insert client: %w: %w", err, oauth.ErrOAuthInternal)
	}
	return &ClientCredentials{
		Client:            row,
		ClientIDPlain:     clientID,
		ClientSecretPlain: plainSecret,
	}, nil
}

func (s *clientService) ListByUser(ctx context.Context, userID uint64) ([]model.OAuthClient, error) {
	return s.repo.ListClientsByUser(ctx, userID)
}

func (s *clientService) Disable(ctx context.Context, id, actorUserID uint64) error {
	c, err := s.repo.FindClientByID(ctx, id)
	if err != nil {
		return fmt.Errorf("find client: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if c == nil {
		return oauth.ErrClientNotFound
	}
	// ownership 校验:manual client 只能创建者禁用。DCR 匿名 client(registered_by_user_id=0)
	// MVP 先不让普通 user 禁用 —— 未来加 admin 超级接口或 org-level 审计页再放开。
	if c.RegisteredByUserID != actorUserID {
		return oauth.ErrForbidden
	}

	// 软标记 + 吊销 token。用事务保证原子。
	now := time.Now().UTC()
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		if err := tx.UpdateClientDisabled(ctx, id, true); err != nil {
			return err
		}
		if _, err := tx.RevokeAccessTokensByClient(ctx, c.ClientID, now); err != nil {
			return err
		}
		if _, err := tx.RevokeRefreshTokensByClient(ctx, c.ClientID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("disable tx: %w: %w", err, oauth.ErrOAuthInternal)
	}
	s.log.InfoCtx(ctx, "oauth: client disabled", map[string]any{
		"client_id": c.ClientID, "actor_user_id": actorUserID,
	})
	return nil
}

func (s *clientService) FindActiveByClientID(ctx context.Context, clientID string) (*model.OAuthClient, error) {
	c, err := s.repo.FindClientByClientID(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if c == nil || c.Disabled {
		return nil, nil
	}
	return c, nil
}

func (s *clientService) VerifyClientSecret(c *model.OAuthClient, plainSecret string) bool {
	if c == nil || plainSecret == "" {
		return false
	}
	return c.ClientSecretHash == sha256Hex(plainSecret)
}

// ── helpers ─────────────────────────────────────────────────────────

func validateRedirectURIs(uris []string) ([]string, error) {
	if len(uris) == 0 {
		return nil, oauth.ErrRedirectURIsEmpty
	}
	if len(uris) > oauth.RedirectURIsMaxCount {
		return nil, oauth.ErrRedirectURIInvalid
	}
	seen := make(map[string]struct{}, len(uris))
	out := make([]string, 0, len(uris))
	for _, raw := range uris {
		u := strings.TrimSpace(raw)
		if u == "" || len(u) > oauth.RedirectURIMaxLen {
			return nil, oauth.ErrRedirectURIInvalid
		}
		if !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || isLoopbackHTTP(u)) {
			// 严格点:必须 http(s) scheme(Claude Desktop / Cursor 目前都是 https 或 localhost callback)
			return nil, oauth.ErrRedirectURIInvalid
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// isLoopbackHTTP 简单判断 http://localhost 或 http://127.0.0.1;本地开发允许 http。
func isLoopbackHTTP(u string) bool {
	return strings.HasPrefix(u, "http://localhost") || strings.HasPrefix(u, "http://127.0.0.1")
}

func validTokenAuthMethod(m string) bool {
	switch m {
	case oauth.TokenAuthClientSecretBasic, oauth.TokenAuthClientSecretPost, oauth.TokenAuthNone:
		return true
	}
	return false
}

// ErrorsTranslateNoRows 把 gorm.ErrRecordNotFound 映射到本模块哨兵错误。
// 若未来新增资源类型,补这里的 switch。
//
//nolint:unused // 备用 helper,当前 repository 层已经吞 NotFound 返 (nil, nil)
func ErrorsTranslateNoRows(err error, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sentinel
	}
	return err
}
