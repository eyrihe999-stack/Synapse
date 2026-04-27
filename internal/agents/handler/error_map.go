// error_map.go agents handler 的 sentinel → HTTP 响应映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// handleServiceError service 错误 → HTTP 响应统一入口。
//
// 约定:业务错走 HTTP 200 + body 业务码;内部错用 HTTP 500。
// 和 asyncjob / organization 风格一致。
func (h *Handler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────
	case errors.Is(err, agents.ErrAgentDisplayNameInvalid):
		h.log.WarnCtx(ctx, "agents: invalid display name", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agents.CodeAgentDisplayNameInvalid, Message: "Invalid display name"})
	case errors.Is(err, agents.ErrAgentInvalidRequest):
		h.log.WarnCtx(ctx, "agents: invalid request", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agents.CodeAgentInvalidRequest, Message: "Invalid request"})

	// ─── 403 段 ─────
	case errors.Is(err, agents.ErrAgentForbidden):
		h.log.WarnCtx(ctx, "agents: forbidden", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agents.CodeAgentForbidden, Message: "Forbidden"})

	// ─── 404 段 ─────
	case errors.Is(err, agents.ErrAgentNotFound):
		h.log.WarnCtx(ctx, "agents: not found", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agents.CodeAgentNotFound, Message: "Agent not found"})

	// ─── 500 段 ─────
	case errors.Is(err, agents.ErrAgentInternal):
		h.log.ErrorCtx(ctx, "agents: internal error", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.log.ErrorCtx(ctx, "agents: unmapped error", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
