package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/dto"
)

// AttachProjectKBRef POST /api/v2/projects/:id/kb-refs
func (h *Handler) AttachProjectKBRef(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.AttachProjectKBRefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	ref, err := h.svc.ProjectKBRef.Attach(c.Request.Context(), projectID, userID, req.KBSourceID, req.KBDocumentID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "kb ref attached", dto.ToProjectKBRefResponse(ref))
}

// ListProjectKBRefs GET /api/v2/projects/:id/kb-refs
func (h *Handler) ListProjectKBRefs(c *gin.Context) {
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	refs, err := h.svc.ProjectKBRef.List(c.Request.Context(), projectID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToProjectKBRefListResponse(refs))
}

// DetachProjectKBRef DELETE /api/v2/project-kb-refs/:ref_id
func (h *Handler) DetachProjectKBRef(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	refID, ok := parseUint64Param(c, "ref_id")
	if !ok {
		return
	}
	if err := h.svc.ProjectKBRef.Detach(c.Request.Context(), refID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "kb ref detached", nil)
}
