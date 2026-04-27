package service

import (
	"context"
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

// PATService 管理 user Personal Access Token。
//
// 用途:给 Cursor / Codex / curl 等不走 OAuth flow 的客户端使用。语义和 OAuth
// access_token 一致(bearer → user + agent),但不走浏览器授权。
//
// 发 PAT 时**自动建一个个人 agent**(和 OAuth consent 同套路,复用 AgentBootstrapper),
// 绑定到该 PAT。吊销 PAT 不会吊销 agent —— agent 可以继续用 OAuth 重新发 token。
type PATService interface {
	// Create 建 PAT 并返明文 token(只此一次)。
	// orgIDResolver 由调用方或 main.go 实现(和 OAuth agent 自动建共用)。
	Create(ctx context.Context, in CreatePATInput) (*CreatePATResult, error)

	// ListByUser 列当前 user 的所有 PAT(含 revoked,前端按状态过滤展示)。
	ListByUser(ctx context.Context, userID uint64) ([]model.UserPAT, error)

	// Revoke 软吊销(revoked_at 非空),中间件按此过滤。
	Revoke(ctx context.Context, patID, actorUserID uint64) error
}

// CreatePATInput 建 PAT 参数。
type CreatePATInput struct {
	UserID       uint64
	OrgID        uint64 // 调用方查该 user 的一个 org(AgentBootstrapper 也要)
	Label        string
	ExpiresAt    *time.Time // nil = 不过期
}

// CreatePATResult 返给 handler 的结果(Token 明文只此一次)。
type CreatePATResult struct {
	PAT   *model.UserPAT
	Token string // 明文,只此一次返回
}

type patService struct {
	repo              repository.Repository
	agentBootstrapper AgentBootstrapper
	log               logger.LoggerInterface
}

func newPATService(repo repository.Repository, agentBootstrapper AgentBootstrapper, log logger.LoggerInterface) PATService {
	return &patService{repo: repo, agentBootstrapper: agentBootstrapper, log: log}
}

func (s *patService) Create(ctx context.Context, in CreatePATInput) (*CreatePATResult, error) {
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > oauth.PATLabelMaxLen {
		return nil, oauth.ErrPATLabelInvalid
	}
	if in.UserID == 0 {
		return nil, oauth.ErrOAuthInternal
	}
	if s.agentBootstrapper == nil {
		return nil, fmt.Errorf("agent bootstrapper not configured: %w", oauth.ErrOAuthInternal)
	}

	// 1. 建 agent(和 OAuth consent 共用路径)
	_, agentPrincipalID, err := s.agentBootstrapper.CreateUserAgent(ctx, in.UserID, label)
	if err != nil {
		return nil, fmt.Errorf("bootstrap agent: %w: %w", err, oauth.ErrOAuthInternal)
	}

	// 2. 生成 PAT 明文 + hash
	plain, err := randomBase64URL(oauth.TokenRandomBytes)
	if err != nil {
		return nil, fmt.Errorf("gen pat token: %w: %w", err, oauth.ErrOAuthInternal)
	}
	plain = "syn_pat_" + plain
	row := &model.UserPAT{
		TokenHash: sha256Hex(plain),
		UserID:    in.UserID,
		AgentID:   agentPrincipalID,
		Label:     label,
		ExpiresAt: in.ExpiresAt,
	}
	if err := s.repo.CreatePAT(ctx, row); err != nil {
		return nil, fmt.Errorf("insert pat: %w: %w", err, oauth.ErrOAuthInternal)
	}
	return &CreatePATResult{PAT: row, Token: plain}, nil
}

func (s *patService) ListByUser(ctx context.Context, userID uint64) ([]model.UserPAT, error) {
	return s.repo.ListPATsByUser(ctx, userID)
}

func (s *patService) Revoke(ctx context.Context, patID, actorUserID uint64) error {
	p, err := s.repo.FindPATByID(ctx, patID)
	if err != nil {
		return fmt.Errorf("find pat: %w: %w", err, oauth.ErrOAuthInternal)
	}
	if p == nil || p.UserID != actorUserID {
		return oauth.ErrPATNotFound
	}
	if err := s.repo.RevokePAT(ctx, patID, time.Now().UTC()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return oauth.ErrPATNotFound
		}
		return fmt.Errorf("revoke pat: %w: %w", err, oauth.ErrOAuthInternal)
	}
	return nil
}
