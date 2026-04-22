// service.go 组织模块 service 层的共享类型、配置与转换工具。
package service

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	userpkg "github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// UserVerifier M1.1 跨模块前置 guard。
// main.go 用 user service 的 IsUserVerified 作为实现注入;
// nil 时 org service 跳过检查(单测/旧部署兼容)。
type UserVerifier interface {
	IsUserVerified(ctx context.Context, userID uint64) (bool, error)
}

// UserLookup 跨模块读依赖:邀请流程需要 inviter 的 display_name(邮件文案)
// 和 accepting user 的 email(Accept 时校验 email 匹配)。
//
// main.go 从 user service 装适配器注入;nil 时 InvitationService 不可用。
type UserLookup interface {
	// LookupUser 按 user_id 返回邀请邮件/校验所需的用户信息。
	// 用户不存在 / 已删 / banned 时返 sentinel error(service 层按需翻译)。
	LookupUser(ctx context.Context, userID uint64) (*InviteUserInfo, error)
}

// InviteUserInfo 邀请流程需要的用户侧摘要信息。
type InviteUserInfo struct {
	Email       string
	DisplayName string // 空串 → 邮件模板里用 email 前缀兜底
	Locale      string // "zh" / "en";其他值视作 en
}

// UserLookupFunc 把一个 func 适配为 UserLookup 接口,main.go 用 closure 注入时省去建 struct。
type UserLookupFunc func(ctx context.Context, userID uint64) (*InviteUserInfo, error)

// LookupUser 让 UserLookupFunc 满足 UserLookup 接口。
func (f UserLookupFunc) LookupUser(ctx context.Context, userID uint64) (*InviteUserInfo, error) {
	return f(ctx, userID)
}

// OwnerCheckerAdapter M3.7 反向跨模块依赖:user 注销流程要查"此用户是否为某 org 的 owner"。
type OwnerCheckerAdapter struct {
	repo   repository.Repository
	logger logger.LoggerInterface
}

// NewOwnerCheckerAdapter 构造 user 模块 OwnerChecker 的适配器。
func NewOwnerCheckerAdapter(repo repository.Repository, log logger.LoggerInterface) *OwnerCheckerAdapter {
	return &OwnerCheckerAdapter{repo: repo, logger: log}
}

// ListActiveOrgsOwnedBy 返回该 user 持有的 active org 摘要(slug + display_name),
// 供 user.DeleteAccount 决定是否阻塞注销。
//
// 可能的错误:
//   - ErrOrgInternal:repo 查询失败
func (a *OwnerCheckerAdapter) ListActiveOrgsOwnedBy(ctx context.Context, userID uint64) ([]userpkg.OwnedOrgSummary, error) {
	orgs, err := a.repo.ListActiveOrgsOwnedBy(ctx, userID)
	if err != nil {
		a.logger.ErrorCtx(ctx, "查询用户持有的 active org 失败", err, map[string]any{"user_id": userID})
		return nil, fmt.Errorf("list active orgs owned by: %w: %w", err, organization.ErrOrgInternal)
	}
	out := make([]userpkg.OwnedOrgSummary, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, userpkg.OwnedOrgSummary{
			Slug:        o.Slug,
			DisplayName: o.DisplayName,
		})
	}
	return out, nil
}

// Config service 层需要的配置项,从应用配置中装填。
type Config struct {
	// MaxOwnedOrgs 每用户最多创建的 org 数
	MaxOwnedOrgs int
	// MaxJoinedOrgs 每用户最多加入的 org 数
	MaxJoinedOrgs int
}

// DefaultConfig 返回默认配置值,用于测试或主流程缺省回退。
func DefaultConfig() Config {
	return Config{
		MaxOwnedOrgs:  organization.DefaultMaxOwnedOrgs,
		MaxJoinedOrgs: organization.DefaultMaxJoinedOrgs,
	}
}

// ─── model → dto 转换 ────────────────────────────────────────────────────────

// orgToDTO 将 Org 模型转为 OrgResponse。
func orgToDTO(m *model.Org) dto.OrgResponse {
	return dto.OrgResponse{
		ID:          m.ID,
		Slug:        m.Slug,
		DisplayName: m.DisplayName,
		Description: m.Description,
		OwnerUserID: m.OwnerUserID,
		Status:      m.Status,
		CreatedAt:   m.CreatedAt.Unix(),
		UpdatedAt:   m.UpdatedAt.Unix(),
	}
}

// memberWithProfileToDTO 把 MemberWithProfile(member + users + org_roles JOIN 出的展示字段)
// 转为 MemberResponse。role 信息在 Role 字段里;若 JOIN 记录缺失(极端情况)角色字段退化为 zero 值。
// EmailVerifiedAt / LastLoginAt 用指针 —— 为 nil 时序列化为 null,前端区分"未验证/从未登录"与
// "已验证/已登录"。
func memberWithProfileToDTO(mp *repository.MemberWithProfile) dto.MemberResponse {
	resp := dto.MemberResponse{
		UserID:      mp.Member.UserID,
		Email:       mp.Email,
		DisplayName: mp.DisplayName,
		AvatarURL:   mp.AvatarURL,
		Status:      mp.Status,
		JoinedAt:    mp.Member.JoinedAt.Unix(),
		Role: dto.RoleSummary{
			Slug:        mp.RoleSlug,
			DisplayName: mp.RoleDisplayName,
			IsSystem:    mp.RoleIsSystem,
		},
	}
	if mp.EmailVerifiedAt != nil {
		ts := mp.EmailVerifiedAt.Unix()
		resp.EmailVerifiedAt = &ts
	}
	if mp.LastLoginAt != nil {
		ts := mp.LastLoginAt.Unix()
		resp.LastLoginAt = &ts
	}
	return resp
}

// roleToDTO 把 OrgRole 模型转为 RoleResponse。
func roleToDTO(m *model.OrgRole) dto.RoleResponse {
	perms := []string(m.Permissions)
	if perms == nil {
		perms = []string{}
	}
	return dto.RoleResponse{
		Slug:        m.Slug,
		DisplayName: m.DisplayName,
		IsSystem:    m.IsSystem,
		Permissions: perms,
		CreatedAt:   m.CreatedAt.Unix(),
		UpdatedAt:   m.UpdatedAt.Unix(),
	}
}

// invitationWithRoleToDTO 把 InvitationWithRole(邀请 + JOIN 出的角色字段)转为 InvitationResponse。
func invitationWithRoleToDTO(iw *repository.InvitationWithRole) dto.InvitationResponse {
	inv := iw.Invitation
	var acceptedAt int64
	if inv.AcceptedAt != nil {
		acceptedAt = inv.AcceptedAt.Unix()
	}
	return dto.InvitationResponse{
		ID:             inv.ID,
		Email:          inv.Email,
		Role:           dto.RoleSummary{Slug: iw.RoleSlug, DisplayName: iw.RoleDisplayName, IsSystem: iw.RoleIsSystem},
		Status:         inv.Status,
		InviterUserID:  inv.InviterUserID,
		ExpiresAt:      inv.ExpiresAt.Unix(),
		AcceptedAt:     acceptedAt,
		AcceptedUserID: inv.AcceptedUserID,
		CreatedAt:      inv.CreatedAt.Unix(),
		UpdatedAt:      inv.UpdatedAt.Unix(),
	}
}
