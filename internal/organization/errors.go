// errors.go 组织模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/403/404/409/429/500)
//   - SS:模块号 11 = organization
//   - CCCC:业务码
package organization

import "errors"

// ─── 400 段:请求/业务校验错误 ────────────────────────────────────────────────

const (
	// CodeOrgInvalidRequest 请求参数无效
	CodeOrgInvalidRequest = 400110010
	// CodeOrgSlugInvalid slug 格式不合法
	CodeOrgSlugInvalid = 400110011
	// CodeOrgDisplayNameInvalid display_name 长度不合法
	CodeOrgDisplayNameInvalid = 400110012

	// CodeOrgMaxOwnedReached 超出每用户最大创建 org 数
	CodeOrgMaxOwnedReached = 400110020
	// CodeOrgMaxJoinedReached 超出每用户最大加入 org 数
	CodeOrgMaxJoinedReached = 400110021

	// CodeOwnerCannotLeave owner 不能主动退出 org
	CodeOwnerCannotLeave = 400110050
	// CodeMemberRemoveOwner 不能踢出 owner
	CodeMemberRemoveOwner = 400110053

	// ─ 角色相关 400 ─

	// CodeRoleSlugInvalid 角色 slug 格式非法
	CodeRoleSlugInvalid = 400110060
	// CodeRoleSlugReserved 角色 slug 占用了系统保留 slug
	CodeRoleSlugReserved = 400110061
	// CodeRoleDisplayNameInvalid 角色 display_name 非法(为空或超长)
	CodeRoleDisplayNameInvalid = 400110062
	// CodeRoleIsSystem 对系统角色执行了不允许的操作(改 display_name 除外 —— 系统角色 display_name 也锁死)
	CodeRoleIsSystem = 400110063
	// CodeRoleHasMembers 角色下仍有成员,不允许删除(调用方先迁移再删)
	CodeRoleHasMembers = 400110064
	// CodeMaxCustomRolesReached 超出每 org 自定义角色上限
	CodeMaxCustomRolesReached = 400110065
	// CodeCannotAssignOwnerRole 不允许通过改角色接口给成员分配 owner 角色(转让走独立接口)
	CodeCannotAssignOwnerRole = 400110066
	// CodeCannotChangeOwnerRole 不允许修改 owner member 的角色(owner 角色和 Org.OwnerUserID 绑定)
	CodeCannotChangeOwnerRole = 400110067

	// CodeRolePermissionInvalid 提供的 permissions 列表里有未知 perm
	CodeRolePermissionInvalid = 400110068
	// CodeRolePermissionCeilingExceeded 提供的 permissions 超出 caller 自身权限上限
	CodeRolePermissionCeilingExceeded = 400110069

	// ─ 邀请相关 400 ─

	// CodeInvitationEmailInvalid 邀请目标 email 格式非法
	CodeInvitationEmailInvalid = 400110070
	// CodeInvitationEmailAlreadyMember 邀请目标 email 已是该 org 的成员
	CodeInvitationEmailAlreadyMember = 400110071
	// CodeInvitationDuplicatePending 同 org 同 email 已有一条 pending 邀请
	CodeInvitationDuplicatePending = 400110072
	// CodeInvitationCannotInviteOwner 不能通过邀请创建 owner 角色成员
	CodeInvitationCannotInviteOwner = 400110073
	// CodeInvitationNotPending 目标邀请已不在 pending 状态(accepted/revoked/expired)
	CodeInvitationNotPending = 400110074
	// CodeInvitationExpired 邀请已过期
	CodeInvitationExpired = 400110075
	// CodeInvitationEmailMismatch 登录用户 email 与邀请目标 email 不一致
	CodeInvitationEmailMismatch = 400110076
	// CodeInvitationTokenInvalid token 格式非法或查无此邀请
	CodeInvitationTokenInvalid = 400110077
	// CodeInvitationSearchInvalid 搜索参数非法(type 未知、query 格式不符 type 等)
	CodeInvitationSearchInvalid = 400110078
)

// ─── 429 段:限流 ─────────────────────────────────────────────────────────────

const (
	// CodeInvitationRateLimited 邀请邮件发送触及限流(cooldown / daily cap 任一命中)
	CodeInvitationRateLimited = 429110010
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeOrgNotMember 不是该 org 的成员
	CodeOrgNotMember = 403110011
	// CodeOrgUserNotVerified 调用方邮箱未验证,不允许创建 org
	CodeOrgUserNotVerified = 403110013
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeOrgNotFound org 不存在
	CodeOrgNotFound = 404110020
	// CodeOrgDissolved org 已解散
	CodeOrgDissolved = 404110021
	// CodeMemberNotFound 成员不存在
	CodeMemberNotFound = 404110023
	// CodeRoleNotFound 角色不存在
	CodeRoleNotFound = 404110024
	// CodeInvitationNotFound 邀请不存在(id 或 token 无匹配)
	CodeInvitationNotFound = 404110025
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeOrgSlugTaken slug 已被占用
	CodeOrgSlugTaken = 409110010
	// CodeRoleSlugTaken 角色 slug 在该 org 内已被占用
	CodeRoleSlugTaken = 409110011
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeOrgInternal 内部错误
	CodeOrgInternal = 500110000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ─ 400 段 ─

	// ErrOrgInvalidRequest 请求参数无效
	ErrOrgInvalidRequest = errors.New("organization: invalid request")
	// ErrOrgSlugInvalid slug 格式不合法
	ErrOrgSlugInvalid = errors.New("organization: slug invalid")
	// ErrOrgDisplayNameInvalid display_name 长度不合法
	ErrOrgDisplayNameInvalid = errors.New("organization: display name invalid")

	// ErrOrgMaxOwnedReached 超出创建上限
	ErrOrgMaxOwnedReached = errors.New("organization: max owned orgs reached")
	// ErrOrgMaxJoinedReached 超出加入上限
	ErrOrgMaxJoinedReached = errors.New("organization: max joined orgs reached")

	// ErrOwnerCannotLeave owner 不能主动退出
	ErrOwnerCannotLeave = errors.New("organization: owner cannot leave")
	// ErrMemberRemoveOwner 不能踢出 owner
	ErrMemberRemoveOwner = errors.New("organization: cannot remove owner")

	// ─ 403 段 ─

	// ErrOrgNotMember 非成员
	ErrOrgNotMember = errors.New("organization: not a member")
	// ErrOrgUserNotVerified 调用方邮箱未验证,不允许创建 org
	ErrOrgUserNotVerified = errors.New("organization: user email not verified")

	// ─ 404 段 ─

	// ErrOrgNotFound org 不存在
	ErrOrgNotFound = errors.New("organization: org not found")
	// ErrOrgDissolved org 已解散
	ErrOrgDissolved = errors.New("organization: org dissolved")
	// ErrMemberNotFound 成员不存在
	ErrMemberNotFound = errors.New("organization: member not found")

	// ─ 409 段 ─

	// ErrOrgSlugTaken slug 已被占用
	ErrOrgSlugTaken = errors.New("organization: slug taken")

	// ─ 409 段:角色 ─

	// ErrRoleSlugTaken 角色 slug 在该 org 内已被占用
	ErrRoleSlugTaken = errors.New("organization: role slug taken")

	// ─ 角色 400 段 ─

	// ErrRoleSlugInvalid 角色 slug 格式非法
	ErrRoleSlugInvalid = errors.New("organization: role slug invalid")
	// ErrRoleSlugReserved 角色 slug 与系统保留 slug 冲突
	ErrRoleSlugReserved = errors.New("organization: role slug reserved")
	// ErrRoleDisplayNameInvalid 角色 display_name 非法
	ErrRoleDisplayNameInvalid = errors.New("organization: role display name invalid")
	// ErrRoleIsSystem 该角色是系统角色,不允许执行该操作
	ErrRoleIsSystem = errors.New("organization: role is system")
	// ErrRoleHasMembers 角色下仍有成员,不允许删除
	ErrRoleHasMembers = errors.New("organization: role has members")
	// ErrMaxCustomRolesReached 超出每 org 自定义角色上限
	ErrMaxCustomRolesReached = errors.New("organization: max custom roles reached")
	// ErrCannotAssignOwnerRole 不允许通过改角色接口分配 owner 角色
	ErrCannotAssignOwnerRole = errors.New("organization: cannot assign owner role")
	// ErrCannotChangeOwnerRole 不允许修改 owner member 的角色
	ErrCannotChangeOwnerRole = errors.New("organization: cannot change owner role")
	// ErrRolePermissionInvalid permissions 列表里有未知 perm
	ErrRolePermissionInvalid = errors.New("organization: role permission invalid")
	// ErrRolePermissionCeilingExceeded 提供的 permissions 超出 caller 上限
	ErrRolePermissionCeilingExceeded = errors.New("organization: role permission ceiling exceeded")

	// ─ 404 段:角色 ─

	// ErrRoleNotFound 角色不存在
	ErrRoleNotFound = errors.New("organization: role not found")

	// ─ 邀请 400 段 ─

	// ErrInvitationEmailInvalid 邀请目标 email 格式非法
	ErrInvitationEmailInvalid = errors.New("organization: invitation email invalid")
	// ErrInvitationEmailAlreadyMember 邀请目标 email 已是该 org 的成员
	ErrInvitationEmailAlreadyMember = errors.New("organization: email is already a member")
	// ErrInvitationDuplicatePending 同 org 同 email 已存在一条 pending 邀请
	ErrInvitationDuplicatePending = errors.New("organization: duplicate pending invitation")
	// ErrInvitationCannotInviteOwner 不能邀请 owner 角色
	ErrInvitationCannotInviteOwner = errors.New("organization: cannot invite owner role")
	// ErrInvitationNotPending 邀请已不在 pending 状态
	ErrInvitationNotPending = errors.New("organization: invitation not pending")
	// ErrInvitationExpired 邀请已过期
	ErrInvitationExpired = errors.New("organization: invitation expired")
	// ErrInvitationEmailMismatch 登录用户 email 与邀请目标 email 不一致
	ErrInvitationEmailMismatch = errors.New("organization: invitation email mismatch")
	// ErrInvitationTokenInvalid token 无效(格式错或查无此邀请)
	ErrInvitationTokenInvalid = errors.New("organization: invitation token invalid")
	// ErrInvitationSearchInvalid 搜索参数非法
	ErrInvitationSearchInvalid = errors.New("organization: invitation search invalid")

	// ─ 邀请 404 段 ─

	// ErrInvitationNotFound 邀请不存在
	ErrInvitationNotFound = errors.New("organization: invitation not found")

	// ─ 邀请 429 段 ─

	// ErrInvitationRateLimited 邀请邮件发送触及限流
	ErrInvitationRateLimited = errors.New("organization: invitation send rate limited")

	// ─ 500 段 ─

	// ErrOrgInternal 内部基础设施错误
	ErrOrgInternal = errors.New("organization: internal error")
)
