// errors.go 组织模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/403/404/409/500)
//   - SS:模块号 11 = organization
//   - CCCC:业务码
//
// 所有业务错误(400/403/404/409)统一以 HTTP 200 + body 业务码返回,
// 仅 500 内部错误使用非 200 状态码。限流错误在 agent 模块使用 429 是唯一例外。
package organization

import "errors"

// ─── 400 段:请求/业务校验错误 ────────────────────────────────────────────────

// 400 11 001x 通用参数错误
const (
	// CodeOrgInvalidRequest 请求参数无效
	CodeOrgInvalidRequest = 400110010
	// CodeOrgSlugInvalid slug 格式不合法(非 kebab-case 或长度超限)
	CodeOrgSlugInvalid = 400110011
	// CodeOrgDisplayNameInvalid display_name 长度不合法
	CodeOrgDisplayNameInvalid = 400110012
)

// 400 11 002x 数量限制
const (
	// CodeOrgMaxOwnedReached 超出每用户最大创建 org 数
	CodeOrgMaxOwnedReached = 400110020
	// CodeOrgMaxJoinedReached 超出每用户最大加入 org 数
	CodeOrgMaxJoinedReached = 400110021
)

// 400 11 003x 邀请流程错误
const (
	// CodeInvitationInvalidTarget 邀请目标不存在或未注册
	CodeInvitationInvalidTarget = 400110030
	// CodeInvitationAlreadyMember 被邀请人已是成员
	CodeInvitationAlreadyMember = 400110031
	// CodeInvitationAlreadyPending 已有未处理邀请
	CodeInvitationAlreadyPending = 400110032
	// CodeInvitationExpired 邀请已过期
	CodeInvitationExpired = 400110033
	// CodeInvitationNotForYou 不是该邀请的被邀请人
	CodeInvitationNotForYou = 400110034
	// CodeInvitationNotPending 邀请不处于 pending 状态
	CodeInvitationNotPending = 400110035
	// CodeInvitationInviteeNotRegistered 被邀请人未注册(手机号/邮箱未找到用户)
	CodeInvitationInviteeNotRegistered = 400110036
	// CodeInvitationSelf 不能邀请自己
	CodeInvitationSelf = 400110037
)

// 400 11 004x 角色校验
const (
	// CodeRoleNotCustom 尝试编辑/删除预设角色
	CodeRoleNotCustom = 400110040
	// CodeRolePermissionInvalid 权限点不存在或被保留(owner 独占)
	CodeRolePermissionInvalid = 400110041
	// CodeRoleInUse 角色正在被成员使用,不能删除
	CodeRoleInUse = 400110042
	// CodeRoleNameInvalid 角色 name 格式/长度非法
	CodeRoleNameInvalid = 400110043
	// CodeRolePermissionEmpty 自定义角色至少包含 1 个权限点
	CodeRolePermissionEmpty = 400110044
)

// 400 11 005x 成员/所有权
const (
	// CodeOwnerCannotLeave owner 不能主动退出 org(必须先转让或解散)
	CodeOwnerCannotLeave = 400110050
	// CodeTransferTargetNotMember 转让目标不是 org 成员
	CodeTransferTargetNotMember = 400110051
	// CodeOwnerSelfDemote owner 不能给自己降级
	CodeOwnerSelfDemote = 400110052
	// CodeMemberRemoveOwner 不能踢出 owner
	CodeMemberRemoveOwner = 400110053
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

// 403 11 001x
const (
	// CodeOrgPermissionDenied 无权执行该操作
	CodeOrgPermissionDenied = 403110010
	// CodeOrgNotMember 不是该 org 的成员
	CodeOrgNotMember = 403110011
	// CodeOrgOwnerOnly 该操作仅 owner 可执行
	CodeOrgOwnerOnly = 403110012
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

// 404 11 002x
const (
	// CodeOrgNotFound org 不存在
	CodeOrgNotFound = 404110020
	// CodeOrgDissolved org 已解散
	CodeOrgDissolved = 404110021
	// CodeInvitationNotFound 邀请不存在
	CodeInvitationNotFound = 404110022
	// CodeMemberNotFound 成员不存在
	CodeMemberNotFound = 404110023
	// CodeRoleNotFound 角色不存在
	CodeRoleNotFound = 404110024
	// CodeUserNotFound 用户不存在
	CodeUserNotFound = 404110025
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

// 409 11 001x
const (
	// CodeOrgSlugTaken slug 已被占用
	CodeOrgSlugTaken = 409110010
	// CodeRoleNameTaken 角色 name 在本 org 内已存在
	CodeRoleNameTaken = 409110011
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodeOrgInternal 内部错误
	CodeOrgInternal = 500110000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────
//
// Service 层返回的每个 error 都必须包含(是或包装)一个这里定义的 sentinel error,
// 禁止返回匿名的 fmt.Errorf 或 errors.New。
// Handler 层的 handleServiceError 必须用 errors.Is() 覆盖所有 sentinel 分支。

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

	// ErrInvitationInvalidTarget 邀请目标不合法
	ErrInvitationInvalidTarget = errors.New("organization: invitation target invalid")
	// ErrInvitationAlreadyMember 被邀请人已是成员
	ErrInvitationAlreadyMember = errors.New("organization: invitee already a member")
	// ErrInvitationAlreadyPending 已有未处理邀请
	ErrInvitationAlreadyPending = errors.New("organization: invitation already pending")
	// ErrInvitationExpired 邀请已过期
	ErrInvitationExpired = errors.New("organization: invitation expired")
	// ErrInvitationNotForYou 非该邀请的被邀请人
	ErrInvitationNotForYou = errors.New("organization: invitation not for you")
	// ErrInvitationNotPending 邀请状态非 pending
	ErrInvitationNotPending = errors.New("organization: invitation not pending")
	// ErrInvitationInviteeNotRegistered 被邀请人未注册
	ErrInvitationInviteeNotRegistered = errors.New("organization: invitee not registered")
	// ErrInvitationSelf 不能邀请自己
	ErrInvitationSelf = errors.New("organization: cannot invite self")

	// ErrRoleNotCustom 预设角色不可编辑/删除
	ErrRoleNotCustom = errors.New("organization: role is preset")
	// ErrRolePermissionInvalid 权限点不合法
	ErrRolePermissionInvalid = errors.New("organization: role permission invalid")
	// ErrRoleInUse 角色被成员引用,不能删除
	ErrRoleInUse = errors.New("organization: role in use")
	// ErrRoleNameInvalid 角色 name 格式非法
	ErrRoleNameInvalid = errors.New("organization: role name invalid")
	// ErrRolePermissionEmpty 自定义角色至少 1 个权限点
	ErrRolePermissionEmpty = errors.New("organization: role permissions empty")

	// ErrOwnerCannotLeave owner 不能主动退出
	ErrOwnerCannotLeave = errors.New("organization: owner cannot leave")
	// ErrTransferTargetNotMember 转让目标非成员
	ErrTransferTargetNotMember = errors.New("organization: transfer target not member")
	// ErrOwnerSelfDemote owner 不能给自己降级
	ErrOwnerSelfDemote = errors.New("organization: owner self demote")
	// ErrMemberRemoveOwner 不能踢出 owner
	ErrMemberRemoveOwner = errors.New("organization: cannot remove owner")

	// ─ 403 段 ─

	// ErrOrgPermissionDenied 无权执行
	ErrOrgPermissionDenied = errors.New("organization: permission denied")
	// ErrOrgNotMember 非成员
	ErrOrgNotMember = errors.New("organization: not a member")
	// ErrOrgOwnerOnly 仅 owner 可执行
	ErrOrgOwnerOnly = errors.New("organization: owner only")

	// ─ 404 段 ─

	// ErrOrgNotFound org 不存在
	ErrOrgNotFound = errors.New("organization: org not found")
	// ErrOrgDissolved org 已解散
	ErrOrgDissolved = errors.New("organization: org dissolved")
	// ErrInvitationNotFound 邀请不存在
	ErrInvitationNotFound = errors.New("organization: invitation not found")
	// ErrMemberNotFound 成员不存在
	ErrMemberNotFound = errors.New("organization: member not found")
	// ErrRoleNotFound 角色不存在
	ErrRoleNotFound = errors.New("organization: role not found")
	// ErrUserNotFound 用户不存在
	ErrUserNotFound = errors.New("organization: user not found")

	// ─ 409 段 ─

	// ErrOrgSlugTaken slug 已被占用
	ErrOrgSlugTaken = errors.New("organization: slug taken")
	// ErrRoleNameTaken 角色名已被占用
	ErrRoleNameTaken = errors.New("organization: role name taken")

	// ─ 500 段 ─

	// ErrOrgInternal 内部基础设施错误
	ErrOrgInternal = errors.New("organization: internal error")
)
