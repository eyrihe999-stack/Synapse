// service.go 组织模块 service 层的共享类型、配置与转换工具。
//
// 本文件不定义具体的业务逻辑接口,而是提供:
//   - Config: service 层所需的配置项(从 config.yaml 装填)
//   - model → dto 的转换函数
//   - 权限集合的 JSON 序列化/反序列化工具
//   - 角色预设集合的构造函数(OrgService.CreateOrg 使用)
//
// 具体的业务接口定义在 org_service.go / member_service.go / invitation_service.go /
// role_service.go 中。
package service

import (
	"encoding/json"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/organization/dto"
	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	"gorm.io/datatypes"
)

// Config service 层需要的配置项,从应用配置中装填。
type Config struct {
	// MaxOwnedOrgs 每用户最多创建的 org 数
	MaxOwnedOrgs int
	// MaxJoinedOrgs 每用户最多加入的 org 数
	MaxJoinedOrgs int
	// InvitationExpiresDays 邀请默认过期天数
	InvitationExpiresDays int
}

// DefaultConfig 返回默认配置值,用于测试或主流程缺省回退。
func DefaultConfig() Config {
	return Config{
		MaxOwnedOrgs:          organization.DefaultMaxOwnedOrgs,
		MaxJoinedOrgs:         organization.DefaultMaxJoinedOrgs,
		InvitationExpiresDays: organization.DefaultInvitationExpiresDays,
	}
}

// ─── 预设角色构造 ─────────────────────────────────────────────────────────────

// BuildPresetRoles 返回新 org 需要种入的 3 条预设角色记录(owner / admin / member)。
// 调用方需设置 OrgID 并在同一个事务内插入。
//
// 权限分配规则:
//   - owner:拥有 AllPermissions 的全部内容(含 owner 独占)
//   - admin:拥有 AllPermissions 减去 OwnerOnlyPermissions
//   - member:基础权限:publish / unpublish.self / invoke / audit.read.self
func BuildPresetRoles() []*model.OrgRole {
	// 使用 mustMarshalPermissions:输入是编译期常量,Marshal 不可能失败;
	// 若失败意味着代码有 bug,panic 是正确行为(fail-fast)。
	ownerPerms := mustMarshalPermissions(organization.AllPermissions)
	adminPerms := mustMarshalPermissions(subtract(organization.AllPermissions, organization.OwnerOnlyPermissions))
	memberPerms := mustMarshalPermissions([]string{
		organization.PermAgentPublish,
		organization.PermAgentUnpublishSelf,
		organization.PermAgentInvoke,
		organization.PermAuditReadSelf,
	})

	return []*model.OrgRole{
		{
			Name:        organization.RoleOwner,
			DisplayName: "所有者",
			IsPreset:    true,
			Permissions: ownerPerms,
		},
		{
			Name:        organization.RoleAdmin,
			DisplayName: "管理员",
			IsPreset:    true,
			Permissions: adminPerms,
		},
		{
			Name:        organization.RoleMember,
			DisplayName: "成员",
			IsPreset:    true,
			Permissions: memberPerms,
		},
	}
}

// marshalPermissions 把 []string 权限列表序列化为 JSON(供 GORM datatypes.JSON 字段使用)。
// 纯工具函数无 logger,调用方会记录失败上下文。
func marshalPermissions(perms []string) (datatypes.JSON, error) {
	if perms == nil {
		perms = []string{}
	}
	b, err := json.Marshal(perms)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("marshal permissions: %w", err)
	}
	return datatypes.JSON(b), nil
}

// mustMarshalPermissions 是 marshalPermissions 的不可失败变体,用于内部常量数据。
// 若 Marshal 失败说明代码有 bug,panic 是正确行为。
func mustMarshalPermissions(perms []string) datatypes.JSON {
	data, err := marshalPermissions(perms)
	if err != nil {
		//sayso-lint:ignore fatal-panic
		panic(fmt.Sprintf("mustMarshalPermissions: %v", err))
	}
	return data
}

// unmarshalPermissions 把 JSON 权限列表反序列化为 []string。
// 空或 null 返回空切片。
func unmarshalPermissions(data datatypes.JSON) []string {
	if len(data) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return []string{}
	}
	return out
}

// subtract 返回 a 中不在 b 里的元素(保持 a 的顺序)。
func subtract(a, b []string) []string {
	bs := make(map[string]struct{}, len(b))
	for _, x := range b {
		bs[x] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if _, ok := bs[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}

// ─── model → dto 转换 ────────────────────────────────────────────────────────

// orgToDTO 将 Org 模型转为 OrgResponse。
func orgToDTO(m *model.Org) dto.OrgResponse {
	return dto.OrgResponse{
		ID:                 m.ID,
		Slug:               m.Slug,
		DisplayName:        m.DisplayName,
		Description:        m.Description,
		OwnerUserID:        m.OwnerUserID,
		Status:             m.Status,
		RequireAgentReview: m.RequireAgentReview,
		RecordFullPayload:  m.RecordFullPayload,
		CreatedAt:          m.CreatedAt.Unix(),
		UpdatedAt:          m.UpdatedAt.Unix(),
	}
}

// roleToSummary 角色 → RoleSummary(列表场景)。
func roleToSummary(m *model.OrgRole) dto.RoleSummary {
	return dto.RoleSummary{
		ID:          m.ID,
		Name:        m.Name,
		DisplayName: m.DisplayName,
		IsPreset:    m.IsPreset,
		Permissions: unmarshalPermissions(m.Permissions),
	}
}

// roleToDTO 角色 → 完整 RoleResponse。
func roleToDTO(m *model.OrgRole) dto.RoleResponse {
	return dto.RoleResponse{
		ID:          m.ID,
		OrgID:       m.OrgID,
		Name:        m.Name,
		DisplayName: m.DisplayName,
		IsPreset:    m.IsPreset,
		Permissions: unmarshalPermissions(m.Permissions),
		CreatedAt:   m.CreatedAt.Unix(),
		UpdatedAt:   m.UpdatedAt.Unix(),
	}
}

// memberToDTO 组合 member + role + 用户 profile 为 MemberResponse。
// profile 可为 nil(当用户信息查询失败时),此时 display_name 和 avatar_url 为空。
func memberToDTO(m *model.OrgMember, role *model.OrgRole, profile *repository.UserProfile) dto.MemberResponse {
	resp := dto.MemberResponse{
		UserID:   m.UserID,
		JoinedAt: m.JoinedAt.Unix(),
	}
	if role != nil {
		resp.Role = roleToSummary(role)
	}
	if profile != nil {
		resp.DisplayName = profile.DisplayName
		resp.AvatarURL = profile.AvatarURL
	}
	return resp
}

// userProfileToCandidate 把 UserProfile 转成 InviteeCandidate。
func userProfileToCandidate(p *repository.UserProfile) dto.InviteeCandidate {
	return dto.InviteeCandidate{
		UserID:      p.UserID,
		DisplayName: p.DisplayName,
		AvatarURL:   p.AvatarURL,
		MaskedEmail: p.MaskedEmail,
	}
}
