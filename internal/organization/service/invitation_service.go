// invitation_service.go 组织邀请业务 service。
//
// 职责:
//   - 创建 / 撤销 / 重发 / 列出邀请
//   - Accept:登录用户持 token 接受邀请,事务内写 OrgMember + 把邀请状态推到 accepted
//   - Preview:未登录持 token 预览邀请摘要(org 名 / inviter / 角色 / 过期时间)
//   - 懒过期:Accept/Preview/List 发现 pending + ExpiresAt<now 时就地改 expired
//
// 关键设计:
//   - raw token 只出现在邮件和 URL 里,DB 只存 SHA-256 hash —— 脱库后无法伪造 accept
//   - Resend 语义 = 生成新 token + 更新 token_hash + 重置 ExpiresAt,老邮件链接自动失效
//   - 发邮件失败不回滚邀请,只 WARN 日志,调用方可以走 Resend 重试
//   - 现阶段无权限分级:"是成员即可邀请",invite owner 角色依旧硬拒(转让走单独接口)
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"gorm.io/gorm"
)

// InvitationService 定义邀请相关的业务操作。
//
//sayso-lint:ignore interface-pollution
type InvitationService interface {
	// Create 创建一条邀请,发 pending 邮件。
	// 相同 (org_id, email) pending 已存在 → 返 ErrInvitationDuplicatePending(让前端走 Resend)。
	Create(ctx context.Context, orgID, inviterUserID uint64, req dto.CreateInvitationRequest) (*dto.InvitationResponse, error)

	// Accept 登录用户持 raw token 接受邀请,成功后加入 org 成员。
	Accept(ctx context.Context, acceptingUserID uint64, req dto.AcceptInvitationRequest) (*dto.AcceptInvitationResult, error)

	// Revoke 撤销 pending 邀请(调用方已通过 orgContext 校验是成员)。
	Revoke(ctx context.Context, orgID, invitationID uint64) error

	// Resend 重发邮件。会换新 token、重置过期时间,老链接立即失效。
	Resend(ctx context.Context, orgID, invitationID uint64) (*dto.InvitationResponse, error)

	// List 列某 org 下邀请(statusFilter 为空则全部)。List 时顺带懒过期。
	List(ctx context.Context, orgID uint64, statusFilter string, page, size int) (*dto.ListInvitationsResponse, error)

	// Preview 未登录场景,前端落地页用 raw token 查邀请摘要。
	Preview(ctx context.Context, rawToken string) (*dto.InvitationPreviewResponse, error)

	// SearchCandidates 在 users 表里搜可邀请的候选对象。
	// searchType 取 "email" / "user_id" / "name" 之一。
	//   - email / user_id:精确匹配,最多 1 条返回
	//   - name:display_name 模糊匹配,最多 10 条返回,query 长度需 >=2 字符
	// 调用方需要是该 org 的成员(handler 层保证)。
	SearchCandidates(ctx context.Context, orgID uint64, searchType, query string) (*dto.SearchCandidatesResponse, error)

	// ListMyInvitations 列当前登录用户收到的邀请(被邀请人收件箱视图)。
	// statusFilter 为空时返全部(pending + 已处理);非空按 status 精确过滤。
	// 只要请求可能包含 pending,就在遍历时顺带懒过期。
	ListMyInvitations(ctx context.Context, acceptingUserID uint64, statusFilter string) (*dto.ListMyInvitationsResponse, error)

	// ListSentInvitations 列当前登录用户作为 inviter 发出的邀请(跨 org 发件箱)。
	// statusFilter 语义同上;只要请求可能包含 pending,就顺带懒过期。
	ListSentInvitations(ctx context.Context, inviterUserID uint64, statusFilter string) (*dto.ListSentInvitationsResponse, error)

	// AcceptByID 站内入口:登录用户在收件箱里点击接受,用 invitation id 而非 token。
	// 核心校验(pending + 未过期 + email 匹配)和 Accept(by token) 完全一致。
	AcceptByID(ctx context.Context, invitationID, acceptingUserID uint64) (*dto.AcceptInvitationResult, error)

	// Reject 被邀请人主动拒绝。需登录;email 必须匹配邀请 email;pending 邀请才可拒。
	Reject(ctx context.Context, invitationID, acceptingUserID uint64) error
}

// InvitationConfig InvitationService 的依赖/参数。
type InvitationConfig struct {
	// FrontendBaseURL 前端邀请落地页的 URL 前缀(例 "https://app.example.com/invite")。
	// 邮件里拼接成 "{FrontendBaseURL}?token={rawToken}"。
	// 空串时回退到 raw token 字符串(不推荐,只为方便 dev 降级)。
	FrontendBaseURL string
}

// ─── 邀请邮件发送限流 ──────────────────────────────────────────────────────────

// Key 前缀均登记到 internal/common/database/redis.go 的 Key Registry。
const (
	// inviteResendCooldownKeyPrefix 单条邀请 Resend cooldown。
	// 完整 key: synapse:inv_resend_cd:{invitation_id}
	inviteResendCooldownKeyPrefix = "synapse:inv_resend_cd"
	// inviteSendEmailDailyKeyPrefix per-target-email 每日发送配额。
	// 完整 key: synapse:inv_send_email:{email}:{YYYY-MM-DD}
	inviteSendEmailDailyKeyPrefix = "synapse:inv_send_email"
	// inviteSendInviterDailyKeyPrefix per-inviter 每日发送配额。
	// 完整 key: synapse:inv_send_inviter:{user_id}:{YYYY-MM-DD}
	inviteSendInviterDailyKeyPrefix = "synapse:inv_send_inviter"
	// inviteSendOrgDailyKeyPrefix per-org 每日发送配额。
	// 完整 key: synapse:inv_send_org:{org_id}:{YYYY-MM-DD}
	inviteSendOrgDailyKeyPrefix = "synapse:inv_send_org"
)

const (
	// inviteResendCooldown 同一条邀请 Resend 冷却时长。
	inviteResendCooldown = 60 * time.Second
	// inviteDailyCapTTL 各日限 key 的 TTL(覆盖 UTC 一整天,过天自然切新 key,老 key 自毁)。
	inviteDailyCapTTL = 24 * time.Hour

	// inviteDailyCapPerEmail 同一 target email 每日最多收到的邀请邮件数(跨 org/inviter 合计)。
	inviteDailyCapPerEmail = 10
	// inviteDailyCapPerInviter 同一 inviter 每日最多发起的邀请数。
	inviteDailyCapPerInviter = 50
	// inviteDailyCapPerOrg 同一 org 每日最多发起的邀请数(兜底)。
	inviteDailyCapPerOrg = 200
)

// ─── 实现 ────────────────────────────────────────────────────────────────────

type invitationService struct {
	cfg    InvitationConfig
	repo   repository.Repository
	sender email.Sender
	users  UserLookup
	guard  InviteGuard
	logger logger.LoggerInterface
}

// NewInvitationService 构造 InvitationService。
// sender / users 不能为 nil —— 没有它们邀请业务无法工作;main.go 装配时保证。
// guard 可为 nil:Redis 未就绪或单测场景自动 fail-open,主流程不阻断。
func NewInvitationService(
	cfg InvitationConfig,
	repo repository.Repository,
	sender email.Sender,
	users UserLookup,
	guard InviteGuard,
	log logger.LoggerInterface,
) InvitationService {
	return &invitationService{
		cfg:    cfg,
		repo:   repo,
		sender: sender,
		users:  users,
		guard:  guard,
		logger: log,
	}
}

// Create 创建一条邀请并发送邮件。
//
// 可能的错误:
//   - ErrInvitationEmailInvalid:email 格式非法
//   - ErrRoleNotFound:role_slug 找不到对应角色
//   - ErrInvitationCannotInviteOwner:角色是 owner
//   - ErrInvitationEmailAlreadyMember:email 已是成员
//   - ErrInvitationDuplicatePending:已有一条 pending 邀请
//   - ErrInvitationRateLimited:per-email / per-inviter / per-org 日限触顶
//   - ErrOrgNotFound / ErrOrgInternal:org 查找失败
//   - ErrOrgInternal:DB / token 生成失败
func (s *invitationService) Create(ctx context.Context, orgID, inviterUserID uint64, req dto.CreateInvitationRequest) (*dto.InvitationResponse, error) {
	normEmail, err := normalizeEmail(req.Email)
	if err != nil {
		s.logger.WarnCtx(ctx, "邀请 email 格式非法", map[string]any{"org_id": orgID, "email": req.Email})
		return nil, fmt.Errorf("invalid email: %w", organization.ErrInvitationEmailInvalid)
	}

	// role 存在性校验 + 非 owner
	role, err := s.repo.FindRoleByOrgAndSlug(ctx, orgID, req.RoleSlug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请目标角色不存在", map[string]any{"org_id": orgID, "role_slug": req.RoleSlug})
			return nil, fmt.Errorf("find role: %w", organization.ErrRoleNotFound)
		}
		s.logger.ErrorCtx(ctx, "查邀请目标角色失败", err, map[string]any{"org_id": orgID, "role_slug": req.RoleSlug})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	if role.Slug == organization.SystemRoleSlugOwner {
		s.logger.WarnCtx(ctx, "不能邀请 owner 角色", map[string]any{"org_id": orgID, "email": normEmail})
		return nil, fmt.Errorf("cannot invite owner: %w", organization.ErrInvitationCannotInviteOwner)
	}

	// email 已是成员 → 拒
	isMember, err := s.repo.IsEmailMemberOfOrg(ctx, orgID, normEmail)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查 email 是否已是成员失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("check member: %w: %w", err, organization.ErrOrgInternal)
	}
	if isMember {
		s.logger.WarnCtx(ctx, "邀请目标 email 已是成员", map[string]any{"org_id": orgID, "email": normEmail})
		return nil, fmt.Errorf("email already member: %w", organization.ErrInvitationEmailAlreadyMember)
	}

	// 同 org 同 email 已有 pending → 返错让调用方走 Resend
	if existing, findErr := s.repo.FindPendingInvitation(ctx, orgID, normEmail); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "已存在 pending 邀请", map[string]any{"org_id": orgID, "email": normEmail, "invitation_id": existing.ID})
		return nil, fmt.Errorf("duplicate pending: %w", organization.ErrInvitationDuplicatePending)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 pending 邀请失败", findErr, map[string]any{"org_id": orgID, "email": normEmail})
		return nil, fmt.Errorf("check pending: %w: %w", findErr, organization.ErrOrgInternal)
	}

	// 三道日限前置:DuplicatePending 先放前面,免得一条合法"去 Resend"的请求白白吃掉配额。
	if err := s.checkSendQuota(ctx, orgID, inviterUserID, normEmail); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 加载 org 用于邮件文案
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}

	rawToken, tokenHash, err := organization.GenerateInvitationToken()
	if err != nil {
		s.logger.ErrorCtx(ctx, "生成邀请 token 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("generate token: %w: %w", err, organization.ErrOrgInternal)
	}

	now := time.Now().UTC()
	inv := &model.OrgInvitation{
		OrgID:         orgID,
		InviterUserID: inviterUserID,
		Email:         normEmail,
		RoleID:        role.ID,
		TokenHash:     tokenHash,
		Status:        model.InvitationStatusPending,
		ExpiresAt:     now.Add(organization.DefaultInvitationTTL),
	}
	if err := s.repo.CreateInvitation(ctx, inv); err != nil {
		s.logger.ErrorCtx(ctx, "写邀请记录失败", err, map[string]any{"org_id": orgID, "email": normEmail})
		return nil, fmt.Errorf("create invitation: %w: %w", err, organization.ErrOrgInternal)
	}

	s.logger.InfoCtx(ctx, "邀请已创建", map[string]any{
		"org_id":        orgID,
		"invitation_id": inv.ID,
		"email":         normEmail,
		"role_slug":     role.Slug,
		"expires_at":    inv.ExpiresAt,
	})

	// 异步感不重要,同步发邮件失败也不回滚 —— 调用方可走 Resend 重试。
	s.sendInvitationEmail(ctx, org, inv, role, inviterUserID, rawToken)

	resp := invitationWithRoleToDTO(&repository.InvitationWithRole{
		Invitation:      inv,
		RoleSlug:        role.Slug,
		RoleDisplayName: role.DisplayName,
		RoleIsSystem:    role.IsSystem,
	})
	return &resp, nil
}

// Accept 登录用户持 raw token 接受邀请(邮件链接入口)。
//
// 可能的错误:
//   - ErrInvitationTokenInvalid:token 无对应记录
//   - ErrInvitationNotPending:已 accepted/revoked/rejected
//   - ErrInvitationExpired:过期
//   - ErrInvitationEmailMismatch:登录用户 email 与邀请 email 不一致
//   - ErrOrgInternal:DB 操作失败
func (s *invitationService) Accept(ctx context.Context, acceptingUserID uint64, req dto.AcceptInvitationRequest) (*dto.AcceptInvitationResult, error) {
	if req.Token == "" {
		return nil, fmt.Errorf("empty token: %w", organization.ErrInvitationTokenInvalid)
	}
	tokenHash := organization.HashInvitationToken(req.Token)

	inv, err := s.repo.FindInvitationByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请 token 无匹配", map[string]any{"user_id": acceptingUserID})
			return nil, fmt.Errorf("token not found: %w", organization.ErrInvitationTokenInvalid)
		}
		s.logger.ErrorCtx(ctx, "查邀请失败", err, map[string]any{"user_id": acceptingUserID})
		return nil, fmt.Errorf("find by token: %w: %w", err, organization.ErrOrgInternal)
	}
	//sayso-lint:ignore sentinel-wrap
	return s.acceptLoadedInvitation(ctx, inv, acceptingUserID)
}

// AcceptByID 站内收件箱入口:登录用户点击"接受"按钮,用 invitation id 接受。
// 和 Accept(by token) 的核心校验完全一致,只是找 invitation 的入口不同。
func (s *invitationService) AcceptByID(ctx context.Context, invitationID, acceptingUserID uint64) (*dto.AcceptInvitationResult, error) {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请不存在(by id)", map[string]any{"invitation_id": invitationID, "user_id": acceptingUserID})
			return nil, fmt.Errorf("not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "查邀请失败(by id)", err, map[string]any{"invitation_id": invitationID})
		return nil, fmt.Errorf("find by id: %w: %w", err, organization.ErrOrgInternal)
	}
	//sayso-lint:ignore sentinel-wrap
	return s.acceptLoadedInvitation(ctx, inv, acceptingUserID)
}

// acceptLoadedInvitation 接受流程的共同实现,Accept / AcceptByID 都先查到 inv 然后调这个。
// 完整流程:懒过期 → 状态校验 → email 匹配 → org 加载/active 校验 → 事务写 member + 推 accepted。
func (s *invitationService) acceptLoadedInvitation(ctx context.Context, inv *model.OrgInvitation, acceptingUserID uint64) (*dto.AcceptInvitationResult, error) {
	// 懒过期
	if inv.Status == model.InvitationStatusPending && time.Now().After(inv.ExpiresAt) {
		if markErr := s.markExpired(ctx, inv.ID); markErr != nil {
			s.logger.WarnCtx(ctx, "懒过期标记失败", map[string]any{"invitation_id": inv.ID})
		}
		return nil, fmt.Errorf("invitation expired: %w", organization.ErrInvitationExpired)
	}

	if inv.Status != model.InvitationStatusPending {
		s.logger.WarnCtx(ctx, "邀请已非 pending", map[string]any{"invitation_id": inv.ID, "status": inv.Status})
		return nil, fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}

	// 登录用户 email 必须匹配邀请 email
	userInfo, err := s.users.LookupUser(ctx, acceptingUserID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "查登录用户信息失败", err, map[string]any{"user_id": acceptingUserID})
		return nil, fmt.Errorf("lookup user: %w: %w", err, organization.ErrOrgInternal)
	}
	if !equalEmail(userInfo.Email, inv.Email) {
		s.logger.WarnCtx(ctx, "登录用户 email 与邀请不符", map[string]any{
			"invitation_id": inv.ID, "user_id": acceptingUserID,
		})
		return nil, fmt.Errorf("email mismatch: %w", organization.ErrInvitationEmailMismatch)
	}

	// 加载 org 用于结果返回
	org, err := s.repo.FindOrgByID(ctx, inv.OrgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查 org 失败", err, map[string]any{"org_id": inv.OrgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.Status != model.OrgStatusActive {
		// org 已解散,邀请已失效;状态推到 revoked(一种"不能接受的终态")
		if markErr := s.repo.UpdateInvitationFields(ctx, inv.ID, map[string]any{
			"status": model.InvitationStatusRevoked,
		}); markErr != nil {
			s.logger.WarnCtx(ctx, "org 已解散时更新邀请状态失败", map[string]any{"invitation_id": inv.ID})
		}
		return nil, fmt.Errorf("org dissolved: %w", organization.ErrOrgDissolved)
	}

	now := time.Now().UTC()
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		// 防御性:如果该 user 已经是成员,仍把邀请推到 accepted,但不重复 CreateMember。
		_, findErr := tx.FindMember(ctx, inv.OrgID, acceptingUserID)
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("tx find member: %w", findErr)
		}
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			member := &model.OrgMember{
				OrgID:    inv.OrgID,
				UserID:   acceptingUserID,
				RoleID:   inv.RoleID,
				JoinedAt: now,
			}
			if createErr := tx.CreateMember(ctx, member); createErr != nil {
				return fmt.Errorf("tx create member: %w", createErr)
			}
		}
		if updErr := tx.UpdateInvitationFields(ctx, inv.ID, map[string]any{
			"status":           model.InvitationStatusAccepted,
			"accepted_at":      &now,
			"accepted_user_id": acceptingUserID,
		}); updErr != nil {
			return fmt.Errorf("tx update invitation: %w", updErr)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorCtx(ctx, "接受邀请事务失败", err, map[string]any{"invitation_id": inv.ID, "user_id": acceptingUserID})
		return nil, fmt.Errorf("accept tx: %w: %w", err, organization.ErrOrgInternal)
	}

	s.logger.InfoCtx(ctx, "邀请已接受", map[string]any{
		"invitation_id": inv.ID, "org_id": inv.OrgID,
		"user_id": acceptingUserID, "role_id": inv.RoleID,
	})

	return &dto.AcceptInvitationResult{
		OrgID:       org.ID,
		OrgSlug:     org.Slug,
		DisplayName: org.DisplayName,
	}, nil
}

// Reject 被邀请人主动拒绝 pending 邀请。
//
// 可能的错误:
//   - ErrInvitationNotFound:id 对应记录不存在
//   - ErrInvitationEmailMismatch:登录用户 email 与邀请不匹配
//   - ErrInvitationNotPending:已非 pending
//   - ErrInvitationExpired:过期(懒过期后返错)
//   - ErrOrgInternal:DB 操作失败
func (s *invitationService) Reject(ctx context.Context, invitationID, acceptingUserID uint64) error {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "拒绝:邀请不存在", map[string]any{"invitation_id": invitationID})
			return fmt.Errorf("not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "拒绝:查邀请失败", err, map[string]any{"invitation_id": invitationID})
		return fmt.Errorf("find: %w: %w", err, organization.ErrOrgInternal)
	}

	// 懒过期
	if inv.Status == model.InvitationStatusPending && time.Now().After(inv.ExpiresAt) {
		if markErr := s.markExpired(ctx, inv.ID); markErr != nil {
			s.logger.WarnCtx(ctx, "拒绝:懒过期标记失败", map[string]any{"invitation_id": inv.ID})
		}
		return fmt.Errorf("expired: %w", organization.ErrInvitationExpired)
	}
	if inv.Status != model.InvitationStatusPending {
		return fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}

	// email 必须匹配 —— 这是"被邀请人才能拒绝"的唯一权限依据
	userInfo, err := s.users.LookupUser(ctx, acceptingUserID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "拒绝:查登录用户失败", err, map[string]any{"user_id": acceptingUserID})
		return fmt.Errorf("lookup user: %w: %w", err, organization.ErrOrgInternal)
	}
	if !equalEmail(userInfo.Email, inv.Email) {
		s.logger.WarnCtx(ctx, "拒绝:email 不匹配", map[string]any{"invitation_id": inv.ID, "user_id": acceptingUserID})
		return fmt.Errorf("email mismatch: %w", organization.ErrInvitationEmailMismatch)
	}

	if err := s.repo.UpdateInvitationFields(ctx, inv.ID, map[string]any{
		"status": model.InvitationStatusRejected,
	}); err != nil {
		s.logger.ErrorCtx(ctx, "拒绝:更新状态失败", err, map[string]any{"invitation_id": inv.ID})
		return fmt.Errorf("update: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "邀请已被拒绝", map[string]any{
		"invitation_id": inv.ID, "user_id": acceptingUserID,
	})
	return nil
}

// ListMyInvitations 返回当前登录用户收到的邀请(按 created_at DESC)。
// statusFilter:空 → 所有状态;非空 → 精确过滤。请求可能包含 pending 时顺带懒过期。
func (s *invitationService) ListMyInvitations(ctx context.Context, acceptingUserID uint64, statusFilter string) (*dto.ListMyInvitationsResponse, error) {
	userInfo, err := s.users.LookupUser(ctx, acceptingUserID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列收件箱:查登录用户失败", err, map[string]any{"user_id": acceptingUserID})
		return nil, fmt.Errorf("lookup user: %w: %w", err, organization.ErrOrgInternal)
	}

	rows, err := s.repo.ListInvitationsByEmail(ctx, userInfo.Email, statusFilter)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列收件箱失败", err, map[string]any{"user_id": acceptingUserID, "status": statusFilter})
		return nil, fmt.Errorf("list: %w: %w", err, organization.ErrOrgInternal)
	}

	now := time.Now()
	// 懒过期只在请求可能包含 pending 时做:statusFilter 为空(全量)或 ==pending
	lazyExpire := statusFilter == "" || statusFilter == model.InvitationStatusPending
	// 缓存 inviter user info,避免同一 inviter 多条邀请反复查 user service
	inviterNameCache := make(map[uint64]string)

	items := make([]dto.MyInvitationResponse, 0, len(rows))
	for _, row := range rows {
		effectiveStatus := row.Invitation.Status
		// 懒过期:pending + 已过 ExpiresAt → 就地改 expired;返回里也改为 expired
		if lazyExpire && row.Invitation.Status == model.InvitationStatusPending && now.After(row.Invitation.ExpiresAt) {
			if markErr := s.markExpired(ctx, row.Invitation.ID); markErr != nil {
				s.logger.WarnCtx(ctx, "收件箱:懒过期失败", map[string]any{"invitation_id": row.Invitation.ID})
			}
			effectiveStatus = model.InvitationStatusExpired
			// 若显式过滤 pending 则刚从 pending 降级的不再出现
			if statusFilter == model.InvitationStatusPending {
				continue
			}
		}

		inviterName, ok := inviterNameCache[row.Invitation.InviterUserID]
		if !ok {
			info, _ := s.users.LookupUser(ctx, row.Invitation.InviterUserID) // 查不到就用空串兜底
			if info != nil {
				inviterName = info.DisplayName
				if inviterName == "" {
					inviterName = info.Email
				}
			}
			inviterNameCache[row.Invitation.InviterUserID] = inviterName
		}

		items = append(items, dto.MyInvitationResponse{
			ID:             row.Invitation.ID,
			OrgSlug:        row.OrgSlug,
			OrgDisplayName: row.OrgDisplayName,
			InviterName:    inviterName,
			Role: dto.RoleSummary{
				Slug:        row.RoleSlug,
				DisplayName: row.RoleDisplayName,
				IsSystem:    row.RoleIsSystem,
			},
			Status:    effectiveStatus,
			ExpiresAt: row.Invitation.ExpiresAt.Unix(),
			CreatedAt: row.Invitation.CreatedAt.Unix(),
		})
	}
	return &dto.ListMyInvitationsResponse{Items: items}, nil
}

// ListSentInvitations 返回当前登录用户作为 inviter 发出的邀请(跨 org,按 created_at DESC)。
// statusFilter 语义同 ListMyInvitations;请求可能包含 pending 时顺带懒过期。
func (s *invitationService) ListSentInvitations(ctx context.Context, inviterUserID uint64, statusFilter string) (*dto.ListSentInvitationsResponse, error) {
	rows, err := s.repo.ListInvitationsByInviter(ctx, inviterUserID, statusFilter)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列发件箱失败", err, map[string]any{"user_id": inviterUserID, "status": statusFilter})
		return nil, fmt.Errorf("list: %w: %w", err, organization.ErrOrgInternal)
	}

	now := time.Now()
	lazyExpire := statusFilter == "" || statusFilter == model.InvitationStatusPending

	items := make([]dto.SentInvitationResponse, 0, len(rows))
	for _, row := range rows {
		effectiveStatus := row.Invitation.Status
		if lazyExpire && row.Invitation.Status == model.InvitationStatusPending && now.After(row.Invitation.ExpiresAt) {
			if markErr := s.markExpired(ctx, row.Invitation.ID); markErr != nil {
				s.logger.WarnCtx(ctx, "发件箱:懒过期失败", map[string]any{"invitation_id": row.Invitation.ID})
			}
			effectiveStatus = model.InvitationStatusExpired
			if statusFilter == model.InvitationStatusPending {
				continue
			}
		}

		items = append(items, dto.SentInvitationResponse{
			ID:             row.Invitation.ID,
			OrgSlug:        row.OrgSlug,
			OrgDisplayName: row.OrgDisplayName,
			Email:          row.Invitation.Email,
			Role: dto.RoleSummary{
				Slug:        row.RoleSlug,
				DisplayName: row.RoleDisplayName,
				IsSystem:    row.RoleIsSystem,
			},
			Status:    effectiveStatus,
			ExpiresAt: row.Invitation.ExpiresAt.Unix(),
			CreatedAt: row.Invitation.CreatedAt.Unix(),
		})
	}
	return &dto.ListSentInvitationsResponse{Items: items}, nil
}

// Revoke 撤销一条 pending 邀请。
func (s *invitationService) Revoke(ctx context.Context, orgID, invitationID uint64) error {
	inv, err := s.loadInvitationInOrg(ctx, orgID, invitationID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if inv.Status != model.InvitationStatusPending {
		s.logger.WarnCtx(ctx, "撤销非 pending 邀请被拒", map[string]any{"invitation_id": invitationID, "status": inv.Status})
		return fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}
	if err := s.repo.UpdateInvitationFields(ctx, inv.ID, map[string]any{
		"status": model.InvitationStatusRevoked,
	}); err != nil {
		s.logger.ErrorCtx(ctx, "撤销邀请失败", err, map[string]any{"invitation_id": invitationID})
		return fmt.Errorf("revoke: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "邀请已撤销", map[string]any{"invitation_id": invitationID})
	return nil
}

// Resend 为 pending 邀请生成新 token + 重置 ExpiresAt + 重发邮件。
//
// 过期 pending 会被懒过期 → 返 ErrInvitationExpired,调用方需要重建(走 Create)。
// 限流:先单条 60s cooldown,再跑 per-email / per-inviter / per-org 日限(Resend 计入 inviter/org 配额)。
func (s *invitationService) Resend(ctx context.Context, orgID, invitationID uint64) (*dto.InvitationResponse, error) {
	inv, err := s.loadInvitationInOrg(ctx, orgID, invitationID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	// 懒过期
	if inv.Status == model.InvitationStatusPending && time.Now().After(inv.ExpiresAt) {
		if markErr := s.markExpired(ctx, inv.ID); markErr != nil {
			s.logger.WarnCtx(ctx, "Resend 懒过期标记失败", map[string]any{"invitation_id": inv.ID})
		}
		return nil, fmt.Errorf("expired: %w", organization.ErrInvitationExpired)
	}
	if inv.Status != model.InvitationStatusPending {
		return nil, fmt.Errorf("not pending: %w", organization.ErrInvitationNotPending)
	}

	// 单条 cooldown 先拦 —— 命中不再 INCR 日限,不白白消耗配额
	if err := s.checkResendCooldown(ctx, invitationID); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	// 日限(和 Create 共用三道);inviter 用记录上的 InviterUserID(可能和当前调用方不同,但配额归属发起人更合理)
	if err := s.checkSendQuota(ctx, orgID, inv.InviterUserID, inv.Email); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 生成新 token
	rawToken, tokenHash, err := organization.GenerateInvitationToken()
	if err != nil {
		s.logger.ErrorCtx(ctx, "Resend 生成 token 失败", err, map[string]any{"invitation_id": invitationID})
		return nil, fmt.Errorf("generate token: %w: %w", err, organization.ErrOrgInternal)
	}
	now := time.Now().UTC()
	newExpires := now.Add(organization.DefaultInvitationTTL)
	if err := s.repo.UpdateInvitationFields(ctx, inv.ID, map[string]any{
		"token_hash": tokenHash,
		"expires_at": newExpires,
	}); err != nil {
		s.logger.ErrorCtx(ctx, "Resend 更新邀请失败", err, map[string]any{"invitation_id": invitationID})
		return nil, fmt.Errorf("update: %w: %w", err, organization.ErrOrgInternal)
	}
	inv.TokenHash = tokenHash
	inv.ExpiresAt = newExpires

	// 加载 org + role 给邮件渲染
	org, err := s.repo.FindOrgByID(ctx, inv.OrgID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "Resend 查 org 失败", err, map[string]any{"org_id": inv.OrgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	role, err := s.repo.FindRoleByID(ctx, inv.RoleID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "Resend 查 role 失败", err, map[string]any{"role_id": inv.RoleID})
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}

	s.sendInvitationEmail(ctx, org, inv, role, inv.InviterUserID, rawToken)

	s.logger.InfoCtx(ctx, "邀请已重发", map[string]any{"invitation_id": invitationID, "expires_at": newExpires})

	resp := invitationWithRoleToDTO(&repository.InvitationWithRole{
		Invitation:      inv,
		RoleSlug:        role.Slug,
		RoleDisplayName: role.DisplayName,
		RoleIsSystem:    role.IsSystem,
	})
	return &resp, nil
}

// List 分页列出某 org 的邀请,顺带懒过期。
func (s *invitationService) List(ctx context.Context, orgID uint64, statusFilter string, page, size int) (*dto.ListInvitationsResponse, error) {
	if size <= 0 || size > organization.MaxPageSize {
		size = organization.DefaultPageSize
	}
	if page <= 0 {
		page = 1
	}

	items, total, err := s.repo.ListInvitationsByOrg(ctx, orgID, statusFilter, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列邀请失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list invitations: %w: %w", err, organization.ErrOrgInternal)
	}

	now := time.Now()
	resp := &dto.ListInvitationsResponse{
		Items: make([]dto.InvitationResponse, 0, len(items)),
		Total: total,
		Page:  page,
		Size:  size,
	}
	for _, item := range items {
		// 懒过期:内存 patch + 单条 UPDATE
		if item.Invitation.Status == model.InvitationStatusPending && now.After(item.Invitation.ExpiresAt) {
			item.Invitation.Status = model.InvitationStatusExpired
			if markErr := s.markExpired(ctx, item.Invitation.ID); markErr != nil {
				s.logger.WarnCtx(ctx, "List 懒过期标记失败", map[string]any{"invitation_id": item.Invitation.ID})
			}
		}
		resp.Items = append(resp.Items, invitationWithRoleToDTO(item))
	}
	return resp, nil
}

// Preview 未登录场景按 raw token 查邀请摘要。
func (s *invitationService) Preview(ctx context.Context, rawToken string) (*dto.InvitationPreviewResponse, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("empty token: %w", organization.ErrInvitationTokenInvalid)
	}
	tokenHash := organization.HashInvitationToken(rawToken)
	inv, err := s.repo.FindInvitationByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("token not found: %w", organization.ErrInvitationTokenInvalid)
		}
		s.logger.ErrorCtx(ctx, "Preview 查邀请失败", err, nil)
		return nil, fmt.Errorf("find by token: %w: %w", err, organization.ErrOrgInternal)
	}

	// 懒过期
	if inv.Status == model.InvitationStatusPending && time.Now().After(inv.ExpiresAt) {
		if markErr := s.markExpired(ctx, inv.ID); markErr != nil {
			s.logger.WarnCtx(ctx, "Preview 懒过期标记失败", map[string]any{"invitation_id": inv.ID})
		}
		inv.Status = model.InvitationStatusExpired
	}

	org, err := s.repo.FindOrgByID(ctx, inv.OrgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "Preview 查 org 失败", err, nil)
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	role, err := s.repo.FindRoleByID(ctx, inv.RoleID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "Preview 查 role 失败", err, nil)
		return nil, fmt.Errorf("find role: %w: %w", err, organization.ErrOrgInternal)
	}
	inviterInfo, _ := s.users.LookupUser(ctx, inv.InviterUserID) // 忽略错误,inviter 可能已删;邮件前缀兜底
	var inviterName string
	if inviterInfo != nil {
		inviterName = inviterInfo.DisplayName
		if inviterName == "" {
			inviterName = inviterInfo.Email
		}
	}

	var roleSlug, roleDisplayName string
	var roleIsSystem bool
	if role != nil {
		roleSlug = role.Slug
		roleDisplayName = role.DisplayName
		roleIsSystem = role.IsSystem
	}

	return &dto.InvitationPreviewResponse{
		OrgSlug:        org.Slug,
		OrgDisplayName: org.DisplayName,
		InviterName:    inviterName,
		Email:          inv.Email,
		Role: dto.RoleSummary{
			Slug:        roleSlug,
			DisplayName: roleDisplayName,
			IsSystem:    roleIsSystem,
		},
		Status:    inv.Status,
		ExpiresAt: inv.ExpiresAt.Unix(),
	}, nil
}

// SearchCandidates 按 (searchType, query) 在全站 users 中搜可邀请对象。
//
// 参数合法性由本方法校验(handler 不重复校验):
//   - email   : 必须 mail.ParseAddress 成功,query 先 trim + lower
//   - user_id : 必须纯十进制数字且非 0
//   - name    : trim 后 utf8 长度 >=2,SQL 层包装成 LIKE '%query%'
//   - 其他 type : ErrInvitationSearchInvalid
//
// 可能的错误:
//   - ErrInvitationSearchInvalid:type 或 query 非法
//   - ErrOrgInternal:DB 查询失败
func (s *invitationService) SearchCandidates(ctx context.Context, orgID uint64, searchType, query string) (*dto.SearchCandidatesResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query: %w", organization.ErrInvitationSearchInvalid)
	}

	// 规范化 query + limit
	var repoQuery string
	var limit int
	switch searchType {
	case repository.InviteSearchTypeEmail:
		addr, parseErr := mail.ParseAddress(query)
		if parseErr != nil {
			s.logger.WarnCtx(ctx, "search: email 格式非法", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("invalid email: %w", organization.ErrInvitationSearchInvalid)
		}
		repoQuery = strings.ToLower(addr.Address)
		limit = 1
	case repository.InviteSearchTypeUserID:
		// ParseUint 拒绝负号 / 小数点 / 非数字字符,正好符合需求
		if _, parseErr := strconvParseUint(query); parseErr != nil {
			s.logger.WarnCtx(ctx, "search: user_id 非纯数字", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("invalid user_id: %w", organization.ErrInvitationSearchInvalid)
		}
		repoQuery = query
		limit = 1
	case repository.InviteSearchTypeName:
		// 按 rune 计数,避免纯英文 len("ab")=2 和纯中文 len("张三")=6 的差异性
		if runeLen(query) < 2 {
			s.logger.WarnCtx(ctx, "search: name 过短", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("query too short: %w", organization.ErrInvitationSearchInvalid)
		}
		// LIKE 里的 % _ 转义:本地部署场景简化处理,把 % 和 _ 替换成 \% \_,
		// MySQL LIKE 默认 ESCAPE='\\',匹配字面量。
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		repoQuery = "%" + escaped + "%"
		limit = 10
	default:
		s.logger.WarnCtx(ctx, "search: 未知 searchType", map[string]any{"org_id": orgID, "type": searchType})
		return nil, fmt.Errorf("unknown search type: %w", organization.ErrInvitationSearchInvalid)
	}

	candidates, err := s.repo.SearchInviteCandidates(ctx, orgID, searchType, repoQuery, limit)
	if err != nil {
		s.logger.ErrorCtx(ctx, "search: 查候选失败", err, map[string]any{"org_id": orgID, "type": searchType})
		return nil, fmt.Errorf("search: %w: %w", err, organization.ErrOrgInternal)
	}

	items := make([]dto.InviteCandidateResponse, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, dto.InviteCandidateResponse{
			UserID:           c.UserID,
			Email:            c.Email,
			DisplayName:      c.DisplayName,
			AvatarURL:        c.AvatarURL,
			IsMember:         c.IsMember,
			HasPendingInvite: c.HasPendingInvite,
		})
	}
	return &dto.SearchCandidatesResponse{Items: items}, nil
}

// strconvParseUint 是 strconv.ParseUint 的薄包装,避免在本文件多处 import strconv。
// 由于只在搜索路径用一次,独立出来让主逻辑简洁。
func strconvParseUint(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}

// runeLen 返回字符串的 unicode 字符数(非字节数)。
// 用于按"字符"校验 name 查询长度,防止纯中文查询被误判过短。
func runeLen(s string) int {
	return len([]rune(s))
}

// ─── 内部工具 ────────────────────────────────────────────────────────────────

// loadInvitationInOrg 按 id 加载邀请,并校验归属的 orgID。
func (s *invitationService) loadInvitationInOrg(ctx context.Context, orgID, invitationID uint64) (*model.OrgInvitation, error) {
	inv, err := s.repo.FindInvitationByID(ctx, invitationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "邀请不存在", map[string]any{"invitation_id": invitationID})
			return nil, fmt.Errorf("not found: %w", organization.ErrInvitationNotFound)
		}
		s.logger.ErrorCtx(ctx, "查邀请失败", err, map[string]any{"invitation_id": invitationID})
		return nil, fmt.Errorf("find invitation: %w: %w", err, organization.ErrOrgInternal)
	}
	if inv.OrgID != orgID {
		// 当作不存在,不暴露跨 org 的 id 存在性
		s.logger.WarnCtx(ctx, "邀请 org 不匹配", map[string]any{"invitation_id": invitationID, "expected_org": orgID, "actual_org": inv.OrgID})
		return nil, fmt.Errorf("cross-org: %w", organization.ErrInvitationNotFound)
	}
	return inv, nil
}

// markExpired 把某条邀请状态推到 expired。
// 失败只 WARN 日志,上层仍按 expired 处理(业务语义不变)。
func (s *invitationService) markExpired(ctx context.Context, invitationID uint64) error {
	return s.repo.UpdateInvitationFields(ctx, invitationID, map[string]any{
		"status": model.InvitationStatusExpired,
	})
}

// checkSendQuota 跑"每日"三道闸:per-email / per-inviter / per-org。
// 命中任一即 ErrInvitationRateLimited。Redis 故障 fail-open(打 log 继续走)。
// 顺序:email → inviter → org(从"受害者保护"到"资源保护",前者命中就不必再 INCR 后面的)。
func (s *invitationService) checkSendQuota(ctx context.Context, orgID, inviterUserID uint64, normEmail string) error {
	if s.guard == nil {
		return nil
	}
	day := time.Now().UTC().Format("2006-01-02")

	emailKey := fmt.Sprintf("%s:%s:%s", inviteSendEmailDailyKeyPrefix, normEmail, day)
	if n, err := s.guard.TouchCounter(ctx, emailKey, inviteDailyCapTTL); err == nil && n > inviteDailyCapPerEmail {
		s.logger.WarnCtx(ctx, "邀请邮件触发 per-email 日限", map[string]any{
			"email": normEmail, "count": n, "cap": inviteDailyCapPerEmail,
		})
		return fmt.Errorf("per-email daily cap: %w", organization.ErrInvitationRateLimited)
	}

	inviterKey := fmt.Sprintf("%s:%d:%s", inviteSendInviterDailyKeyPrefix, inviterUserID, day)
	if n, err := s.guard.TouchCounter(ctx, inviterKey, inviteDailyCapTTL); err == nil && n > inviteDailyCapPerInviter {
		s.logger.WarnCtx(ctx, "邀请邮件触发 per-inviter 日限", map[string]any{
			"inviter_id": inviterUserID, "count": n, "cap": inviteDailyCapPerInviter,
		})
		return fmt.Errorf("per-inviter daily cap: %w", organization.ErrInvitationRateLimited)
	}

	orgKey := fmt.Sprintf("%s:%d:%s", inviteSendOrgDailyKeyPrefix, orgID, day)
	if n, err := s.guard.TouchCounter(ctx, orgKey, inviteDailyCapTTL); err == nil && n > inviteDailyCapPerOrg {
		s.logger.WarnCtx(ctx, "邀请邮件触发 per-org 日限", map[string]any{
			"org_id": orgID, "count": n, "cap": inviteDailyCapPerOrg,
		})
		return fmt.Errorf("per-org daily cap: %w", organization.ErrInvitationRateLimited)
	}
	return nil
}

// checkResendCooldown 单条邀请 Resend 60s cooldown。命中即 ErrInvitationRateLimited。
// Redis 故障 fail-open。
func (s *invitationService) checkResendCooldown(ctx context.Context, invitationID uint64) error {
	if s.guard == nil {
		return nil
	}
	key := fmt.Sprintf("%s:%d", inviteResendCooldownKeyPrefix, invitationID)
	n, err := s.guard.TouchCounter(ctx, key, inviteResendCooldown)
	if err != nil {
		return nil
	}
	if n > 1 {
		s.logger.WarnCtx(ctx, "邀请 Resend 触发 cooldown", map[string]any{
			"invitation_id": invitationID, "count": n,
		})
		return fmt.Errorf("resend cooldown: %w", organization.ErrInvitationRateLimited)
	}
	return nil
}

// sendInvitationEmail 构建并发送邀请邮件。
// 失败只 WARN 日志,不影响主响应 —— 调用方可以走 Resend 重试。
func (s *invitationService) sendInvitationEmail(
	ctx context.Context, org *model.Org, inv *model.OrgInvitation, role *model.OrgRole,
	inviterUserID uint64, rawToken string,
) {
	inviterInfo, err := s.users.LookupUser(ctx, inviterUserID)
	if err != nil {
		s.logger.WarnCtx(ctx, "查 inviter 信息失败,邮件用兜底名", map[string]any{"inviter_id": inviterUserID, "invitation_id": inv.ID})
	}
	inviterName := "Synapse"
	locale := ""
	if inviterInfo != nil {
		if inviterInfo.DisplayName != "" {
			inviterName = inviterInfo.DisplayName
		} else if inviterInfo.Email != "" {
			inviterName = inviterInfo.Email
		}
		locale = inviterInfo.Locale
	}

	link := buildInvitationLink(s.cfg.FrontendBaseURL, rawToken)
	subject, body := email.BuildInvitationEmail(
		locale, org.DisplayName, inviterName, role.DisplayName,
		email.InvitationTypeMember, link, inv.ExpiresAt,
	)
	if err := s.sender.SendVerificationEmail(ctx, inv.Email, subject, body); err != nil {
		s.logger.WarnCtx(ctx, "邀请邮件发送失败(不影响邀请创建,可走 Resend)", map[string]any{
			"invitation_id": inv.ID, "email": inv.Email, "err": err.Error(),
		})
		return
	}
	s.logger.InfoCtx(ctx, "邀请邮件已发送", map[string]any{"invitation_id": inv.ID, "email": inv.Email})
}

// buildInvitationLink 把 token 拼到前端 URL 上。
// base 为空时返 raw token,确保 dev 无前端时日志里也能看到 token。
func buildInvitationLink(base, rawToken string) string {
	if base == "" {
		return rawToken
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%stoken=%s", base, sep, rawToken)
}

// normalizeEmail 校验格式并转为 lower case。
func normalizeEmail(raw string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return strings.ToLower(addr.Address), nil
}

// equalEmail 大小写不敏感 email 比较。
func equalEmail(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
