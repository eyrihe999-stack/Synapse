// error_map.go agent 模块 service 错误 → HTTP 响应的映射。
//
// 约定:
//   - 业务错误一律 HTTP 200 + body 业务码(和 organization 保持一致)
//   - 限流错误 429 是唯一保留非 200 的业务错误(网关硬拒绝)
//   - 上游 503/504 走 200 + body 码(客户端通过业务码判断)
//   - 仅 ErrAgentInternal / ErrAgentCryptoFailed 走 500
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层的错误映射为 HTTP 响应。
//
//sayso-lint:ignore complexity
func (h *AgentHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// ─── 400 段 ─────
	case errors.Is(err, agent.ErrAgentInvalidRequest):
		h.logger.WarnCtx(ctx, "请求参数无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, agent.ErrAgentSlugInvalid):
		h.logger.WarnCtx(ctx, "agent slug 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentSlugInvalid, Message: "Invalid agent slug"})
	case errors.Is(err, agent.ErrAgentEndpointInvalid):
		h.logger.WarnCtx(ctx, "endpoint 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentEndpointInvalid, Message: "Endpoint must be HTTPS"})
	case errors.Is(err, agent.ErrAgentProtocolUnsupported):
		h.logger.WarnCtx(ctx, "protocol 不支持", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentProtocolUnsupported, Message: "Protocol not supported"})
	case errors.Is(err, agent.ErrAgentTimeoutOutOfRange):
		h.logger.WarnCtx(ctx, "timeout 越界", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentTimeoutOutOfRange, Message: "Timeout out of range"})
	case errors.Is(err, agent.ErrAgentRateLimitOutOfRange):
		h.logger.WarnCtx(ctx, "rate_limit 越界", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentRateLimitOutOfRange, Message: "Rate limit out of range"})
	case errors.Is(err, agent.ErrAgentConcurrentOutOfRange):
		h.logger.WarnCtx(ctx, "max_concurrent 越界", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentConcurrentOutOfRange, Message: "Max concurrent out of range"})
	case errors.Is(err, agent.ErrAgentDisplayNameInvalid):
		h.logger.WarnCtx(ctx, "display_name 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentDisplayNameInvalid, Message: "Invalid display name"})

	case errors.Is(err, agent.ErrMethodEmpty):
		h.logger.WarnCtx(ctx, "method 为空", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodEmpty, Message: "At least one method required"})
	case errors.Is(err, agent.ErrMethodNameInvalid):
		h.logger.WarnCtx(ctx, "method_name 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodNameInvalid, Message: "Invalid method name"})
	case errors.Is(err, agent.ErrMethodTransportUnsupported):
		h.logger.WarnCtx(ctx, "transport 不支持", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodTransportUnsupported, Message: "Method transport not supported"})
	case errors.Is(err, agent.ErrMethodLastCannotDelete):
		h.logger.WarnCtx(ctx, "最后 method 不可删", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodLastCannotDelete, Message: "Cannot delete last method"})
	case errors.Is(err, agent.ErrMethodVisibilityInvalid):
		h.logger.WarnCtx(ctx, "visibility 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodVisibilityInvalid, Message: "Invalid visibility"})

	case errors.Is(err, agent.ErrInvokeMethodMissing):
		h.logger.WarnCtx(ctx, "JSON-RPC method 缺失", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvokeMethodMissing, Message: "JSON-RPC method missing"})
	case errors.Is(err, agent.ErrInvokeJSONRPCInvalid):
		h.logger.WarnCtx(ctx, "JSON-RPC body 非法", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvokeJSONRPCInvalid, Message: "Invalid JSON-RPC body"})
	case errors.Is(err, agent.ErrInvokeMethodNotDeclared):
		h.logger.WarnCtx(ctx, "method 未声明", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvokeMethodNotDeclared, Message: "Method not declared"})

	case errors.Is(err, agent.ErrPublishAlreadyExists):
		h.logger.WarnCtx(ctx, "publish 已存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishAlreadyExists, Message: "Publish already exists"})
	case errors.Is(err, agent.ErrPublishNotPending):
		h.logger.WarnCtx(ctx, "publish 非 pending", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishNotPending, Message: "Publish not pending"})

	// ─── 401 段 ─────
	case errors.Is(err, agent.ErrGatewayAgentAuthFailed):
		h.logger.WarnCtx(ctx, "上游 HMAC 验证失败", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeGatewayAgentAuthFailed, Message: "Upstream auth failed"})

	// ─── 403 段 ─────
	case errors.Is(err, agent.ErrAgentPermissionDenied):
		h.logger.WarnCtx(ctx, "无权操作", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentPermissionDenied, Message: "Permission denied"})
	case errors.Is(err, agent.ErrMethodPrivateInvoke):
		h.logger.WarnCtx(ctx, "private method 仅作者可调", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodPrivateInvoke, Message: "Method is private"})
	case errors.Is(err, agent.ErrAgentNotAuthor):
		h.logger.WarnCtx(ctx, "非 agent 作者", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotAuthor, Message: "Not the agent author"})

	// ─── 404 段 ─────
	case errors.Is(err, agent.ErrAgentNotFound):
		h.logger.WarnCtx(ctx, "agent 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotFound, Message: "Agent not found"})
	case errors.Is(err, agent.ErrMethodNotFound):
		h.logger.WarnCtx(ctx, "method 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodNotFound, Message: "Method not found"})
	case errors.Is(err, agent.ErrPublishNotFound):
		h.logger.WarnCtx(ctx, "publish 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodePublishNotFound, Message: "Publish not found"})
	case errors.Is(err, agent.ErrAgentNotPublishedInOrg):
		h.logger.WarnCtx(ctx, "agent 未发布到 org", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentNotPublishedInOrg, Message: "Agent not published in org"})
	case errors.Is(err, agent.ErrInvocationNotFound):
		h.logger.WarnCtx(ctx, "invocation 不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvocationNotFound, Message: "Invocation not found"})

	// ─── 409 段 ─────
	case errors.Is(err, agent.ErrAgentSlugTaken):
		h.logger.WarnCtx(ctx, "agent slug 已占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentSlugTaken, Message: "Agent slug taken"})
	case errors.Is(err, agent.ErrMethodNameTaken):
		h.logger.WarnCtx(ctx, "method_name 已占用", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeMethodNameTaken, Message: "Method name taken"})

	// ─── 429 段(限流,硬拒绝 HTTP 429) ─────
	case errors.Is(err, agent.ErrUserRateLimited):
		h.logger.WarnCtx(ctx, "用户全局限流", fields)
		c.JSON(http.StatusTooManyRequests, response.BaseResponse{Code: agent.CodeUserRateLimited, Message: "User rate limited"})
	case errors.Is(err, agent.ErrOrgRateLimited):
		h.logger.WarnCtx(ctx, "org 全局限流", fields)
		c.JSON(http.StatusTooManyRequests, response.BaseResponse{Code: agent.CodeOrgRateLimited, Message: "Org rate limited"})
	case errors.Is(err, agent.ErrAgentRateLimited):
		h.logger.WarnCtx(ctx, "agent 限流", fields)
		c.JSON(http.StatusTooManyRequests, response.BaseResponse{Code: agent.CodeAgentRateLimited, Message: "Agent rate limited"})
	case errors.Is(err, agent.ErrUserAgentRateLimited):
		h.logger.WarnCtx(ctx, "user-agent 限流", fields)
		c.JSON(http.StatusTooManyRequests, response.BaseResponse{Code: agent.CodeUserAgentRateLimited, Message: "User-agent rate limited"})

	// ─── 503/504 段 ─────
	case errors.Is(err, agent.ErrGatewayAgentTimeout):
		h.logger.WarnCtx(ctx, "上游超时", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeGatewayAgentTimeout, Message: "Upstream timeout"})
	case errors.Is(err, agent.ErrGatewayAgentUnreachable):
		h.logger.WarnCtx(ctx, "上游不可达", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeGatewayAgentUnreachable, Message: "Upstream unreachable"})
	case errors.Is(err, agent.ErrAgentUnhealthy):
		h.logger.WarnCtx(ctx, "agent unhealthy", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeAgentUnhealthy, Message: "Agent unhealthy"})
	case errors.Is(err, agent.ErrInvocationCanceled):
		h.logger.WarnCtx(ctx, "调用被取消", fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: agent.CodeInvocationCanceled, Message: "Invocation canceled"})

	// ─── 500 段 ─────
	case errors.Is(err, agent.ErrAgentCryptoFailed):
		h.logger.ErrorCtx(ctx, "crypto 失败", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	case errors.Is(err, agent.ErrAgentInternal):
		h.logger.ErrorCtx(ctx, "agent 内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	case errors.Is(err, agent.ErrAuditWriteFailed):
		h.logger.ErrorCtx(ctx, "audit write failed", err, fields)
		response.InternalServerError(c, "Internal server error", "")

	default:
		h.logger.ErrorCtx(ctx, "未映射的 agent 错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
