// error_map.go source 模块用户侧错误码映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层返回的 error 映射为 HTTP 响应。
func (h *SourceHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	case errors.Is(err, source.ErrSourceInvalidRequest):
		h.logger.WarnCtx(ctx, "请求参数无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, source.ErrSourceInvalidVisibility):
		h.logger.WarnCtx(ctx, "visibility 取值非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceInvalidVisibility, Message: "Invalid visibility (must be one of: org, group, private)"})
	case errors.Is(err, source.ErrSourceInvalidKind):
		h.logger.WarnCtx(ctx, "kind 取值非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceInvalidKind, Message: "Invalid source kind"})
	case errors.Is(err, source.ErrSourceInvalidName):
		h.logger.WarnCtx(ctx, "source name 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceInvalidName, Message: "Invalid source name (non-empty, <=128 chars)"})
	case errors.Is(err, source.ErrSourceNameExists):
		h.logger.WarnCtx(ctx, "source name 已被占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceNameExists, Message: "You already have a source with this name"})
	case errors.Is(err, source.ErrSourceHasDocuments):
		h.logger.WarnCtx(ctx, "source 下仍有 doc,拒绝删除", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceHasDocuments, Message: "Source still has documents; delete them first"})

	case errors.Is(err, source.ErrSourceForbidden):
		h.logger.WarnCtx(ctx, "无权操作 source", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceForbidden, Message: "Forbidden"})

	case errors.Is(err, source.ErrSourceNotFound):
		h.logger.WarnCtx(ctx, "source 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: source.CodeSourceNotFound, Message: "Source not found"})

	// ─── ACL 相关错误(从 permission 模块透传) ─────

	case errors.Is(err, permission.ErrACLInvalidSubjectType):
		h.logger.WarnCtx(ctx, "ACL subject_type 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLInvalidSubjectType, Message: "Invalid acl subject type (must be group or user)"})
	case errors.Is(err, permission.ErrACLInvalidPermission):
		h.logger.WarnCtx(ctx, "ACL permission 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLInvalidPermission, Message: "Invalid acl permission (must be read or write)"})
	case errors.Is(err, permission.ErrACLInvalidResourceType):
		h.logger.WarnCtx(ctx, "ACL resource_type 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLInvalidResourceType, Message: "Invalid acl resource type"})
	case errors.Is(err, permission.ErrACLSubjectNotFound):
		h.logger.WarnCtx(ctx, "ACL 授权目标不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLSubjectNotFound, Message: "Subject not found in this organization"})
	case errors.Is(err, permission.ErrACLOnOwnSubject):
		h.logger.WarnCtx(ctx, "不能给资源 owner 自己授权", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLOnOwnSubject, Message: "Cannot grant ACL to the resource owner (owner has implicit admin)"})
	case errors.Is(err, permission.ErrACLNotFound):
		h.logger.WarnCtx(ctx, "ACL 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLNotFound, Message: "ACL not found"})
	case errors.Is(err, permission.ErrACLExists):
		h.logger.WarnCtx(ctx, "ACL 已存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: permission.CodeACLExists, Message: "ACL already exists for this (resource, subject); use PATCH to change permission"})

	case errors.Is(err, source.ErrSourceInternal):
		h.logger.ErrorCtx(ctx, "source 模块内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "未映射的 source 模块错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
