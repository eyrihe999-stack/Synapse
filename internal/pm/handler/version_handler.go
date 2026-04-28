package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/dto"
)

// CreateVersion POST /api/v2/projects/:id/versions
func (h *Handler) CreateVersion(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.CreateVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	v, err := h.svc.Version.Create(c.Request.Context(), projectID, userID, req.Name, req.Status)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "version created", dto.ToVersionResponse(v))
}

// ListVersionsByProject GET /api/v2/projects/:id/versions
func (h *Handler) ListVersionsByProject(c *gin.Context) {
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	vs, err := h.svc.Version.List(c.Request.Context(), projectID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToVersionListResponse(vs))
}

// GetVersion GET /api/v2/versions/:id
func (h *Handler) GetVersion(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	v, err := h.svc.Version.Get(c.Request.Context(), id)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToVersionResponse(v))
}

// UpdateVersion PATCH /api/v2/versions/:id
func (h *Handler) UpdateVersion(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	updates := map[string]any{}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.TargetDate != nil {
		updates["target_date"] = *req.TargetDate
	}
	if req.ReleasedAt != nil {
		updates["released_at"] = *req.ReleasedAt
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "no fields to update",
		})
		return
	}
	if err := h.svc.Version.Update(c.Request.Context(), id, userID, updates); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "version updated", nil)
}
