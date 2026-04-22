// error_map.go 权限模块用户侧错误码映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层返回的 error 映射为 HTTP 响应。
func (h *PermHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────

	case errors.Is(err, permission.ErrPermInvalidRequest):
		h.logger.WarnCtx(ctx, "请求参数无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodePermInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, permission.ErrGroupNameInvalid):
		h.logger.WarnCtx(ctx, "组名非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupNameInvalid, Message: "Invalid group name"})
	case errors.Is(err, permission.ErrMaxGroupsReached):
		h.logger.WarnCtx(ctx, "超出组数上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeMaxGroupsReached, Message: "Max groups per organization reached"})
	case errors.Is(err, permission.ErrGroupHasMembers):
		h.logger.WarnCtx(ctx, "组内仍有成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupHasMembers, Message: "Group still has members"})
	case errors.Is(err, permission.ErrMaxMembersInGroup):
		h.logger.WarnCtx(ctx, "超出组成员上限", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeMaxMembersInGroup, Message: "Max members per group reached"})
	case errors.Is(err, permission.ErrCannotRemoveGroupOwner):
		h.logger.WarnCtx(ctx, "不能把组 owner 踢出", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeCannotRemoveGroupOwner, Message: "Cannot remove the group owner; transfer or delete the group instead"})
	case errors.Is(err, permission.ErrUserNotOrgMember):
		h.logger.WarnCtx(ctx, "目标不是 org 成员", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeUserNotOrgMember, Message: "Target user is not a member of this organization"})

	// ─── 403 段 ─────

	case errors.Is(err, permission.ErrPermForbidden):
		h.logger.WarnCtx(ctx, "无权执行该操作", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodePermForbidden, Message: "Forbidden"})

	// ─── 404 段 ─────

	case errors.Is(err, permission.ErrGroupNotFound):
		h.logger.WarnCtx(ctx, "权限组不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupNotFound, Message: "Group not found"})
	case errors.Is(err, permission.ErrGroupMemberNotFound):
		h.logger.WarnCtx(ctx, "组成员不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupMemberNotFound, Message: "Group member not found"})

	// ─── 409 段 ─────

	case errors.Is(err, permission.ErrGroupNameTaken):
		h.logger.WarnCtx(ctx, "组名已被占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupNameTaken, Message: "Group name already taken"})
	case errors.Is(err, permission.ErrGroupMemberExists):
		h.logger.WarnCtx(ctx, "用户已在组中", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeGroupMemberExists, Message: "User already in group"})

	// ─── 500 段 ─────

	case errors.Is(err, permission.ErrPermInternal):
		h.logger.ErrorCtx(ctx, "权限模块内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "未映射的权限模块错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
