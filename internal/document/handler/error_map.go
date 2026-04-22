// error_map.go document 模块 service/repository 错误 → HTTP 响应的集中映射。
//
// 设计要点:
//   - 所有业务错误走 sentinel(internal/document.ErrXxx),handler 层不直接判 gorm 错。
//   - 未映射的错误统一按 500 返回,并 ErrorCtx 级别打日志(未预期路径)。
//   - 400/404 走 WarnCtx(可预期分支,不刷 error 日志噪音)。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// handleError 把 service/repository 层的 error 映射为 HTTP 响应。
//
// scene 字段是操作名(如 "prepare upload"、"get doc"),只用于日志可读性,不返客户端。
// 调用方已经在该 scene 内走完校验 / 业务逻辑,err 非 nil 时进入本函数统一出栈。
func (h *Handler) handleError(c *gin.Context, scene string, err error) {
	ctx := c.Request.Context()
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"scene": scene, "user_id": userID}

	switch {
	case errors.Is(err, document.ErrDocumentInvalidInput):
		h.log.WarnCtx(ctx, "文档请求参数无效", fields)
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: document.CodeDocumentInvalidInput, Message: "Invalid request",
		})
	case errors.Is(err, document.ErrDocumentNotFound):
		h.log.WarnCtx(ctx, "文档不存在", fields)
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: document.CodeDocumentNotFound, Message: "Document not found",
		})
	case errors.Is(err, document.ErrDimMismatch):
		// 理论上装配层会 fatal,不会漏到 handler;放这里保完整性
		h.log.ErrorCtx(ctx, "文档向量维度不一致", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	case errors.Is(err, document.ErrDocumentInternal):
		h.log.ErrorCtx(ctx, "文档模块内部错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	default:
		h.log.ErrorCtx(ctx, "未映射的文档模块错误", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
