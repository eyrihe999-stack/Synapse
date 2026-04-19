// error_map.go service 层 sentinel 错误 → HTTP 响应映射。
package handler

import (
	"errors"
	"net/http"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// handleServiceError 把 service 层返回的 sentinel 映射成业务码 + HTTP 响应。
// 未命中时走 500,并在日志里带上 user_id 便于排查。
func (h *DocumentHandler) handleServiceError(c *gin.Context, err error) {
	userID, _ := middleware.GetUserID(c)
	fields := map[string]any{"user_id": userID}
	ctx := c.Request.Context()

	switch {
	// 400
	case errors.Is(err, document.ErrDocumentInvalidRequest):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentInvalidRequest, Message: "Invalid request"})
	case errors.Is(err, document.ErrDocumentTitleInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentTitleInvalid, Message: "Invalid title"})
	case errors.Is(err, document.ErrDocumentMIMETypeUnsupported):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentMIMETypeUnsupported, Message: "MIME type unsupported"})
	case errors.Is(err, document.ErrDocumentFileTooLarge):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentFileTooLarge, Message: "File too large"})
	case errors.Is(err, document.ErrDocumentEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentEmpty, Message: "Empty content"})

	// 403
	case errors.Is(err, document.ErrDocumentPermissionDenied):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentPermissionDenied, Message: "Permission denied"})

	// 404
	case errors.Is(err, document.ErrDocumentNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentNotFound, Message: "Document not found"})

	// 503
	case errors.Is(err, document.ErrDocumentStorageFailed):
		h.logger.ErrorCtx(ctx, "document: storage failed", err, fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentStorageFailed, Message: "Storage service failed"})
	case errors.Is(err, document.ErrDocumentIndexFailed):
		h.logger.ErrorCtx(ctx, "document: indexing failed", err, fields)
		c.JSON(http.StatusOK, response.BaseResponse{Code: document.CodeDocumentIndexFailed, Message: "Indexing service failed"})

	// 500
	case errors.Is(err, document.ErrDocumentInternal):
		h.logger.ErrorCtx(ctx, "document: internal error", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	default:
		h.logger.ErrorCtx(ctx, "document: unmapped error", err, fields)
		response.InternalServerError(c, "Internal server error", "")
	}
}
