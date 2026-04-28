package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/dto"
)

// CreateInitiative POST /api/v2/projects/:id/initiatives
func (h *Handler) CreateInitiative(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.CreateInitiativeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	i, err := h.svc.Initiative.Create(c.Request.Context(), projectID, userID, req.Name, req.Description, req.TargetOutcome)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "initiative created", dto.ToInitiativeResponse(i))
}

// ListInitiativesByProject GET /api/v2/projects/:id/initiatives
func (h *Handler) ListInitiativesByProject(c *gin.Context) {
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	is, err := h.svc.Initiative.List(c.Request.Context(), projectID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToInitiativeListResponse(is))
}

// GetInitiative GET /api/v2/initiatives/:id
func (h *Handler) GetInitiative(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	i, err := h.svc.Initiative.Get(c.Request.Context(), id)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToInitiativeResponse(i))
}

// UpdateInitiative PATCH /api/v2/initiatives/:id
func (h *Handler) UpdateInitiative(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateInitiativeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.TargetOutcome != nil {
		updates["target_outcome"] = *req.TargetOutcome
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "no fields to update",
		})
		return
	}
	if err := h.svc.Initiative.Update(c.Request.Context(), id, userID, updates); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "initiative updated", nil)
}

// ArchiveInitiative POST /api/v2/initiatives/:id/archive
func (h *Handler) ArchiveInitiative(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Initiative.Archive(c.Request.Context(), id, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "initiative archived", nil)
}
