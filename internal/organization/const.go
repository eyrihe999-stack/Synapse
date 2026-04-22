// const.go 组织模块常量定义。
package organization

import "time"

// ─── 表名常量 ─────────────────────────────────────────────────────────────────

const (
	// TableOrgs 组织主表
	TableOrgs = "orgs"
	// TableOrgRoles 组织角色表
	TableOrgRoles = "org_roles"
	// TableOrgMembers 组织成员表
	TableOrgMembers = "org_members"
	// TableOrgInvitations 组织邀请表
	TableOrgInvitations = "org_invitations"
)

// ─── 组织状态 ─────────────────────────────────────────────────────────────────

const (
	// OrgStatusActive 组织正常状态
	OrgStatusActive = "active"
	// OrgStatusDissolved 组织已解散(软删除)
	OrgStatusDissolved = "dissolved"
)

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultPageSize 列表接口默认分页大小
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小
	MaxPageSize = 100

	// DefaultMaxOwnedOrgs 每用户最多创建 org 数
	DefaultMaxOwnedOrgs = 5
	// DefaultMaxJoinedOrgs 每用户最多加入 org 数
	DefaultMaxJoinedOrgs = 20

	// MinOrgSlugLength slug 最小长度
	MinOrgSlugLength = 3
	// MaxOrgSlugLength slug 最大长度
	MaxOrgSlugLength = 32
	// MaxOrgDisplayNameLength display_name 最大长度
	MaxOrgDisplayNameLength = 64
	// MaxOrgDescriptionLength 描述最大长度
	MaxOrgDescriptionLength = 500
)

// OrgSlugPattern 组织 slug 正则:小写字母、数字、连字符,必须字母开头,3-32 字符。
const OrgSlugPattern = `^[a-z][a-z0-9-]{2,31}$`

// Slug 预检 reason 枚举,供 check-slug 接口返回。
const (
	SlugCheckReasonInvalidFormat = "invalid_format"
	SlugCheckReasonTaken         = "taken"
)

// ─── 角色相关常量 ─────────────────────────────────────────────────────────────

const (
	// SystemRoleSlugOwner 系统角色:所有者(每 org 唯一,创建/转让时挂在 Org.OwnerUserID 对应的 member 上)
	SystemRoleSlugOwner = "owner"
	// SystemRoleSlugAdmin 系统角色:管理员
	SystemRoleSlugAdmin = "admin"
	// SystemRoleSlugMember 系统角色:普通成员
	SystemRoleSlugMember = "member"

	// RoleSlugPattern 角色 slug 正则:小写字母、数字、连字符,必须字母开头,2-32 字符
	RoleSlugPattern = `^[a-z][a-z0-9-]{1,31}$`

	// MinRoleSlugLength 角色 slug 最小长度
	MinRoleSlugLength = 2
	// MaxRoleSlugLength 角色 slug 最大长度
	MaxRoleSlugLength = 32
	// MaxRoleDisplayNameLength 角色 display_name 最大长度
	MaxRoleDisplayNameLength = 64

	// MaxCustomRolesPerOrg 每 org 最多允许的自定义角色数量(不含系统角色)
	MaxCustomRolesPerOrg = 20
)

// SystemRoleDefaults 是每个 org 创建时需要 seed 的三条系统角色定义(slug → 默认 display_name)。
// 顺序固定,migration 和 CreateOrg 事务复用此常量。
var SystemRoleDefaults = []struct {
	Slug        string
	DisplayName string
}{
	{Slug: SystemRoleSlugOwner, DisplayName: "Owner"},
	{Slug: SystemRoleSlugAdmin, DisplayName: "Admin"},
	{Slug: SystemRoleSlugMember, DisplayName: "Member"},
}

// IsSystemRoleSlug 判断一个 slug 是否是保留的系统角色 slug。
// 创建自定义角色时拒绝这三个 slug。
func IsSystemRoleSlug(slug string) bool {
	switch slug {
	case SystemRoleSlugOwner, SystemRoleSlugAdmin, SystemRoleSlugMember:
		return true
	}
	return false
}

// ─── 邀请相关常量 ─────────────────────────────────────────────────────────────

const (
	// DefaultInvitationTTL 默认邀请有效期(7 天),Create 时用于计算 ExpiresAt。
	// 写死不放 config:7 天是大多数 SaaS 的惯例,用户调整收益小且需求未出现前不做配置项。
	DefaultInvitationTTL = 7 * 24 * time.Hour

	// InvitationTokenRandomBytes 生成 token 的原始字节数。32 字节 base64url 后 ~43 字符。
	InvitationTokenRandomBytes = 32
)
