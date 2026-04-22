// errors.go 权限模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码 (400/403/404/409/500)
//   - SS:模块号 19 = permission
//   - CCCC:业务码
package permission

import "errors"

// ─── 400 段:请求/业务校验错误 ────────────────────────────────────────────────

const (
	// CodePermInvalidRequest 请求参数无效
	CodePermInvalidRequest = 400190010

	// ─ 权限组 400 段 ─

	// CodeGroupNameInvalid 组名非法(为空 / 超长 / 仅空白)
	CodeGroupNameInvalid = 400190060
	// CodeMaxGroupsReached 超出单 org 权限组上限
	CodeMaxGroupsReached = 400190061
	// CodeGroupHasMembers 组内仍有成员,部分清理操作不允许(占位,M1 不用,删组直接级联)
	CodeGroupHasMembers = 400190062
	// CodeMaxMembersInGroup 超出单组成员上限
	CodeMaxMembersInGroup = 400190063
	// CodeCannotRemoveGroupOwner 不允许把组 owner 自己从组里踢出(owner 必须先转让或删组)
	CodeCannotRemoveGroupOwner = 400190064
	// CodeUserNotOrgMember 目标 user 不是该 org 的成员,不能加入该 org 的权限组
	CodeUserNotOrgMember = 400190065

	// ─ ACL 400 段 ─

	// CodeACLInvalidSubjectType subject_type 取值非法(只接受 group/user)
	CodeACLInvalidSubjectType = 400190070
	// CodeACLInvalidPermission permission 取值非法(只接受 read/write)
	CodeACLInvalidPermission = 400190071
	// CodeACLInvalidResourceType resource_type 取值非法(M3 只 source)
	CodeACLInvalidResourceType = 400190072
	// CodeACLSubjectNotFound 授权目标不存在(group_id 不存在 / user_id 不是 org 成员)
	CodeACLSubjectNotFound = 400190073
	// CodeACLOnOwnSubject 不允许给资源 owner 自己授权(owner 隐式拥有 admin)
	CodeACLOnOwnSubject = 400190074
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodePermForbidden 调用方无权执行该操作(组 owner-only 等隐式硬规则)
	CodePermForbidden = 403190010
)

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

const (
	// CodeGroupNotFound 权限组不存在(或不属于当前 org)
	CodeGroupNotFound = 404190020
	// CodeGroupMemberNotFound 该用户不在该组中
	CodeGroupMemberNotFound = 404190021
	// CodeACLNotFound ACL 行不存在
	CodeACLNotFound = 404190022
)

// ─── 409 段:冲突 ─────────────────────────────────────────────────────────────

const (
	// CodeGroupNameTaken 组名在该 org 内已被占用
	CodeGroupNameTaken = 409190010
	// CodeGroupMemberExists 该用户已在该组中
	CodeGroupMemberExists = 409190011
	// CodeACLExists 该 (resource, subject) 已经有 ACL(grant 接口冲突;改 permission 用 PATCH)
	CodeACLExists = 409190012
)

// ─── 500 段:内部错误 ────────────────────────────────────────────────────────

const (
	// CodePermInternal 内部错误
	CodePermInternal = 500190000
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ─ 400 段 ─

	// ErrPermInvalidRequest 请求参数无效
	ErrPermInvalidRequest = errors.New("permission: invalid request")
	// ErrGroupNameInvalid 组名非法
	ErrGroupNameInvalid = errors.New("permission: group name invalid")
	// ErrMaxGroupsReached 超出单 org 权限组上限
	ErrMaxGroupsReached = errors.New("permission: max groups reached")
	// ErrGroupHasMembers 组内仍有成员
	ErrGroupHasMembers = errors.New("permission: group has members")
	// ErrMaxMembersInGroup 超出单组成员上限
	ErrMaxMembersInGroup = errors.New("permission: max members in group reached")
	// ErrCannotRemoveGroupOwner 不能把组 owner 从组里踢出
	ErrCannotRemoveGroupOwner = errors.New("permission: cannot remove group owner")
	// ErrUserNotOrgMember 目标 user 不是 org 成员
	ErrUserNotOrgMember = errors.New("permission: user is not a member of this organization")
	// ErrACLInvalidSubjectType subject_type 取值非法
	ErrACLInvalidSubjectType = errors.New("permission: invalid acl subject type")
	// ErrACLInvalidPermission permission 取值非法
	ErrACLInvalidPermission = errors.New("permission: invalid acl permission")
	// ErrACLInvalidResourceType resource_type 取值非法
	ErrACLInvalidResourceType = errors.New("permission: invalid acl resource type")
	// ErrACLSubjectNotFound 授权目标(group/user)不存在或不属于该 org
	ErrACLSubjectNotFound = errors.New("permission: acl subject not found")
	// ErrACLOnOwnSubject 不允许给资源 owner 自己授权
	ErrACLOnOwnSubject = errors.New("permission: cannot grant acl to resource owner")

	// ─ 403 段 ─

	// ErrPermForbidden 调用方无权执行
	ErrPermForbidden = errors.New("permission: forbidden")

	// ─ 404 段 ─

	// ErrGroupNotFound 权限组不存在
	ErrGroupNotFound = errors.New("permission: group not found")
	// ErrGroupMemberNotFound 该用户不在该组中
	ErrGroupMemberNotFound = errors.New("permission: group member not found")
	// ErrACLNotFound ACL 行不存在
	ErrACLNotFound = errors.New("permission: acl not found")

	// ─ 409 段 ─

	// ErrGroupNameTaken 组名已被占用
	ErrGroupNameTaken = errors.New("permission: group name taken")
	// ErrGroupMemberExists 该用户已在该组中
	ErrGroupMemberExists = errors.New("permission: group member already exists")
	// ErrACLExists 该 (resource, subject) 已经有 ACL 行(grant 时用,update 走 PATCH)
	ErrACLExists = errors.New("permission: acl already exists")

	// ─ 500 段 ─

	// ErrPermInternal 内部基础设施错误
	ErrPermInternal = errors.New("permission: internal error")
)
