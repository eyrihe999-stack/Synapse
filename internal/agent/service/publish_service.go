// publish_service.go Agent 发布到 org 的 service,以及跨模块 hook 的实现。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/agent/dto"
	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"github.com/eyrihe999-stack/Synapse/internal/agent/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// PublishService 管理 agent 在 org 内的发布生命周期。
type PublishService interface {
	Submit(ctx context.Context, userID uint64, org *OrgInfo, requireReview bool, req dto.PublishAgentRequest) (*dto.PublishResponse, error)
	ListByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]dto.PublishResponse, int64, error)
	Approve(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error)
	Reject(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error)
	RevokeByAuthor(ctx context.Context, publishID, userID uint64) error
	RevokeByAdmin(ctx context.Context, publishID, adminUserID uint64) error
	GetActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error)
	// 跨模块 hook
	RevokeByMember(ctx context.Context, orgID, userID uint64, reason string) error
	RevokeByOrg(ctx context.Context, orgID uint64) error
}

type publishService struct {
	repo    repository.Repository
	orgPort OrgPort
	logger  logger.LoggerInterface
}

// NewPublishService 创建 PublishService 实例，注入仓库、OrgPort 和日志依赖。
func NewPublishService(repo repository.Repository, orgPort OrgPort, log logger.LoggerInterface) PublishService {
	return &publishService{repo: repo, orgPort: orgPort, logger: log}
}

// Submit 提交 agent 发布请求到指定 org；若 org 开启审核则进入 pending 状态。
// 可能返回 ErrAgentInvalidRequest、ErrAgentNotFound、ErrAgentNotAuthor、ErrPublishAlreadyExists、ErrAgentInternal。
func (s *publishService) Submit(ctx context.Context, userID uint64, org *OrgInfo, requireReview bool, req dto.PublishAgentRequest) (*dto.PublishResponse, error) {
	if org == nil {
		s.logger.WarnCtx(ctx, "submit publish with nil org", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("org nil: %w", agent.ErrAgentInvalidRequest)
	}
	a, err := s.repo.FindAgentByID(ctx, req.AgentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "agent not found for publish", map[string]any{"agent_id": req.AgentID})
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent failed", err, map[string]any{"agent_id": req.AgentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != userID {
		s.logger.WarnCtx(ctx, "publish submit denied, not author", map[string]any{"agent_id": req.AgentID, "user_id": userID})
		return nil, fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	if a.Status != model.AgentStatusActive {
		s.logger.WarnCtx(ctx, "publish submit denied, agent not active", map[string]any{"agent_id": req.AgentID, "status": a.Status})
		return nil, fmt.Errorf("agent not active: %w", agent.ErrAgentInvalidRequest)
	}
	if existing, fErr := s.repo.FindActivePublish(ctx, a.ID, org.ID); fErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "publish already exists", map[string]any{"agent_id": a.ID, "org_id": org.ID})
		return nil, fmt.Errorf("publish exists: %w", agent.ErrPublishAlreadyExists)
	} else if fErr != nil && !errors.Is(fErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "find active publish failed", fErr, nil)
		return nil, fmt.Errorf("find active publish: %w: %w", fErr, agent.ErrAgentInternal)
	}
	status := model.PublishStatusApproved
	if requireReview {
		status = model.PublishStatusPending
	}
	p := &model.AgentPublish{
		AgentID:           a.ID,
		OrgID:             org.ID,
		SubmittedByUserID: userID,
		Status:            status,
		ReviewNote:        req.Note,
	}
	if err := s.repo.CreatePublish(ctx, p); err != nil {
		s.logger.ErrorCtx(ctx, "create publish failed", err, nil)
		return nil, fmt.Errorf("create publish: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publish submitted", map[string]any{"publish_id": p.ID, "status": status})
	resp := publishToDTO(p)
	return &resp, nil
}

// ListByOrg 分页列出指定 org 下的发布记录，可按 status 过滤。
// 可能返回 ErrAgentInternal。
func (s *publishService) ListByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]dto.PublishResponse, int64, error) {
	list, total, err := s.repo.ListPublishesByOrg(ctx, orgID, status, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list publishes failed", err, nil)
		return nil, 0, fmt.Errorf("list publishes: %w: %w", err, agent.ErrAgentInternal)
	}
	// 收集需要解析的 user IDs（去重）
	uidSet := map[uint64]struct{}{}
	for _, p := range list {
		uidSet[p.SubmittedByUserID] = struct{}{}
		if p.ReviewedByUserID != nil {
			uidSet[*p.ReviewedByUserID] = struct{}{}
		}
	}
	// 批量解析用户显示名
	nameMap := make(map[uint64]string, len(uidSet))
	if s.orgPort != nil {
		for uid := range uidSet {
			if name := s.orgPort.GetUserDisplayName(ctx, uid); name != "" {
				nameMap[uid] = name
			}
		}
	}
	out := make([]dto.PublishResponse, 0, len(list))
	for _, p := range list {
		r := publishToDTO(p)
		r.SubmittedByDisplayName = nameMap[p.SubmittedByUserID]
		if p.ReviewedByUserID != nil {
			r.ReviewedByDisplayName = nameMap[*p.ReviewedByUserID]
		}
		out = append(out, r)
	}
	return out, total, nil
}

// Approve 审批通过指定发布请求，将状态从 pending 更新为 approved。
// 可能返回 ErrPublishNotFound、ErrPublishNotPending、ErrAgentInternal。
//
func (s *publishService) Approve(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error) {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.reviewTransition(ctx, publishID, reviewerUserID, note, model.PublishStatusApproved)
}

// Reject 审批拒绝指定发布请求，将状态从 pending 更新为 rejected。
// 可能返回 ErrPublishNotFound、ErrPublishNotPending、ErrAgentInternal。
//
func (s *publishService) Reject(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error) {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.reviewTransition(ctx, publishID, reviewerUserID, note, model.PublishStatusRejected)
}

// reviewTransition 执行审批状态迁移（approved / rejected），内部共用逻辑。
// 可能返回 ErrPublishNotFound、ErrPublishNotPending、ErrAgentInternal。
func (s *publishService) reviewTransition(ctx context.Context, publishID, reviewerUserID uint64, note, target string) (*dto.PublishResponse, error) {
	p, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "publish not found for review", map[string]any{"publish_id": publishID})
			return nil, fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish for review failed", err, map[string]any{"publish_id": publishID})
		return nil, fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.Status != model.PublishStatusPending {
		s.logger.WarnCtx(ctx, "publish not in pending status", map[string]any{"publish_id": publishID, "status": p.Status})
		return nil, fmt.Errorf("publish not pending: %w", agent.ErrPublishNotPending)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":              target,
		"reviewed_by_user_id": reviewerUserID,
		"reviewed_at":         &now,
		"review_note":         note,
	}
	if err := s.repo.UpdatePublishFields(ctx, publishID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "update publish status failed", err, map[string]any{"publish_id": publishID, "target": target})
		return nil, fmt.Errorf("update publish: %w: %w", err, agent.ErrAgentInternal)
	}
	reloaded, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "reload publish after review failed", err, map[string]any{"publish_id": publishID})
		return nil, fmt.Errorf("reload publish: %w: %w", err, agent.ErrAgentInternal)
	}
	resp := publishToDTO(reloaded)
	return &resp, nil
}

// RevokeByAuthor 由 agent 作者主动撤回发布。
// 可能返回 ErrPublishNotFound、ErrAgentNotAuthor、ErrAgentInternal。
//
func (s *publishService) RevokeByAuthor(ctx context.Context, publishID, userID uint64) error {
	p, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "publish not found for revoke by author", map[string]any{"publish_id": publishID})
			return fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish for revoke by author failed", err, map[string]any{"publish_id": publishID})
		return fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.SubmittedByUserID != userID {
		s.logger.WarnCtx(ctx, "revoke by author denied, not author", map[string]any{"publish_id": publishID, "user_id": userID})
		return fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	//sayso-lint:ignore sentinel-wrap
	return s.markRevoked(ctx, p.ID, agent.RevokedReasonAuthorUnpublished)
}

// RevokeByAdmin 由管理员强制撤回发布。
// 可能返回 ErrPublishNotFound、ErrAgentInternal。
//
func (s *publishService) RevokeByAdmin(ctx context.Context, publishID, adminUserID uint64) error {
	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindPublishByID(ctx, publishID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "publish not found for admin revoke", map[string]any{"publish_id": publishID})
			return fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish for admin revoke failed", err, map[string]any{"publish_id": publishID})
		return fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publish banned by admin", map[string]any{"publish_id": publishID, "admin_user_id": adminUserID})
	//sayso-lint:ignore sentinel-wrap
	return s.markRevoked(ctx, publishID, agent.RevokedReasonAdminBanned)
}

// markRevoked 将发布标记为已撤回，记录原因和时间。
// 可能返回 ErrAgentInternal。
func (s *publishService) markRevoked(ctx context.Context, publishID uint64, reason string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":         model.PublishStatusRevoked,
		"revoked_at":     &now,
		"revoked_reason": reason,
	}
	if err := s.repo.UpdatePublishFields(ctx, publishID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "mark publish revoked failed", err, nil)
		return fmt.Errorf("mark revoked: %w: %w", err, agent.ErrAgentInternal)
	}
	return nil
}

// GetActivePublish 获取 agent 在指定 org 内的有效发布记录（状态须为 approved）。
// 可能返回 ErrAgentNotPublishedInOrg、ErrAgentInternal。
func (s *publishService) GetActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error) {
	p, err := s.repo.FindActivePublish(ctx, agentID, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "no active publish in org", map[string]any{"agent_id": agentID, "org_id": orgID})
			return nil, fmt.Errorf("publish not in org: %w", agent.ErrAgentNotPublishedInOrg)
		}
		s.logger.ErrorCtx(ctx, "find active publish failed", err, map[string]any{"agent_id": agentID, "org_id": orgID})
		return nil, fmt.Errorf("find active publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.Status != model.PublishStatusApproved {
		s.logger.WarnCtx(ctx, "publish exists but not approved", map[string]any{"agent_id": agentID, "org_id": orgID, "status": p.Status})
		return nil, fmt.Errorf("publish not approved: %w", agent.ErrAgentNotPublishedInOrg)
	}
	return p, nil
}

// ─── 跨模块 Hook ────────────────────────────────────────────────────────────

// RevokeByMember 成员离开或被移除 org 时，撤回该成员在该 org 下的所有发布。
// 可能返回 ErrAgentInternal。
func (s *publishService) RevokeByMember(ctx context.Context, orgID, userID uint64, reason string) error {
	revokeReason := agent.RevokedReasonMemberLeft
	if reason != "" && reason != "leave" {
		revokeReason = agent.RevokedReasonAuthorRemoved
	}
	now := time.Now().UTC()
	affected, err := s.repo.RevokePublishesByAuthorOrg(ctx, orgID, userID, revokeReason, now)
	if err != nil {
		s.logger.ErrorCtx(ctx, "revoke publishes by member failed", err, nil)
		return fmt.Errorf("revoke by member: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publishes revoked by member leave", map[string]any{"org_id": orgID, "user_id": userID, "affected": affected})
	return nil
}

// RevokeByOrg org 解散时，撤回该 org 下所有发布。
// 可能返回 ErrAgentInternal。
func (s *publishService) RevokeByOrg(ctx context.Context, orgID uint64) error {
	now := time.Now().UTC()
	affected, err := s.repo.RevokePublishesByOrg(ctx, orgID, agent.RevokedReasonOrgDissolved, now)
	if err != nil {
		s.logger.ErrorCtx(ctx, "revoke publishes by org failed", err, nil)
		return fmt.Errorf("revoke by org: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publishes revoked by org dissolve", map[string]any{"org_id": orgID, "affected": affected})
	return nil
}
