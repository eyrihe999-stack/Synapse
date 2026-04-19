// const.go 组织模块常量定义。
package organization

import "time"

// ─── 表名常量 ─────────────────────────────────────────────────────────────────

const (
	// TableOrgs 组织主表
	TableOrgs = "orgs"
	// TableOrgMembers 组织成员表
	TableOrgMembers = "org_members"
	// TableOrgRoles 角色表（预设 + 自定义,per-org 存储）
	TableOrgRoles = "org_roles"
	// TableOrgInvitations 邀请表
	TableOrgInvitations = "org_invitations"
	// TableOrgMemberRoleHistory 成员角色变更审计表
	TableOrgMemberRoleHistory = "org_member_role_history"
)

// ─── 组织状态 ─────────────────────────────────────────────────────────────────

const (
	// OrgStatusActive 组织正常状态
	OrgStatusActive = "active"
	// OrgStatusDissolved 组织已解散(软删除)
	OrgStatusDissolved = "dissolved"
)

// ─── 预设角色名 ───────────────────────────────────────────────────────────────
// 每个 org 创建时自动种 3 行预设角色。
// 自定义角色的 name 由 owner 自己指定,但不能与预设名冲突。

const (
	// RoleOwner 组织创建者,唯一,拥有全部权限
	RoleOwner = "owner"
	// RoleAdmin 管理员,除 owner 独占权限外全部权限
	RoleAdmin = "admin"
	// RoleMember 普通成员,基础使用权限
	RoleMember = "member"
)

// ─── 权限点(15 个) ───────────────────────────────────────────────────────────
// 权限点是字符串常量,存储在 org_roles.permissions 的 JSON 数组里。
// 新增权限点时必须同步更新 AllPermissions 清单和预设角色的默认权限集。

const (
	// ─ 组织管理类(4 个) ─

	// PermOrgUpdate 修改 org 名称/描述等
	PermOrgUpdate = "org.update"
	// PermOrgDelete 解散 org (owner 独占)
	PermOrgDelete = "org.delete"
	// PermOrgTransfer 转让所有权 (owner 独占)
	PermOrgTransfer = "org.transfer"
	// PermOrgSettingsReviewToggle 开关 agent 发布审核/记录 payload 等 org 级设置
	PermOrgSettingsReviewToggle = "org.settings.review_toggle"

	// ─ 成员管理类(4 个) ─

	// PermMemberInvite 邀请新成员/撤销邀请/查询候选人
	PermMemberInvite = "member.invite"
	// PermMemberRemove 踢出成员
	PermMemberRemove = "member.remove"
	// PermMemberRoleAssign 变更成员角色
	PermMemberRoleAssign = "member.role.assign"
	// PermRoleManage 自定义角色 CRUD (owner 独占)
	PermRoleManage = "role.manage"

	// ─ Agent 管理类(6 个) ─
	// 这些权限点定义在 organization 模块,但实际由 agent 模块消费。
	// 原因:权限字符串是 role 的 JSON 数据,归属于 organization 的存储;
	// agent 模块通过 roleSvc.HasPermission 传权限字符串做判断。

	// PermAgentPublish 把自己的 agent 发布到本 org
	PermAgentPublish = "agent.publish"
	// PermAgentUnpublishSelf 取消自己 agent 的发布
	PermAgentUnpublishSelf = "agent.unpublish.self"
	// PermAgentUnpublishAny 强制下架他人已发布的 agent
	PermAgentUnpublishAny = "agent.unpublish.any"
	// PermAgentReview 审核他人 agent 的发布申请
	PermAgentReview = "agent.review"
	// PermAgentBan 封禁某 agent 在本 org 的可用性
	PermAgentBan = "agent.ban"
	// PermAgentInvoke 调用本 org 内已发布的 agent
	PermAgentInvoke = "agent.invoke"

	// ─ 审计类(2 个) ─

	// PermAuditRead 查看本 org 内所有调用审计
	PermAuditRead = "audit.read"
	// PermAuditReadSelf 仅查看"和我相关"的调用审计(调用者=我 或 被调 agent=我发布的)
	PermAuditReadSelf = "audit.read.self"

	// ─ Document 管理类(3 个) ─
	// 权限点集中在 organization,由 document 模块消费(参考 Agent 管理类同样的分层)。

	// PermDocumentRead 在本 org 检索 / 读取文档。
	PermDocumentRead = "document.read"
	// PermDocumentWrite 上传 / 更新文档。
	PermDocumentWrite = "document.write"
	// PermDocumentDelete 删除文档(含他人上传的)。
	PermDocumentDelete = "document.delete"

	// ─ Integration 管理类(1 个) ─
	// 由 integration 模块消费(参考 Agent/Document 同样的分层)。

	// PermIntegrationManage 管理组织级第三方集成应用凭证(目前覆盖飞书 app_id / app_secret,未来 google / slack 同此权限)。
	// 不影响成员自己点"连接账号"走 OAuth —— 那是用户级操作,只需 JWT。
	PermIntegrationManage = "integration.manage"
)

// AllPermissions 列出所有权限点,用于:
//   - 前端展示权限选择面板
//   - 自定义角色创建时校验权限是否合法
//   - owner 预设角色初始化
//
// 新增权限点时必须同步更新此清单。
var AllPermissions = []string{
	PermOrgUpdate,
	PermOrgDelete,
	PermOrgTransfer,
	PermOrgSettingsReviewToggle,
	PermMemberInvite,
	PermMemberRemove,
	PermMemberRoleAssign,
	PermRoleManage,
	PermAgentPublish,
	PermAgentUnpublishSelf,
	PermAgentUnpublishAny,
	PermAgentReview,
	PermAgentBan,
	PermAgentInvoke,
	PermAuditRead,
	PermAuditReadSelf,
	PermDocumentRead,
	PermDocumentWrite,
	PermDocumentDelete,
	PermIntegrationManage,
}

// OwnerOnlyPermissions 是 owner 独占的权限点,自定义角色不能勾选。
// 这些权限点只出现在 owner 预设角色的 permissions JSON 中。
var OwnerOnlyPermissions = []string{
	PermOrgDelete,
	PermOrgTransfer,
	PermRoleManage,
}

// ─── 邀请类型与状态 ───────────────────────────────────────────────────────────

const (
	// InvitationTypeMember 普通成员邀请
	InvitationTypeMember = "member"
	// InvitationTypeOwnershipTransfer 所有权转让邀请(接受后 owner 交接)
	InvitationTypeOwnershipTransfer = "ownership_transfer"
)

const (
	// InvitationStatusPending 待处理
	InvitationStatusPending = "pending"
	// InvitationStatusAccepted 已接受
	InvitationStatusAccepted = "accepted"
	// InvitationStatusRejected 已拒绝
	InvitationStatusRejected = "rejected"
	// InvitationStatusExpired 已过期
	InvitationStatusExpired = "expired"
	// InvitationStatusRevoked 被邀请人以外的撤销(如邀请发起人主动撤销)
	InvitationStatusRevoked = "revoked"
)

// ─── 角色变更历史 reason 枚举 ─────────────────────────────────────────────────

const (
	// RoleChangeReasonJoin 首次加入 org(from_role_id 为 null)
	RoleChangeReasonJoin = "join"
	// RoleChangeReasonRoleAssign 主动分配/变更角色
	RoleChangeReasonRoleAssign = "role_assign"
	// RoleChangeReasonOwnershipTransfer 所有权转让导致的角色变更
	RoleChangeReasonOwnershipTransfer = "ownership_transfer"
	// RoleChangeReasonLeave 离开 org
	RoleChangeReasonLeave = "leave"
)

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultPageSize 列表接口默认分页大小
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小
	MaxPageSize = 100

	// DefaultInvitationExpiresDays 邀请默认过期天数
	DefaultInvitationExpiresDays = 7

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

	// MaxRoleNameLength 角色 name 最大长度
	MaxRoleNameLength = 32
	// MaxRoleDisplayNameLength 角色 display_name 最大长度
	MaxRoleDisplayNameLength = 64

	// MaxInviteeCandidates 昵称/手机号/邮箱查找候选人返回上限
	MaxInviteeCandidates = 20
)

// OrgSlugPattern 组织 slug 正则:小写字母、数字、连字符,必须字母开头,3-32 字符。
// 示例合法:"acme-corp"、"team-42"、"my-startup"。
// 示例非法:"ACME"(大写)、"1-team"(数字开头)、"my_team"(下划线)。
const OrgSlugPattern = `^[a-z][a-z0-9-]{2,31}$`

// InvitationExpireJobInterval 邀请过期定时任务执行间隔(每天一次)
const InvitationExpireJobInterval = 24 * time.Hour
