// webhook_handler.go GitLab webhook 端点。
//
// 路径:POST /api/v2/webhooks/gitlab/:source_id
//
// 鉴权模型:
//   - **不挂 JWT / OAuth / OrgContext** —— GitLab 不会发任何 user 凭据
//   - 端点本身只校验 X-Gitlab-Token header(由 source.service 验签)
//   - 路由侧只挂 RequestID + MaxBodySize 通用中间件,其他全部跳过
//
// 性能:< 200ms 响应是 GitLab webhook 健康度要求。我们入队即返,真活儿走 asyncjob runner。
package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/gin-gonic/gin"
)

// webhookMaxBodyBytes GitLab push event payload 上限。大型 push(很多 commits)payload
// 偶见接近 1MB,留 4MB 余量。超限直接 413,GitLab 会重试小一点的 push 时仍能送达。
const webhookMaxBodyBytes = 4 * 1024 * 1024

// HandleGitLabWebhook POST /api/v2/webhooks/gitlab/:source_id
//
// 状态码:
//   - 200:已入队 / 已忽略(分支不匹配 / 分支删除 / 非 push event 等无操作场景)
//   - 401:验签失败 / project mismatch
//   - 400:source_id 非法 / payload 解析失败
//   - 404:source 不存在 / 不是 gitlab_repo
//   - 500:DB 错 / enqueuer 未装配
func (h *SourceHandler) HandleGitLabWebhook(c *gin.Context) {
	if !h.checkReady(c) {
		return
	}
	idStr := c.Param("source_id")
	sourceID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || sourceID == 0 {
		c.JSON(http.StatusBadRequest, response.BaseResponse{
			Code:    source.CodeSourceInvalidRequest,
			Message: "invalid source_id",
		})
		return
	}

	// X-Gitlab-Token:GitLab 把 owner 在 UI 粘的 secret 原样作 header 发回。
	// 注意:不是 HMAC 签名,GitLab 当前就这么做。
	headerToken := c.GetHeader("X-Gitlab-Token")

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookMaxBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, response.BaseResponse{
			Code:    source.CodeSourceInvalidRequest,
			Message: "read body failed",
		})
		return
	}
	if len(body) > webhookMaxBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, response.BaseResponse{
			Code:    source.CodeSourceInvalidRequest,
			Message: "payload too large",
		})
		return
	}

	jobID, accepted, err := h.svc.HandleGitLabWebhook(c.Request.Context(), sourceID, headerToken, body)
	if err != nil {
		// service 层的 sentinel 直接走标准 error_map,但这个端点没有 user_id ctx,
		// 所以走简化版直接挂状态码 — 不调 handleServiceError(它依赖 middleware.GetUserID)
		writeWebhookError(c, err)
		return
	}
	if !accepted {
		// 静默 ack:分支不匹配 / branch 删除 / 非 push event
		c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ignored"})
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    http.StatusOK,
		Message: "queued",
		Result:  map[string]any{"job_id": strconv.FormatUint(jobID, 10)},
	})
}

// writeWebhookError 把 service 层 sentinel 翻成 webhook 响应。
// 不复用 handleServiceError —— 后者依赖 user_id ctx;webhook 路径没有 user。
func writeWebhookError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, source.ErrSourceGitLabAuthFailed):
		c.JSON(http.StatusUnauthorized, response.BaseResponse{
			Code:    source.CodeSourceGitLabAuthFailed,
			Message: "webhook signature invalid",
		})
	case errors.Is(err, source.ErrSourceNotFound):
		c.JSON(http.StatusNotFound, response.BaseResponse{
			Code:    source.CodeSourceNotFound,
			Message: "source not found",
		})
	case errors.Is(err, source.ErrSourceInvalidRequest):
		c.JSON(http.StatusBadRequest, response.BaseResponse{
			Code:    source.CodeSourceInvalidRequest,
			Message: "invalid payload",
		})
	default:
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code:    source.CodeSourceInternal,
			Message: "internal error",
		})
	}
}
