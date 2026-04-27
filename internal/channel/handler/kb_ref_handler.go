package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// AddChannelKBRef POST /api/v2/channels/:id/kb-refs
//
// body:{"kb_source_id": X} 或 {"kb_document_id": Y} —— 二选一。
// 只 channel owner 能加。
func (h *Handler) AddChannelKBRef(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.AddKBRefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}

	ref, err := h.svc.KBRef.Add(c.Request.Context(), channelID, userID, req.KBSourceID, req.KBDocumentID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "kb ref added", dto.ToKBRefResponse(ref))
}

// RemoveChannelKBRef DELETE /api/v2/channels/:id/kb-refs/:ref_id
//
// 只 channel owner 能删。ref 不属于该 channel → 404。
func (h *Handler) RemoveChannelKBRef(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	refID, ok := parseUint64Param(c, "ref_id")
	if !ok {
		return
	}
	if err := h.svc.KBRef.Remove(c.Request.Context(), channelID, refID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "kb ref removed", nil)
}

// ListChannelKBRefs GET /api/v2/channels/:id/kb-refs
//
// Channel 成员可读。channel archived 返空列表(应用层按 status 过滤,DB 行保留作审计)。
func (h *Handler) ListChannelKBRefs(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	rows, err := h.svc.KBRef.List(c.Request.Context(), channelID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToKBRefListResponse(rows))
}
