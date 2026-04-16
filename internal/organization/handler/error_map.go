// error_map.go 用户侧错误码映射。
//
// handleServiceError 负责把 service 层的 sentinel error 翻译为对应的 HTTP 响应。
// 约定:
//   - 业务错误一律 HTTP 200 + body 业务码
//   - 仅内部错误(ErrOrgInternal)使用 500
//   - 所有分支都打日志(WarnCtx 业务异常 / ErrorCtx 内部错误)
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层返回的 error 映射为 HTTP 响应。
// 约定:使用 errors.Is 顺序匹配 sentinel,默认走 500 Internal Server Error。
func (h *OrgHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段:请求/业务校验 ─────

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

	// 邀请相关
	case errors.Is(err, organization.ErrInvitationInvalidTarget):
		h.logger.WarnCtx(ctx, "邀请目标无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationInvalidTarget, Message: "Invalid invitation target"})
	case errors.Is(err, organization.ErrInvitationAlreadyMember):
		h.logger.WarnCtx(ctx, "被邀请人已是成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationAlreadyMember, Message: "Invitee already a member"})
	case errors.Is(err, organization.ErrInvitationAlreadyPending):
		h.logger.WarnCtx(ctx, "已有 pending 邀请", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationAlreadyPending, Message: "Invitation already pending"})
	case errors.Is(err, organization.ErrInvitationExpired):
		h.logger.WarnCtx(ctx, "邀请已过期", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationExpired, Message: "Invitation expired"})
	case errors.Is(err, organization.ErrInvitationNotForYou):
		h.logger.WarnCtx(ctx, "非该邀请的被邀请人", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationNotForYou, Message: "Invitation not for you"})
	case errors.Is(err, organization.ErrInvitationNotPending):
		h.logger.WarnCtx(ctx, "邀请非 pending 状态", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationNotPending, Message: "Invitation not pending"})
	case errors.Is(err, organization.ErrInvitationInviteeNotRegistered):
		h.logger.WarnCtx(ctx, "被邀请人未注册", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationInviteeNotRegistered, Message: "Invitee not registered"})
	case errors.Is(err, organization.ErrInvitationSelf):
		h.logger.WarnCtx(ctx, "不能邀请自己", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationSelf, Message: "Cannot invite yourself"})

	// 角色相关
	case errors.Is(err, organization.ErrRoleNotCustom):
		h.logger.WarnCtx(ctx, "预设角色不可操作", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleNotCustom, Message: "Preset role cannot be modified or deleted"})
	case errors.Is(err, organization.ErrRolePermissionInvalid):
		h.logger.WarnCtx(ctx, "角色权限无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRolePermissionInvalid, Message: "Invalid role permissions"})
	case errors.Is(err, organization.ErrRoleInUse):
		h.logger.WarnCtx(ctx, "角色被使用中", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleInUse, Message: "Role in use, cannot delete"})
	case errors.Is(err, organization.ErrRoleNameInvalid):
		h.logger.WarnCtx(ctx, "角色名非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleNameInvalid, Message: "Invalid role name"})
	case errors.Is(err, organization.ErrRolePermissionEmpty):
		h.logger.WarnCtx(ctx, "角色权限为空", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRolePermissionEmpty, Message: "Role must have at least one permission"})

	// owner / 转让相关
	case errors.Is(err, organization.ErrOwnerCannotLeave):
		h.logger.WarnCtx(ctx, "owner 不能退出", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOwnerCannotLeave, Message: "Owner cannot leave, transfer ownership first"})
	case errors.Is(err, organization.ErrTransferTargetNotMember):
		h.logger.WarnCtx(ctx, "转让目标非成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeTransferTargetNotMember, Message: "Transfer target is not a member"})
	case errors.Is(err, organization.ErrOwnerSelfDemote):
		h.logger.WarnCtx(ctx, "owner 不能自降", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOwnerSelfDemote, Message: "Owner cannot self-demote"})
	case errors.Is(err, organization.ErrMemberRemoveOwner):
		h.logger.WarnCtx(ctx, "不能踢 owner", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeMemberRemoveOwner, Message: "Cannot remove the owner"})

	// ─── 403 段:权限 ─────

	case errors.Is(err, organization.ErrOrgPermissionDenied):
		h.logger.WarnCtx(ctx, "无权操作", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgPermissionDenied, Message: "Permission denied"})
	case errors.Is(err, organization.ErrOrgNotMember):
		h.logger.WarnCtx(ctx, "非成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgNotMember, Message: "Not a member of this organization"})
	case errors.Is(err, organization.ErrOrgOwnerOnly):
		h.logger.WarnCtx(ctx, "仅 owner 可操作", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgOwnerOnly, Message: "Owner only"})

	// ─── 404 段:资源不存在 ─────

	case errors.Is(err, organization.ErrOrgNotFound):
		h.logger.WarnCtx(ctx, "org 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgNotFound, Message: "Organization not found"})
	case errors.Is(err, organization.ErrOrgDissolved):
		h.logger.WarnCtx(ctx, "org 已解散", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgDissolved, Message: "Organization dissolved"})
	case errors.Is(err, organization.ErrInvitationNotFound):
		h.logger.WarnCtx(ctx, "邀请不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeInvitationNotFound, Message: "Invitation not found"})
	case errors.Is(err, organization.ErrMemberNotFound):
		h.logger.WarnCtx(ctx, "成员不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeMemberNotFound, Message: "Member not found"})
	case errors.Is(err, organization.ErrRoleNotFound):
		h.logger.WarnCtx(ctx, "角色不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleNotFound, Message: "Role not found"})
	case errors.Is(err, organization.ErrUserNotFound):
		h.logger.WarnCtx(ctx, "用户不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeUserNotFound, Message: "User not found"})

	// ─── 409 段:冲突 ─────

	case errors.Is(err, organization.ErrOrgSlugTaken):
		h.logger.WarnCtx(ctx, "slug 已被占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeOrgSlugTaken, Message: "Slug already taken"})
	case errors.Is(err, organization.ErrRoleNameTaken):
		h.logger.WarnCtx(ctx, "角色名已占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: organization.CodeRoleNameTaken, Message: "Role name already taken"})

	// ─── 500 段:内部错误 ─────

	case errors.Is(err, organization.ErrOrgInternal):
		h.logger.ErrorCtx(ctx, "组织模块内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "未映射的组织模块错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
