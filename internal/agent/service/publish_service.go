// publish_service.go Agent 发布到 org 的 service,以及跨模块 hook 的实现。
//
// 两种调用路径:
//   - 作者/管理员通过 HTTP 接口主动管理 publish(Submit / Approve / Reject / Revoke / Ban)
//   - organization hook 通过 RevokeByMember / RevokeByOrg 批量联动失效
//
// 数据库事务边界:
//   - Submit 走单表插入
//   - Review 只更新 publish 状态
//   - RevokeByMember / RevokeByOrg 使用批量 update,单条事务即可
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
//sayso-lint:ignore interface-pollution
type PublishService interface {
	// Submit 提交一次发布。若 org.require_agent_review=true,status=pending;否则 approved。
	Submit(ctx context.Context, userID uint64, org *OrgInfo, requireReview bool, req dto.PublishAgentRequest) (*dto.PublishResponse, error)
	// ListByOrg 分页列出 org 内的 publish。
	ListByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]dto.PublishResponse, int64, error)
	// Approve 审核通过一条 pending publish。
	Approve(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error)
	// Reject 审核拒绝一条 pending publish。
	Reject(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error)
	// RevokeByAuthor 作者或有 unpublish.self 权限的成员下架自己提交的 publish。
	RevokeByAuthor(ctx context.Context, publishID, userID uint64) error
	// RevokeByAdmin 管理员强制下架(reason=admin_banned)。
	RevokeByAdmin(ctx context.Context, publishID, adminUserID uint64) error
	// GetActivePublish 查 (agent, org) 的当前 active publish,gateway 调用时做兜底。
	GetActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error)

	// ─── 跨模块 hook ──────────────────────────────────────────────────────

	// RevokeByMember 组织成员被移除时调用,revoke 该成员在 org 里的所有 publish。
	// 注入到 organization.HookRegistry.RegisterMemberRemoved。
	RevokeByMember(ctx context.Context, orgID, userID uint64, reason string) error
	// RevokeByOrg org 解散时调用,revoke org 下所有 publish。
	RevokeByOrg(ctx context.Context, orgID uint64) error
}

type publishService struct {
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewPublishService 构造 PublishService。
func NewPublishService(repo repository.Repository, log logger.LoggerInterface) PublishService {
	return &publishService{repo: repo, logger: log}
}

// Submit 提交一次 agent 到 org 的发布申请,根据 requireReview 直接置为 approved 或 pending。
// 调用前已校验 agent.publish 权限,这里再校验作者身份与 agent 状态,并保证 (agent, org) 不存在 active 记录。
//
// 错误:org 为空或 agent 非 active 返回 ErrAgentInvalidRequest;非作者返回 ErrAgentNotAuthor;
// agent 不存在返回 ErrAgentNotFound;已存在 active 返回 ErrPublishAlreadyExists;其他存储错误返回 ErrAgentInternal。
func (s *publishService) Submit(ctx context.Context, userID uint64, org *OrgInfo, requireReview bool, req dto.PublishAgentRequest) (*dto.PublishResponse, error) {
	if org == nil {
		s.logger.WarnCtx(ctx, "publish submit org nil", map[string]any{"user_id": userID})
		return nil, fmt.Errorf("org nil: %w", agent.ErrAgentInvalidRequest)
	}
	a, err := s.repo.FindAgentByID(ctx, req.AgentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("agent not found: %w", agent.ErrAgentNotFound)
		}
		s.logger.ErrorCtx(ctx, "find agent failed", err, map[string]any{"agent_id": req.AgentID})
		return nil, fmt.Errorf("find agent: %w: %w", err, agent.ErrAgentInternal)
	}
	if a.OwnerUserID != userID {
		s.logger.WarnCtx(ctx, "publish submit not author", map[string]any{"agent_id": a.ID, "user_id": userID})
		return nil, fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	if a.Status != model.AgentStatusActive {
		s.logger.WarnCtx(ctx, "publish submit agent not active", map[string]any{"agent_id": a.ID, "status": a.Status})
		return nil, fmt.Errorf("agent not active: %w", agent.ErrAgentInvalidRequest)
	}
	// 预检:不能有 active publish
	if existing, fErr := s.repo.FindActivePublish(ctx, a.ID, org.ID); fErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "publish already exists", map[string]any{"agent_id": a.ID, "org_id": org.ID})
		return nil, fmt.Errorf("publish exists: %w", agent.ErrPublishAlreadyExists)
	} else if fErr != nil && !errors.Is(fErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "find active publish failed", fErr, map[string]any{"agent_id": a.ID, "org_id": org.ID})
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
		s.logger.ErrorCtx(ctx, "create publish failed", err, map[string]any{"agent_id": a.ID, "org_id": org.ID})
		return nil, fmt.Errorf("create publish: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publish submitted", map[string]any{"publish_id": p.ID, "status": status})
	resp := publishToDTO(p)
	return &resp, nil
}

// ListByOrg 分页列出 org 内的 publish。
//
// 错误:底层存储查询失败时返回 ErrAgentInternal。
func (s *publishService) ListByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]dto.PublishResponse, int64, error) {
	list, total, err := s.repo.ListPublishesByOrg(ctx, orgID, status, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "list publishes failed", err, map[string]any{"org_id": orgID})
		return nil, 0, fmt.Errorf("list publishes: %w: %w", err, agent.ErrAgentInternal)
	}
	out := make([]dto.PublishResponse, 0, len(list))
	for _, p := range list {
		out = append(out, publishToDTO(p))
	}
	return out, total, nil
}

// Approve 通过一条 pending publish。
//
// 错误:publish 不存在返回 ErrPublishNotFound,非 pending 返回 ErrPublishNotPending,其他存储错误返回 ErrAgentInternal。
func (s *publishService) Approve(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error) {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.reviewTransition(ctx, publishID, reviewerUserID, note, model.PublishStatusApproved)
}

// Reject 拒绝一条 pending publish。
//
// 错误:publish 不存在返回 ErrPublishNotFound,非 pending 返回 ErrPublishNotPending,其他存储错误返回 ErrAgentInternal。
func (s *publishService) Reject(ctx context.Context, publishID, reviewerUserID uint64, note string) (*dto.PublishResponse, error) {
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.reviewTransition(ctx, publishID, reviewerUserID, note, model.PublishStatusRejected)
}

// reviewTransition 处理 pending → approved/rejected 的公共逻辑。
func (s *publishService) reviewTransition(ctx context.Context, publishID, reviewerUserID uint64, note, target string) (*dto.PublishResponse, error) {
	p, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish failed", err, map[string]any{"publish_id": publishID})
		return nil, fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.Status != model.PublishStatusPending {
		s.logger.WarnCtx(ctx, "publish not pending", map[string]any{"publish_id": publishID, "status": p.Status})
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
		s.logger.ErrorCtx(ctx, "update publish failed", err, map[string]any{"publish_id": publishID})
		return nil, fmt.Errorf("update publish: %w: %w", err, agent.ErrAgentInternal)
	}
	reloaded, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		return nil, fmt.Errorf("reload publish: %w: %w", err, agent.ErrAgentInternal)
	}
	resp := publishToDTO(reloaded)
	return &resp, nil
}

// RevokeByAuthor 作者或同 org 的 publish 发起人主动下架。
//
// 错误:publish 不存在返回 ErrPublishNotFound,非提交者返回 ErrAgentNotAuthor,其他存储错误返回 ErrAgentInternal。
func (s *publishService) RevokeByAuthor(ctx context.Context, publishID, userID uint64) error {
	p, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish failed", err, map[string]any{"publish_id": publishID})
		return fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.SubmittedByUserID != userID {
		s.logger.WarnCtx(ctx, "revoke publish not author", map[string]any{"publish_id": publishID, "user_id": userID})
		return fmt.Errorf("not author: %w", agent.ErrAgentNotAuthor)
	}
	//sayso-lint:ignore sentinel-wrap,log-coverage
	return s.markRevoked(ctx, p.ID, agent.RevokedReasonAuthorUnpublished)
}

// RevokeByAdmin 管理员强制下架(reason=admin_banned)。
//
// 错误:publish 不存在返回 ErrPublishNotFound,其他存储错误返回 ErrAgentInternal。
func (s *publishService) RevokeByAdmin(ctx context.Context, publishID, adminUserID uint64) error {
	p, err := s.repo.FindPublishByID(ctx, publishID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return fmt.Errorf("publish not found: %w", agent.ErrPublishNotFound)
		}
		s.logger.ErrorCtx(ctx, "find publish failed", err, map[string]any{"publish_id": publishID})
		return fmt.Errorf("find publish: %w: %w", err, agent.ErrAgentInternal)
	}
	_ = p // 权限校验由 handler 前置完成,这里不再判断角色
	s.logger.InfoCtx(ctx, "publish banned by admin", map[string]any{"publish_id": publishID, "admin_user_id": adminUserID})
	//sayso-lint:ignore sentinel-wrap
	return s.markRevoked(ctx, publishID, agent.RevokedReasonAdminBanned)
}

// markRevoked 把 publish 标记为 revoked。
func (s *publishService) markRevoked(ctx context.Context, publishID uint64, reason string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":         model.PublishStatusRevoked,
		"revoked_at":     &now,
		"revoked_reason": reason,
	}
	if err := s.repo.UpdatePublishFields(ctx, publishID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "mark publish revoked failed", err, map[string]any{"publish_id": publishID})
		return fmt.Errorf("mark revoked: %w: %w", err, agent.ErrAgentInternal)
	}
	return nil
}

// GetActivePublish 查 (agent, org) 的当前 active publish,非 approved 返回 ErrAgentNotPublishedInOrg。
func (s *publishService) GetActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error) {
	p, err := s.repo.FindActivePublish(ctx, agentID, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("publish not in org: %w", agent.ErrAgentNotPublishedInOrg)
		}
		s.logger.ErrorCtx(ctx, "find active publish failed", err, map[string]any{"agent_id": agentID, "org_id": orgID})
		return nil, fmt.Errorf("find active publish: %w: %w", err, agent.ErrAgentInternal)
	}
	if p.Status != model.PublishStatusApproved {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("publish not approved: %w", agent.ErrAgentNotPublishedInOrg)
	}
	return p, nil
}

// ─── 跨模块 Hook ────────────────────────────────────────────────────────────

// RevokeByMember 成员被移除 / 主动退出 org 时,revoke 该成员在 org 内的所有 active publish。
// 注入为 organization 的 OnMemberRemovedHook。
//
// 错误:批量 revoke 存储失败时返回 ErrAgentInternal。
func (s *publishService) RevokeByMember(ctx context.Context, orgID, userID uint64, reason string) error {
	revokeReason := agent.RevokedReasonMemberLeft
	if reason != "" && reason != "leave" {
		revokeReason = agent.RevokedReasonAuthorRemoved
	}
	now := time.Now().UTC()
	affected, err := s.repo.RevokePublishesByAuthorOrg(ctx, orgID, userID, revokeReason, now)
	if err != nil {
		s.logger.ErrorCtx(ctx, "revoke publishes by member failed", err, map[string]any{"org_id": orgID, "user_id": userID})
		return fmt.Errorf("revoke by member: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publishes revoked by member leave", map[string]any{"org_id": orgID, "user_id": userID, "affected": affected})
	return nil
}

// RevokeByOrg org 解散时 revoke 所有 active publish。
// 注入为 organization 的 OnOrgDissolvedHook。
//
// 错误:批量 revoke 存储失败时返回 ErrAgentInternal。
func (s *publishService) RevokeByOrg(ctx context.Context, orgID uint64) error {
	now := time.Now().UTC()
	affected, err := s.repo.RevokePublishesByOrg(ctx, orgID, agent.RevokedReasonOrgDissolved, now)
	if err != nil {
		s.logger.ErrorCtx(ctx, "revoke publishes by org failed", err, map[string]any{"org_id": orgID})
		return fmt.Errorf("revoke by org: %w: %w", err, agent.ErrAgentInternal)
	}
	s.logger.InfoCtx(ctx, "publishes revoked by org dissolve", map[string]any{"org_id": orgID, "affected": affected})
	return nil
}
