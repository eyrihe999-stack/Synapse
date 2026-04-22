// error_map.go asyncjob 模块错误映射。
//
// HTTP handler 层查询端点目前只依赖 GetJob —— service 层这个方法只会返 ErrAsyncJobInternal。
// Schedule 的业务错误(Duplicate/UnknownKind/Invalid)由调用 Schedule 的业务 handler
// (如 document)自行映射,本文件提供通用映射函数供复用。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// handleServiceError 把 service 层返回的 error 映射为 HTTP 响应。
func (h *Handler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────

	case errors.Is(err, asyncjob.ErrAsyncJobInvalidRequest):
		h.log.WarnCtx(ctx, "asyncjob: invalid request", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: asyncjob.CodeAsyncJobInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, asyncjob.ErrUnknownKind):
		h.log.WarnCtx(ctx, "asyncjob: unknown kind", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: asyncjob.CodeAsyncJobUnknownKind, Message: "Unknown job kind"})

	// ─── 404 段 ─────

	case errors.Is(err, asyncjob.ErrAsyncJobNotFound):
		h.log.WarnCtx(ctx, "asyncjob: job not found", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: asyncjob.CodeAsyncJobNotFound, Message: "Job not found"})

	// ─── 409 段 ─────

	case errors.Is(err, asyncjob.ErrDuplicateJob):
		h.log.WarnCtx(ctx, "asyncjob: duplicate active job", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: asyncjob.CodeAsyncJobDuplicate, Message: "Duplicate active job"})

	// ─── 500 段 ─────

	case errors.Is(err, asyncjob.ErrAsyncJobInternal):
		h.log.ErrorCtx(ctx, "asyncjob: internal error", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.log.ErrorCtx(ctx, "asyncjob: unmapped error", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
