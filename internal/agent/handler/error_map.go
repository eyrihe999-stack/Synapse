// error_map.go agent 模块 service 错误 → HTTP 响应的映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 将 service 层返回的 sentinel 错误映射为对应的 HTTP 响应码和业务码。
func (h *AgentHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// 400
	case errors.Is(err, agent.ErrAgentInvalidRequest):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, agent.ErrAgentSlugInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentSlugInvalid, Message: "Invalid agent slug"})
	case errors.Is(err, agent.ErrAgentEndpointInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentEndpointInvalid, Message: "Invalid endpoint URL"})
	case errors.Is(err, agent.ErrAgentTypeUnsupported):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentTypeUnsupported, Message: "Agent type not supported"})
	case errors.Is(err, agent.ErrAgentContextModeInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentContextModeInvalid, Message: "Invalid context mode"})
	case errors.Is(err, agent.ErrAgentMaxRoundsOutOfRange):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentMaxRoundsOutOfRange, Message: "Max context rounds out of range"})
	case errors.Is(err, agent.ErrAgentTimeoutOutOfRange):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentTimeoutOutOfRange, Message: "Timeout out of range"})
	case errors.Is(err, agent.ErrAgentDisplayNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentDisplayNameInvalid, Message: "Invalid display name"})
	case errors.Is(err, agent.ErrPublishAlreadyExists):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishAlreadyExists, Message: "Publish already exists"})
	case errors.Is(err, agent.ErrPublishNotPending):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishNotPending, Message: "Publish not pending"})
	case errors.Is(err, agent.ErrChatRequestInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeChatRequestInvalid, Message: "Invalid chat request"})
	case errors.Is(err, agent.ErrSessionNotBelongToUser):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeSessionNotBelongToUser, Message: "Session does not belong to user"})

	// 403
	case errors.Is(err, agent.ErrAgentPermissionDenied):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Permission denied"})
	case errors.Is(err, agent.ErrAgentNotAuthor):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotAuthor, Message: "Not the agent author"})

	// 404
	case errors.Is(err, agent.ErrAgentNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotFound, Message: "Agent not found"})
	case errors.Is(err, agent.ErrPublishNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishNotFound, Message: "Publish not found"})
	case errors.Is(err, agent.ErrAgentNotPublishedInOrg):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotPublishedInOrg, Message: "Agent not published in org"})
	case errors.Is(err, agent.ErrSessionNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeSessionNotFound, Message: "Session not found"})

	// 409
	case errors.Is(err, agent.ErrAgentSlugTaken):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentSlugTaken, Message: "Agent slug taken"})

	// 429
	case errors.Is(err, agent.ErrChatRateLimited):
		c.JSON(http.StatusTooManyRequests, response.BaseResponse{Code: agent.CodeChatRateLimited, Message: "Chat rate limited"})

	// 503/504
	case errors.Is(err, agent.ErrChatUpstreamTimeout):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeChatUpstreamTimeout, Message: "Upstream timeout"})
	case errors.Is(err, agent.ErrChatUpstreamUnreachable):
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeChatUpstreamUnreachable, Message: "Upstream unreachable"})

	// 500
	case errors.Is(err, agent.ErrAgentCryptoFailed):
		h.logger.ErrorCtx(ctx, "crypto failed", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	case errors.Is(err, agent.ErrAgentInternal):
		h.logger.ErrorCtx(ctx, "agent internal error", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "unmapped agent error", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
