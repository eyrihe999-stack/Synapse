// error_map.go 用户侧错误码映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层返回的 error 映射为 HTTP 响应。
func (h *OrgHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────

	case errors.Is(err, organization.ErrOrgInvalidRequest):
		h.logger.WarnCtx(ctx, "请求参数无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, organization.ErrOrgSlugInvalid):
		h.logger.WarnCtx(ctx, "slug 格式非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgSlugInvalid, Message: "Invalid slug format"})
	case errors.Is(err, organization.ErrOrgDisplayNameInvalid):
		h.logger.WarnCtx(ctx, "display_name 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgDisplayNameInvalid, Message: "Invalid display name"})
	case errors.Is(err, organization.ErrOrgMaxOwnedReached):
		h.logger.WarnCtx(ctx, "超出创建上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgMaxOwnedReached, Message: "Max owned organizations reached"})
	case errors.Is(err, organization.ErrOrgMaxJoinedReached):
		h.logger.WarnCtx(ctx, "超出加入上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgMaxJoinedReached, Message: "Max joined organizations reached"})
	case errors.Is(err, organization.ErrOwnerCannotLeave):
		h.logger.WarnCtx(ctx, "owner 不能退出", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOwnerCannotLeave, Message: "Owner cannot leave, dissolve the organization instead"})
	case errors.Is(err, organization.ErrMemberRemoveOwner):
		h.logger.WarnCtx(ctx, "不能踢 owner", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeMemberRemoveOwner, Message: "Cannot remove the owner"})

	// ─── 400 段:角色 ─────

	case errors.Is(err, organization.ErrRoleSlugInvalid):
		h.logger.WarnCtx(ctx, "role slug 格式非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleSlugInvalid, Message: "Invalid role slug format"})
	case errors.Is(err, organization.ErrRoleSlugReserved):
		h.logger.WarnCtx(ctx, "role slug 保留", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleSlugReserved, Message: "Role slug is reserved by system roles"})
	case errors.Is(err, organization.ErrRoleDisplayNameInvalid):
		h.logger.WarnCtx(ctx, "role display_name 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleDisplayNameInvalid, Message: "Invalid role display name"})
	case errors.Is(err, organization.ErrRoleIsSystem):
		h.logger.WarnCtx(ctx, "不能操作系统角色", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleIsSystem, Message: "Cannot modify or delete system roles"})
	case errors.Is(err, organization.ErrRoleHasMembers):
		h.logger.WarnCtx(ctx, "角色下仍有成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleHasMembers, Message: "Role still has members, reassign them before deleting"})
	case errors.Is(err, organization.ErrMaxCustomRolesReached):
		h.logger.WarnCtx(ctx, "超出自定义角色上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeMaxCustomRolesReached, Message: "Max custom roles reached"})
	case errors.Is(err, organization.ErrCannotAssignOwnerRole):
		h.logger.WarnCtx(ctx, "不能分配 owner 角色", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeCannotAssignOwnerRole, Message: "Cannot assign owner role via this endpoint"})
	case errors.Is(err, organization.ErrCannotChangeOwnerRole):
		h.logger.WarnCtx(ctx, "不能修改 owner 的角色", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeCannotChangeOwnerRole, Message: "Cannot change the role of the organization owner"})
	case errors.Is(err, organization.ErrRolePermissionInvalid):
		h.logger.WarnCtx(ctx, "permissions 列表含未知 perm", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRolePermissionInvalid, Message: "Permissions list contains unknown permission"})
	case errors.Is(err, organization.ErrRolePermissionCeilingExceeded):
		h.logger.WarnCtx(ctx, "permissions 超出 caller 上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRolePermissionCeilingExceeded, Message: "Permissions exceed your own ceiling"})

	// ─── 400 段:邀请 ─────

	case errors.Is(err, organization.ErrInvitationEmailInvalid):
		h.logger.WarnCtx(ctx, "邀请 email 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationEmailInvalid, Message: "Invalid email address"})
	case errors.Is(err, organization.ErrInvitationEmailAlreadyMember):
		h.logger.WarnCtx(ctx, "邀请 email 已是成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationEmailAlreadyMember, Message: "This email is already a member of the organization"})
	case errors.Is(err, organization.ErrInvitationDuplicatePending):
		h.logger.WarnCtx(ctx, "已存在 pending 邀请", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationDuplicatePending, Message: "There is already a pending invitation for this email, resend it instead"})
	case errors.Is(err, organization.ErrInvitationCannotInviteOwner):
		h.logger.WarnCtx(ctx, "不能邀请 owner 角色", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationCannotInviteOwner, Message: "Cannot invite with owner role"})
	case errors.Is(err, organization.ErrInvitationNotPending):
		h.logger.WarnCtx(ctx, "邀请非 pending", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationNotPending, Message: "Invitation is no longer pending"})
	case errors.Is(err, organization.ErrInvitationExpired):
		h.logger.WarnCtx(ctx, "邀请已过期", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationExpired, Message: "Invitation has expired"})
	case errors.Is(err, organization.ErrInvitationEmailMismatch):
		h.logger.WarnCtx(ctx, "邀请 email 与登录用户不符", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationEmailMismatch, Message: "Your email does not match the invitation"})
	case errors.Is(err, organization.ErrInvitationTokenInvalid):
		h.logger.WarnCtx(ctx, "邀请 token 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationTokenInvalid, Message: "Invalid invitation token"})
	case errors.Is(err, organization.ErrInvitationSearchInvalid):
		h.logger.WarnCtx(ctx, "邀请搜索参数非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationSearchInvalid, Message: "Invalid search parameters"})

	// ─── 403 段 ─────

	case errors.Is(err, organization.ErrOrgNotMember):
		h.logger.WarnCtx(ctx, "非成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgNotMember, Message: "Not a member of this organization"})
	case errors.Is(err, organization.ErrOrgUserNotVerified):
		h.logger.WarnCtx(ctx, "邮箱未验证,拒绝创建 org", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgUserNotVerified, Message: "Please verify your email before creating an organization"})

	// ─── 404 段 ─────

	case errors.Is(err, organization.ErrOrgNotFound):
		h.logger.WarnCtx(ctx, "org 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgNotFound, Message: "Organization not found"})
	case errors.Is(err, organization.ErrOrgDissolved):
		h.logger.WarnCtx(ctx, "org 已解散", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgDissolved, Message: "Organization dissolved"})
	case errors.Is(err, organization.ErrMemberNotFound):
		h.logger.WarnCtx(ctx, "成员不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeMemberNotFound, Message: "Member not found"})
	case errors.Is(err, organization.ErrRoleNotFound):
		h.logger.WarnCtx(ctx, "角色不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleNotFound, Message: "Role not found"})
	case errors.Is(err, organization.ErrInvitationNotFound):
		h.logger.WarnCtx(ctx, "邀请不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationNotFound, Message: "Invitation not found"})

	// ─── 429 段 ─────

	case errors.Is(err, organization.ErrInvitationRateLimited):
		h.logger.WarnCtx(ctx, "邀请邮件发送触发限流", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationRateLimited, Message: "Invitation sending is rate limited, please try later"})

	// ─── 409 段 ─────

	case errors.Is(err, organization.ErrOrgSlugTaken):
		h.logger.WarnCtx(ctx, "slug 已被占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgSlugTaken, Message: "Slug already taken"})
	case errors.Is(err, organization.ErrRoleSlugTaken):
		h.logger.WarnCtx(ctx, "role slug 已被占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleSlugTaken, Message: "Role slug already taken"})

	// ─── 500 段 ─────

	case errors.Is(err, organization.ErrOrgInternal):
		h.logger.ErrorCtx(ctx, "组织模块内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "未映射的组织模块错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
