// invitation_service.go 邀请全流程 service。
//
//sayso-lint:ignore file-size
//
// 职责:
//   - 候选人查找(按 user_id / 昵称 / 手机号 / 邮箱)
//   - 创建邀请(pending),强制被邀请人已注册
//   - 列出我的邀请 / org 的邀请
//   - 接受 / 拒绝 / 撤销
//   - 所有权转让的特殊接受流程(owner 交接)
//   - 定时任务:过期邀请批量标记 expired
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// InvitationService 定义邀请相关业务操作。
//sayso-lint:ignore interface-pollution
type InvitationService interface {
	// SearchInvitees 按 user_id / 昵称 / 手机号 / 邮箱 查找候选人。
	SearchInvitees(ctx context.Context, req dto.SearchInviteesRequest) (*dto.SearchInviteesResponse, error)

	// CreateInvitation 创建一条邀请。
	// inviterUserID 是发起人,orgID / inviteeUserID / roleID 来自 request。
	CreateInvitation(ctx context.Context, inviterUserID, orgID uint64, req dto.CreateInvitationRequest) (*dto.InvitationResponse, error)

	// InitiateOwnershipTransfer 发起所有权转让(生成 ownership_transfer 类型邀请)。
	InitiateOwnershipTransfer(ctx context.Context, inviterUserID, orgID, targetUserID uint64) (*dto.InvitationResponse, error)

	// ListByOrg 列出 org 的 pending 邀请。
	ListByOrg(ctx context.Context, orgID uint64, page, size int) (*dto.ListInvitationsResponse, error)

	// ListMine 列出当前用户收到的 pending 邀请。
	ListMine(ctx context.Context, userID uint64, page, size int) (*dto.ListInvitationsResponse, error)

	// Accept 接受邀请(普通成员 or 所有权转让)。
	Accept(ctx context.Context, userID, invitationID uint64) error

	// Reject 拒绝邀请。
	Reject(ctx context.Context, userID, invitationID uint64) error

	// Revoke 撤销邀请(发起人或有 PermMemberRemove 的管理员)。
	Revoke(ctx context.Context, operatorUserID, invitationID uint64) error

	// ExpireJob 定时任务入口,批量把过期 pending 邀请标记为 expired。
	// 返回受影响行数便于定时任务日志追踪。
	ExpireJob(ctx context.Context) (int64, error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type invitationService struct {
	cfg    Config
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewInvitationService 构造一个 InvitationService 实例。
func NewInvitationService(cfg Config, repo repository.Repository, log logger.LoggerInterface) InvitationService {
	return &invitationService{cfg: cfg, repo: repo, logger: log}
}

// SearchInvitees 按 user_id / 昵称 / 手机号 / 邮箱 查找已注册用户候选人。
// 昵称不唯一时返回候选列表(最多 MaxInviteeCandidates)。查询参数缺失返回 ErrOrgInvalidRequest,数据库错误返回 ErrOrgInternal。
func (s *invitationService) SearchInvitees(ctx context.Context, req dto.SearchInviteesRequest) (*dto.SearchInviteesResponse, error) {
	resp := &dto.SearchInviteesResponse{Candidates: []dto.InviteeCandidate{}}

	switch dto.InviteQueryType(req.QueryType) {
	case dto.InviteQueryByUserID:
		if req.UserID == 0 {
			s.logger.WarnCtx(ctx, "缺少 user_id", nil)
			return nil, fmt.Errorf("missing user_id: %w", organization.ErrOrgInvalidRequest)
		}
		p, err := s.repo.FindUserProfileByID(ctx, req.UserID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "查询用户失败", err, map[string]any{"user_id": req.UserID})
			return nil, fmt.Errorf("find user: %w: %w", err, organization.ErrOrgInternal)
		}
		if p != nil {
			resp.Candidates = append(resp.Candidates, userProfileToCandidate(p))
		}

	case dto.InviteQueryByNickname:
		if strings.TrimSpace(req.Nickname) == "" {
			s.logger.WarnCtx(ctx, "缺少昵称", nil)
			return nil, fmt.Errorf("missing nickname: %w", organization.ErrOrgInvalidRequest)
		}
		list, err := s.repo.SearchUserProfilesByDisplayName(ctx, req.Nickname, organization.MaxInviteeCandidates)
		if err != nil {
			s.logger.ErrorCtx(ctx, "按昵称查询失败", err, map[string]any{"nickname": req.Nickname})
			return nil, fmt.Errorf("search nickname: %w: %w", err, organization.ErrOrgInternal)
		}
		for _, p := range list {
			resp.Candidates = append(resp.Candidates, userProfileToCandidate(p))
		}

	case dto.InviteQueryByEmail:
		if req.Email == "" {
			s.logger.WarnCtx(ctx, "缺少邮箱", nil)
			return nil, fmt.Errorf("missing email: %w", organization.ErrOrgInvalidRequest)
		}
		p, err := s.repo.FindUserProfileByEmail(ctx, req.Email)
		if err != nil {
			s.logger.ErrorCtx(ctx, "按邮箱查询失败", err, nil)
			return nil, fmt.Errorf("find by email: %w: %w", err, organization.ErrOrgInternal)
		}
		if p != nil {
			resp.Candidates = append(resp.Candidates, userProfileToCandidate(p))
		}

	default:
		s.logger.WarnCtx(ctx, "未知的查询类型", map[string]any{"query_type": req.QueryType})
		return nil, fmt.Errorf("unknown query type: %w", organization.ErrOrgInvalidRequest)
	}

	return resp, nil
}

// CreateInvitation 为已注册用户创建待确认邀请。被邀请人必须已注册且非成员,不能邀请为 owner 角色。
// 可能返回:ErrInvitationInvalidTarget / ErrInvitationSelf / ErrInvitationInviteeNotRegistered / ErrInvitationAlreadyMember / ErrInvitationAlreadyPending / ErrRoleNotFound / ErrOrgInvalidRequest / ErrOrgInternal
func (s *invitationService) CreateInvitation(ctx context.Context, inviterUserID, orgID uint64, req dto.CreateInvitationRequest) (*dto.InvitationResponse, error) {
	if req.InviteeUserID == 0 {
		s.logger.WarnCtx(ctx, "邀请目标 user_id 为空", map[string]any{"inviter": inviterUserID, "org_id": orgID})
		return nil, fmt.Errorf("missing invitee: %w", organization.ErrInvitationInvalidTarget)
	}
	if req.InviteeUserID == inviterUserID {
		s.logger.WarnCtx(ctx, "不能邀请自己", map[string]any{"user_id": inviterUserID, "org_id": orgID})
		return nil, fmt.Errorf("cannot invite self: %w", organization.ErrInvitationSelf)
	}

	// 被邀请人必须已注册
	profile, err := s.repo.FindUserProfileByID(ctx, req.InviteeUserID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查询被邀请人失败", err, map[string]any{"invitee": req.InviteeUserID})
		return nil, fmt.Errorf("find invitee: %w: %w", err, organization.ErrOrgInternal)
	}
	if profile == nil {
		s.logger.WarnCtx(ctx, "被邀请人未注册", map[string]any{"invitee": req.InviteeUserID})
		return nil, fmt.Errorf("invitee not registered: %w", organization.ErrInvitationInviteeNotRegistered)
	}

	// 检查被邀请人是否已是成员
	if existing, err := s.repo.FindMember(ctx, orgID, req.InviteeUserID); err == nil && existing != nil {
		s.logger.WarnCtx(ctx, "被邀请人已是成员", map[string]any{"invitee": req.InviteeUserID, "org_id": orgID})
		return nil, fmt.Errorf("already member: %w", organization.ErrInvitationAlreadyMember)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"invitee": req.InviteeUserID})
		return nil, fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}

	// 检查是否已有 pending 邀请
	if existing, err := s.repo.FindPendingByOrgInvitee(ctx, orgID, req.InviteeUserID); err == nil && existing != nil {
		s.logger.WarnCtx(ctx, "已有 pending 邀请", map[string]any{"invitee": req.InviteeUserID, "org_id": orgID})
		return nil, fmt.Errorf("already pending: %w", organization.ErrInvitationAlreadyPending)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查询 pending 邀请失败", err, nil)
		return nil, fmt.Errorf("find pending: %w: %w", err, organization.ErrOrgInternal)
	}

	// 校验 role 属于同 org 且非 owner 角色
	role, err := s.repo.FindRoleByID(ctx, req.RoleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "目标角色不存在", map[string]any{"role_id": req.RoleID})
			return nil, fmt.Errorf("role not found: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询角色失败", err, map[string]any{"role_id": req.RoleID})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if role.OrgID != orgID {
		s.logger.WarnCtx(ctx, "角色不属于该 org", map[string]any{"role_id": req.RoleID, "org_id": orgID})
		return nil, fmt.Errorf("role not in org: %w", organization.ErrRoleNotFound)
	}
	if role.Name == organization.RoleOwner {
		s.logger.WarnCtx(ctx, "不能通过普通邀请给 owner 角色", map[string]any{"role_id": req.RoleID})
		return nil, fmt.Errorf("cannot invite as owner: %w", organization.ErrOrgInvalidRequest)
	}

	// 创建邀请
	now := time.Now().UTC()
	inv := &model.OrgInvitation{
		OrgID:         orgID,
		InviterUserID: inviterUserID,
		InviteeUserID: req.InviteeUserID,
		RoleID:        req.RoleID,
		Type:          model.InvitationTypeMember,
		Status:        model.InvitationStatusPending,
		ExpiresAt:     now.AddDate(0, 0, s.cfg.InvitationExpiresDays),
	}
	if err := s.repo.CreateInvitation(ctx, inv); err != nil {
		s.logger.ErrorCtx(ctx, "创建邀请失败", err, map[string]any{"org_id": orgID, "invitee": req.InviteeUserID})
		return nil, fmt.Errorf("create invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "邀请已创建", map[string]any{"inv_id": inv.ID, "org_id": orgID, "invitee": req.InviteeUserID, "role_id": req.RoleID})

	resp := s.invitationToDTO(ctx, inv)
	return &resp, nil
}

// InitiateOwnershipTransfer 发起所有权转让。只有当前 owner 可调用,target 必须是 org 活跃成员且非自己。
// 实际的 owner 交接在 Accept 里的事务中完成。
// 可能返回:ErrInvitationSelf / ErrOrgNotFound / ErrOrgOwnerOnly / ErrTransferTargetNotMember / ErrInvitationAlreadyPending / ErrOrgInternal
func (s *invitationService) InitiateOwnershipTransfer(ctx context.Context, inviterUserID, orgID, targetUserID uint64) (*dto.InvitationResponse, error) {
	if targetUserID == inviterUserID {
		s.logger.WarnCtx(ctx, "不能转让给自己", map[string]any{"user_id": inviterUserID, "org_id": orgID})
		return nil, fmt.Errorf("cannot transfer to self: %w", organization.ErrInvitationSelf)
	}
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.OwnerUserID != inviterUserID {
		s.logger.WarnCtx(ctx, "非 owner 不能发起转让", map[string]any{"org_id": orgID, "user_id": inviterUserID})
		return nil, fmt.Errorf("not owner: %w", organization.ErrOrgOwnerOnly)
	}

	// 目标必须是 org 成员(member 本身不用,仅查存在性)
	//sayso-lint:ignore err-swallow
	if _, err := s.repo.FindMember(ctx, orgID, targetUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "转让目标非成员", map[string]any{"org_id": orgID, "target": targetUserID})
			return nil, fmt.Errorf("target not member: %w", organization.ErrTransferTargetNotMember)
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "target": targetUserID})
		return nil, fmt.Errorf("find target: %w: %w", err, organization.ErrOrgInternal)
	}

	// 已有 pending 邀请拒绝(任意类型)
	if existing, err := s.repo.FindPendingByOrgInvitee(ctx, orgID, targetUserID); err == nil && existing != nil {
		s.logger.WarnCtx(ctx, "目标已有 pending 邀请", map[string]any{"org_id": orgID, "target": targetUserID})
		return nil, fmt.Errorf("already pending: %w", organization.ErrInvitationAlreadyPending)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 pending 失败", err, nil)
		return nil, fmt.Errorf("find pending: %w: %w", err, organization.ErrOrgInternal)
	}

	// 查 owner 角色 id(转让时让目标继承这个角色)
	ownerRole, err := s.repo.FindRoleByOrgName(ctx, orgID, organization.RoleOwner)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查 owner 角色失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find owner role: %w: %w", err, organization.ErrOrgInternal)
	}

	now := time.Now().UTC()
	inv := &model.OrgInvitation{
		OrgID:         orgID,
		InviterUserID: inviterUserID,
		InviteeUserID: targetUserID,
		RoleID:        ownerRole.ID,
		Type:          model.InvitationTypeOwnershipTransfer,
		Status:        model.InvitationStatusPending,
		ExpiresAt:     now.AddDate(0, 0, s.cfg.InvitationExpiresDays),
	}
	if err := s.repo.CreateInvitation(ctx, inv); err != nil {
		s.logger.ErrorCtx(ctx, "创建转让邀请失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("create transfer invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "所有权转让邀请已创建", map[string]any{"inv_id": inv.ID, "org_id": orgID, "target": targetUserID})

	resp := s.invitationToDTO(ctx, inv)
	return &resp, nil
}

// ListByOrg 分页列出某 org 的所有 pending 邀请。数据库错误返回 ErrOrgInternal。
func (s *invitationService) ListByOrg(ctx context.Context, orgID uint64, page, size int) (*dto.ListInvitationsResponse, error) {
	if size <= 0 || size > organization.MaxPageSize {
		size = organization.DefaultPageSize
	}
	if page <= 0 {
		page = 1
	}
	list, total, err := s.repo.ListPendingByOrg(ctx, orgID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出 org 邀请失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list org invitations: %w: %w", err, organization.ErrOrgInternal)
	}
	return s.buildListResponse(ctx, list, total, page, size), nil
}

// ListMine 分页列出当前用户收到的 pending 邀请。数据库错误返回 ErrOrgInternal。
func (s *invitationService) ListMine(ctx context.Context, userID uint64, page, size int) (*dto.ListInvitationsResponse, error) {
	if size <= 0 || size > organization.MaxPageSize {
		size = organization.DefaultPageSize
	}
	if page <= 0 {
		page = 1
	}
	list, total, err := s.repo.ListPendingByInvitee(ctx, userID, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出我的邀请失败", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("list my invitations: %w: %w", err, organization.ErrOrgInternal)
	}
	return s.buildListResponse(ctx, list, total, page, size), nil
}

// ─── 私有工具 ────────────────────────────────────────────────────────────────

// invitationToDTO 将 model 转成 DTO,补充 org 和 role 信息以便前端展示。
// 查询失败时降级为只包含基础字段的响应,不中断主流程。
func (s *invitationService) invitationToDTO(ctx context.Context, inv *model.OrgInvitation) dto.InvitationResponse {
	resp := dto.InvitationResponse{
		ID:            inv.ID,
		OrgID:         inv.OrgID,
		InviterUserID: inv.InviterUserID,
		InviteeUserID: inv.InviteeUserID,
		Type:          inv.Type,
		Status:        inv.Status,
		ExpiresAt:     inv.ExpiresAt.Unix(),
		CreatedAt:     inv.CreatedAt.Unix(),
	}
	if org, err := s.repo.FindOrgByID(ctx, inv.OrgID); err == nil && org != nil {
		resp.OrgSlug = org.Slug
		resp.OrgDisplayName = org.DisplayName
	}
	if role, err := s.repo.FindRoleByID(ctx, inv.RoleID); err == nil && role != nil {
		r := roleToSummary(role)
		resp.Role = &r
	}
	return resp
}

// buildListResponse 组装分页列表响应,批量加载 org 和 role 信息。
func (s *invitationService) buildListResponse(ctx context.Context, list []*model.OrgInvitation, total int64, page, size int) *dto.ListInvitationsResponse {
	resp := &dto.ListInvitationsResponse{
		Items: make([]dto.InvitationResponse, 0, len(list)),
		Total: total,
		Page:  page,
		Size:  size,
	}
	for _, inv := range list {
		resp.Items = append(resp.Items, s.invitationToDTO(ctx, inv))
	}
	return resp
}
