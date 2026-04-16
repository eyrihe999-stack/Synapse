// org_service.go 组织本体的 CRUD / 设置 / 转让 / 解散 service。
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
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// OrgService 定义组织本体的业务操作。
//sayso-lint:ignore interface-pollution
type OrgService interface {
	// CreateOrg 创建一个 org 并把调用者设为 owner。
	// 事务内插入:org / 3 条预设角色 / owner member / role_history
	CreateOrg(ctx context.Context, userID uint64, req dto.CreateOrgRequest) (*dto.OrgResponse, error)

	// GetOrgBySlug 查 org 详情。调用方应该先确认 user 是成员。
	GetOrgBySlug(ctx context.Context, slug string) (*dto.OrgResponse, error)

	// ListOrgsByUser 列出某用户所属的所有 org(含当前用户在其中的角色)。
	ListOrgsByUser(ctx context.Context, userID uint64) ([]dto.OrgWithMyRoleResponse, error)

	// UpdateOrg 更新 org 基础信息(display_name / description)。需要 PermOrgUpdate。
	UpdateOrg(ctx context.Context, orgID uint64, req dto.UpdateOrgRequest) (*dto.OrgResponse, error)

	// UpdateSettings 更新 org 设置(require_agent_review / record_full_payload)。
	// 需要 PermOrgSettingsReviewToggle。
	UpdateSettings(ctx context.Context, orgID uint64, req dto.UpdateOrgSettingsRequest) (*dto.OrgResponse, error)

	// DissolveOrg 解散 org(owner 独占)。主事务提交后触发 OnOrgDissolved hooks。
	DissolveOrg(ctx context.Context, orgID uint64) error

	// GetOrgByID 内部工具,按 ID 取 org。
	GetOrgByID(ctx context.Context, orgID uint64) (*model.Org, error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type orgService struct {
	cfg    Config
	repo   repository.Repository
	hooks  *HookRegistry
	logger logger.LoggerInterface
}

// NewOrgService 构造一个 OrgService 实例。
// hooks 可以为 nil(测试时),生产环境必须传入以支持解散时的跨模块联动。
func NewOrgService(cfg Config, repo repository.Repository, hooks *HookRegistry, log logger.LoggerInterface) OrgService {
	return &orgService{cfg: cfg, repo: repo, hooks: hooks, logger: log}
}

// orgSlugRegexp 预编译的 slug 校验正则。
var orgSlugRegexp = regexp.MustCompile(organization.OrgSlugPattern)

// CreateOrg 创建新 org 并把调用者注册为 owner。事务内完成:写 org、种 3 条预设角色、插 owner member、写角色历史。
// 可能返回:ErrOrgSlugInvalid / ErrOrgDisplayNameInvalid / ErrOrgMaxOwnedReached / ErrOrgSlugTaken / ErrOrgInternal
func (s *orgService) CreateOrg(ctx context.Context, userID uint64, req dto.CreateOrgRequest) (*dto.OrgResponse, error) {
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

	// 事务内:创建 org + 3 条预设角色 + owner member + role_history
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		now := time.Now().UTC()
		org := &model.Org{
			Slug:               req.Slug,
			DisplayName:        req.DisplayName,
			Description:        req.Description,
			OwnerUserID:        userID,
			Status:             model.OrgStatusActive,
			RequireAgentReview: false,
			RecordFullPayload:  false,
		}
		if createOrgErr := tx.CreateOrg(ctx, org); createOrgErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 org 失败", createOrgErr, map[string]any{"user_id": userID, "slug": req.Slug})
			return fmt.Errorf("tx create org: %w: %w", createOrgErr, organization.ErrOrgInternal)
		}

		// 种入 3 条预设角色,设置 org_id
		presetRoles := BuildPresetRoles()
		for _, r := range presetRoles {
			r.OrgID = org.ID
		}
		if createRoleErr := tx.CreateRolesBatch(ctx, presetRoles); createRoleErr != nil {
			s.logger.ErrorCtx(ctx, "事务内种预设角色失败", createRoleErr, map[string]any{"org_id": org.ID})
			return fmt.Errorf("tx create preset roles: %w: %w", createRoleErr, organization.ErrOrgInternal)
		}

		// 找到 owner 角色 ID
		var ownerRoleID uint64
		for _, r := range presetRoles {
			if r.Name == organization.RoleOwner {
				ownerRoleID = r.ID
				break
			}
		}
		if ownerRoleID == 0 {
			s.logger.ErrorCtx(ctx, "预设 owner 角色 ID 为 0", nil, map[string]any{"org_id": org.ID})
			return fmt.Errorf("preset owner role missing: %w", organization.ErrOrgInternal)
		}

		// 创建 owner member
		ownerMember := &model.OrgMember{
			OrgID:    org.ID,
			UserID:   userID,
			RoleID:   ownerRoleID,
			JoinedAt: now,
		}
		if createMemberErr := tx.CreateMember(ctx, ownerMember); createMemberErr != nil {
			s.logger.ErrorCtx(ctx, "事务内创建 owner member 失败", createMemberErr, map[string]any{"org_id": org.ID, "user_id": userID})
			return fmt.Errorf("tx create owner member: %w: %w", createMemberErr, organization.ErrOrgInternal)
		}

		// 角色历史:首次加入
		if historyErr := tx.AppendRoleHistory(ctx, &model.OrgMemberRoleHistory{
			OrgID:           org.ID,
			UserID:          userID,
			FromRoleID:      nil,
			ToRoleID:        ownerRoleID,
			ChangedByUserID: userID,
			Reason:          organization.RoleChangeReasonJoin,
		}); historyErr != nil {
			s.logger.ErrorCtx(ctx, "事务内写首次加入历史失败", historyErr, map[string]any{"org_id": org.ID})
			return fmt.Errorf("tx append role history: %w: %w", historyErr, organization.ErrOrgInternal)
		}

		createdOrg = org
		return nil
	})
	if err != nil {
		// 事务内的日志已打,这里只返回
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

// GetOrgBySlug 按 slug 查询 org 详情。已解散的 org 返回 ErrOrgDissolved,不存在返回 ErrOrgNotFound。
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

// ListOrgsByUser 列出某用户所属的所有 active org,并附带其角色快照。查询失败返回 ErrOrgInternal。
func (s *orgService) ListOrgsByUser(ctx context.Context, userID uint64) ([]dto.OrgWithMyRoleResponse, error) {
	rows, err := s.repo.ListOrgsByUser(ctx, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列出我的 org 失败", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("list orgs: %w: %w", err, organization.ErrOrgInternal)
	}
	out := make([]dto.OrgWithMyRoleResponse, 0, len(rows))
	for _, r := range rows {
		item := dto.OrgWithMyRoleResponse{
			Org:      orgToDTO(r.Org),
			JoinedAt: r.Member.JoinedAt.Unix(),
		}
		if r.Role != nil {
			item.MyRole = roleToSummary(r.Role)
		}
		out = append(out, item)
	}
	return out, nil
}

// UpdateOrg 部分更新 org 的 display_name 和 description,权限判断由 handler 中间件前置完成。
// 可能返回:ErrOrgDisplayNameInvalid / ErrOrgInvalidRequest / ErrOrgNotFound / ErrOrgInternal
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

// UpdateSettings 更新 org 运行时开关(require_agent_review、record_full_payload)。
// 未提供任何字段时返回当前 org 的 DTO。数据库失败返回 ErrOrgInternal。
func (s *orgService) UpdateSettings(ctx context.Context, orgID uint64, req dto.UpdateOrgSettingsRequest) (*dto.OrgResponse, error) {
	updates := map[string]any{}
	if req.RequireAgentReview != nil {
		updates["require_agent_review"] = *req.RequireAgentReview
	}
	if req.RecordFullPayload != nil {
		updates["record_full_payload"] = *req.RecordFullPayload
	}
	if len(updates) == 0 {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return s.loadOrgDTO(ctx, orgID)
	}
	if err := s.repo.UpdateOrgFields(ctx, orgID, updates); err != nil {
		s.logger.ErrorCtx(ctx, "更新 org 设置失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("update settings: %w: %w", err, organization.ErrOrgInternal)
	}
	s.logger.InfoCtx(ctx, "org 设置已更新", map[string]any{"org_id": orgID, "updates": len(updates)})
	//sayso-lint:ignore sentinel-wrap
	return s.loadOrgDTO(ctx, orgID)
}

// DissolveOrg 软删除 org(标记 status=dissolved 并记录时间),解散后触发 OnOrgDissolved hook。
// 权限校验由 handler 层完成。数据库失败返回 ErrOrgInternal。
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

	// 事后触发跨模块 hook(失败只记日志,不回滚)
	if s.hooks != nil {
		s.hooks.FireOrgDissolved(ctx, orgID)
	}
	return nil
}

// GetOrgByID 按 ID 加载原始 model.Org,供中间件和内部代码使用。
// 不存在返回 ErrOrgNotFound,数据库失败返回 ErrOrgInternal。
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

// loadOrgDTO 内部工具:按 ID 重新加载 org 并转成 DTO,用于 UpdateOrg / UpdateSettings 返回最新状态。
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
