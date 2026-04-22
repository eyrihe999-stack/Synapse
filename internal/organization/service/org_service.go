// org_service.go 组织本体的 CRUD / 解散 service。
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

// OrgService 定义组织本体的业务操作。
//sayso-lint:ignore interface-pollution
type OrgService interface {
	// CreateOrg 创建一个 org 并把调用者设为 owner。
	CreateOrg(ctx context.Context, userID uint64, req dto.CreateOrgRequest) (*dto.OrgResponse, error)

	// GetOrgBySlug 查 org 详情。调用方应该先确认 user 是成员。
	GetOrgBySlug(ctx context.Context, slug string) (*dto.OrgResponse, error)

	// CheckSlug 预检 slug 合法性与可用性。
	CheckSlug(ctx context.Context, slug string) (*dto.CheckSlugResponse, error)

	// ListOrgsByUser 列出某用户所属的所有 org。
	ListOrgsByUser(ctx context.Context, userID uint64) ([]dto.OrgWithMembershipResponse, error)

	// UpdateOrg 更新 org 基础信息(display_name / description)。
	UpdateOrg(ctx context.Context, orgID uint64, req dto.UpdateOrgRequest) (*dto.OrgResponse, error)

	// DissolveOrg 软删除 org(标记 status=dissolved + dissolved_at)。
	DissolveOrg(ctx context.Context, orgID uint64) error

	// GetOrgByID 内部工具,按 ID 取 org。
	GetOrgByID(ctx context.Context, orgID uint64) (*model.Org, error)

	// IsMember 查询指定 user 是否是 active org 的成员。
	// 用于 OAuth consent 页的成员资格二次校验。
	IsMember(ctx context.Context, orgID, userID uint64) (bool, error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type orgService struct {
	cfg      Config
	repo     repository.Repository
	logger   logger.LoggerInterface
	verifier UserVerifier // M1.1 邮箱验证 guard;nil 时 CreateOrg 不做前置校验
}

// NewOrgService 构造一个 OrgService 实例。
func NewOrgService(cfg Config, repo repository.Repository, verifier UserVerifier, log logger.LoggerInterface) OrgService {
	return &orgService{cfg: cfg, repo: repo, logger: log, verifier: verifier}
}

// orgSlugRegexp 预编译的 slug 校验正则。
var orgSlugRegexp = regexp.MustCompile(organization.OrgSlugPattern)

// CreateOrg 创建新 org 并把调用者注册为 owner。事务内:写 org + 插 owner member。
//
// 可能的错误:
//   - ErrOrgUserNotVerified:邮箱未验证
//   - ErrOrgSlugInvalid:slug 格式非法
//   - ErrOrgDisplayNameInvalid:display_name 非法
//   - ErrOrgInvalidRequest:description 超长
//   - ErrOrgMaxOwnedReached:超出每用户创建上限
//   - ErrOrgSlugTaken:slug 已被占用
//   - ErrOrgInternal:数据库查询或事务执行失败
func (s *orgService) CreateOrg(ctx context.Context, userID uint64, req dto.CreateOrgRequest) (*dto.OrgResponse, error) {
	// M1.1 前置:邮箱未验证的账号不允许创建 org
	if s.verifier != nil {
		verified, vErr := s.verifier.IsUserVerified(ctx, userID)
		if vErr != nil {
			s.logger.ErrorCtx(ctx, "查询邮箱验证状态失败", vErr, map[string]any{"user_id": userID})
			return nil, fmt.Errorf("check verified: %w: %w", vErr, organization.ErrOrgInternal)
		}
		if !verified {
			s.logger.WarnCtx(ctx, "邮箱未验证,拒绝创建 org", map[string]any{"user_id": userID})
			return nil, fmt.Errorf("email unverified: %w", organization.ErrOrgUserNotVerified)
		}
	}

	// 参数校验
	if !orgSlugRegexp.MatchString(req.Slug) {
		s.logger.WarnCtx(ctx, "slug 格式非法", map[string]any{"user_id": userID, "slug": req.Slug})
		return nil, fmt.Errorf("invalid slug: %w", organization.ErrOrgSlugInvalid)
	}
	if req.DisplayName == "" || len(req.DisplayName) > organization.MaxOrgDisplayNameLength {
		s.logger.WarnCtx(ctx, "display_name 长度非法", map[string]any{"user_id": userID, "len": len(req.DisplayName)})
		return nil, fmt.Errorf("invalid display name: %w", organization.ErrOrgDisplayNameInvalid)
	}
	if len(req.Description) > organization.MaxOrgDescriptionLength {
		s.logger.WarnCtx(ctx, "description 过长", map[string]any{"user_id": userID, "len": len(req.Description)})
		return nil, fmt.Errorf("description too long: %w", organization.ErrOrgInvalidRequest)
	}

	// 创建数量上限
	count, err := s.repo.CountOwnedOrgsByUser(ctx, userID, false)
	if err != nil {
		s.logger.ErrorCtx(ctx, "统计已有 org 失败", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("count owned: %w: %w", err, organization.ErrOrgInternal)
	}
	if int(count) >= s.cfg.MaxOwnedOrgs {
		s.logger.WarnCtx(ctx, "超出创建上限", map[string]any{"user_id": userID, "count": count, "max": s.cfg.MaxOwnedOrgs})
		return nil, fmt.Errorf("max owned reached: %w", organization.ErrOrgMaxOwnedReached)
	}

	// slug 唯一性预检(DB 唯一索引是最终保证,这里先查一次返回友好错误)
	if existing, err := s.repo.FindOrgBySlug(ctx, req.Slug); err == nil && existing != nil {
		s.logger.WarnCtx(ctx, "slug 已被占用", map[string]any{"slug": req.Slug})
		return nil, fmt.Errorf("slug taken: %w", organization.ErrOrgSlugTaken)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 slug 失败", err, map[string]any{"slug": req.Slug})
		return nil, fmt.Errorf("check slug: %w: %w", err, organization.ErrOrgInternal)
	}

	var createdOrg *model.Org

	// 事务内:创建 org + seed 三个系统角色 + owner member(挂 owner 角色)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		now := time.Now().UTC()
		org := &model.Org{
			Slug:        req.Slug,
			DisplayName: req.DisplayName,
			Description: req.Description,
			OwnerUserID: userID,
			Status:      model.OrgStatusActive,
		}
		if createOrgErr := tx.CreateOrg(ctx, org); createOrgErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 org 失败", createOrgErr, map[string]any{"user_id": userID, "slug": req.Slug})
			return fmt.Errorf("tx create org: %w: %w", createOrgErr, organization.ErrOrgInternal)
		}

		systemRoles, seedErr := tx.SeedSystemRolesForOrg(ctx, org.ID)
		if seedErr != nil {
			s.logger.ErrorCtx(ctx, "事务内 seed 系统角色失败", seedErr, map[string]any{"org_id": org.ID, "user_id": userID})
			return fmt.Errorf("tx seed system roles: %w: %w", seedErr, organization.ErrOrgInternal)
		}
		ownerRole, ok := systemRoles[model.SystemRoleSlugOwner]
		if !ok || ownerRole == nil {
			s.logger.ErrorCtx(ctx, "seed 后缺少 owner 角色", nil, map[string]any{"org_id": org.ID})
			return fmt.Errorf("owner role missing after seed: %w", organization.ErrOrgInternal)
		}

		ownerMember := &model.OrgMember{
			OrgID:    org.ID,
			UserID:   userID,
			RoleID:   ownerRole.ID,
			JoinedAt: now,
		}
		if createMemberErr := tx.CreateMember(ctx, ownerMember); createMemberErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 owner member 失败", createMemberErr, map[string]any{"org_id": org.ID, "user_id": userID})
			return fmt.Errorf("tx create owner member: %w: %w", createMemberErr, organization.ErrOrgInternal)
		}

		createdOrg = org
		return nil
	})
	if err != nil {
		if errors.Is(err, organization.ErrOrgInternal) || errors.Is(err, organization.ErrOrgSlugTaken) {
			s.logger.ErrorCtx(ctx, "创建 org 事务失败", err, map[string]any{"user_id": userID, "slug": req.Slug})
		}
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	s.logger.InfoCtx(ctx, "org 创建成功", map[string]any{"user_id": userID, "org_id": createdOrg.ID, "slug": createdOrg.Slug})
	resp := orgToDTO(createdOrg)
	return &resp, nil
}

// GetOrgBySlug 按 slug 查询 org 详情。
//
// 可能的错误:
//   - ErrOrgNotFound:slug 对应的 org 不存在
//   - ErrOrgDissolved:org 已解散
//   - ErrOrgInternal:数据库查询失败
func (s *orgService) GetOrgBySlug(ctx context.Context, slug string) (*dto.OrgResponse, error) {
	org, err := s.repo.FindOrgBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"slug": slug})
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"slug": slug})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.Status == model.OrgStatusDissolved {
		s.logger.WarnCtx(ctx, "org 已解散", map[string]any{"slug": slug, "org_id": org.ID})
		return nil, fmt.Errorf("org dissolved: %w", organization.ErrOrgDissolved)
	}
	resp := orgToDTO(org)
	return &resp, nil
}

// ListOrgsByUser 列出某用户所属的所有 active org。
//
// 可能的错误:
//   - ErrOrgInternal:数据库查询失败
func (s *orgService) ListOrgsByUser(ctx context.Context, userID uint64) ([]dto.OrgWithMembershipResponse, error) {
	rows, err := s.repo.ListOrgsByUser(ctx, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出我的 org 失败", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("list orgs: %w: %w", err, organization.ErrOrgInternal)
	}
	out := make([]dto.OrgWithMembershipResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, dto.OrgWithMembershipResponse{
			Org:      orgToDTO(r.Org),
			JoinedAt: r.Member.JoinedAt.Unix(),
		})
	}
	return out, nil
}

// UpdateOrg 部分更新 org 的 display_name 和 description。
//
// 可能的错误:
//   - ErrOrgDisplayNameInvalid:display_name 非法
//   - ErrOrgInvalidRequest:description 超长
//   - ErrOrgNotFound:org 不存在(loadOrgDTO 返回)
//   - ErrOrgInternal:数据库查询或更新失败
func (s *orgService) UpdateOrg(ctx context.Context, orgID uint64, req dto.UpdateOrgRequest) (*dto.OrgResponse, error) {
	updates := map[string]any{}
	if req.DisplayName != nil {
		if *req.DisplayName == "" || len(*req.DisplayName) > organization.MaxOrgDisplayNameLength {
			s.logger.WarnCtx(ctx, "display_name 非法", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("invalid display name: %w", organization.ErrOrgDisplayNameInvalid)
		}
		updates["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		if len(*req.Description) > organization.MaxOrgDescriptionLength {
			s.logger.WarnCtx(ctx, "description 过长", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("description too long: %w", organization.ErrOrgInvalidRequest)
		}
		updates["description"] = *req.Description
	}
	if len(updates) == 0 {
		//sayso-lint:ignore sentinel-wrap
		return s.loadOrgDTO(ctx, orgID)
	}
	if err := s.repo.UpdateOrgFields(ctx, orgID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "更新 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("update org: %w: %w", err, organization.ErrOrgInternal)
	}
	//sayso-lint:ignore sentinel-wrap
	return s.loadOrgDTO(ctx, orgID)
}

// DissolveOrg 软删除 org(标记 status=dissolved + dissolved_at)。
//
// 可能的错误:
//   - ErrOrgInternal:数据库更新失败
func (s *orgService) DissolveOrg(ctx context.Context, orgID uint64) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":       model.OrgStatusDissolved,
		"dissolved_at": &now,
	}
	if err := s.repo.UpdateOrgFields(ctx, orgID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "解散 org 失败", err, map[string]any{"org_id": orgID})
		return fmt.Errorf("dissolve org: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "org 已解散", map[string]any{"org_id": orgID})
	return nil
}

// CheckSlug 对前端输入的 slug 做一次预检,返回 (available, reason)。
func (s *orgService) CheckSlug(ctx context.Context, slug string) (*dto.CheckSlugResponse, error) {
	if !orgSlugRegexp.MatchString(slug) {
		return &dto.CheckSlugResponse{Available: false, Reason: organization.SlugCheckReasonInvalidFormat}, nil
	}
	existing, err := s.repo.FindOrgBySlug(ctx, slug)
	if err == nil && existing != nil {
		return &dto.CheckSlugResponse{Available: false, Reason: organization.SlugCheckReasonTaken}, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "slug 预检查询失败", err, map[string]any{"slug": slug})
		return nil, fmt.Errorf("check slug: %w: %w", err, organization.ErrOrgInternal)
	}
	return &dto.CheckSlugResponse{Available: true}, nil
}

// GetOrgByID 按 ID 加载原始 model.Org,供中间件和内部代码使用。
//
// 可能的错误:
//   - ErrOrgNotFound:orgID 对应记录不存在
//   - ErrOrgInternal:数据库查询失败
func (s *orgService) GetOrgByID(ctx context.Context, orgID uint64) (*model.Org, error) {
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	return org, nil
}

// IsMember 校验指定 user 是否是 org 的 active 成员。
// orgID 不存在或 org 已解散或 user 不是成员 → (false, nil)。
func (s *orgService) IsMember(ctx context.Context, orgID, userID uint64) (bool, error) {
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		s.logger.ErrorCtx(ctx, "查询 org 失败", err, map[string]any{"org_id": orgID})
		return false, fmt.Errorf("find org: %w: %w", err, organization.ErrOrgInternal)
	}
	if org.Status != model.OrgStatusActive {
		return false, nil
	}
	//sayso-lint:ignore err-swallow
	_, err = s.repo.FindMember(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		s.logger.ErrorCtx(ctx, "查询成员失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return false, fmt.Errorf("find member: %w: %w", err, organization.ErrOrgInternal)
	}
	return true, nil
}

// loadOrgDTO 内部工具:按 ID 重新加载 org 并转成 DTO。
func (s *orgService) loadOrgDTO(ctx context.Context, orgID uint64) (*dto.OrgResponse, error) {
	org, err := s.repo.FindOrgByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "org 不存在", map[string]any{"org_id": orgID})
			return nil, fmt.Errorf("find org: %w", organization.ErrOrgNotFound)
		}
		s.logger.ErrorCtx(ctx, "加载 org 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("load org: %w: %w", err, organization.ErrOrgInternal)
	}
	resp := orgToDTO(org)
	return &resp, nil
}
