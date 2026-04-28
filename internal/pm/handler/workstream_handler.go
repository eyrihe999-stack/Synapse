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

// CreateWorkstream POST /api/v2/initiatives/:id/workstreams
func (h *Handler) CreateWorkstream(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	initiativeID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.CreateWorkstreamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	w, err := h.svc.Workstream.Create(c.Request.Context(), initiativeID, userID, req.VersionID, req.Name, req.Description)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "workstream created", dto.ToWorkstreamResponse(w))
}

// ListWorkstreamsByInitiative GET /api/v2/initiatives/:id/workstreams
func (h *Handler) ListWorkstreamsByInitiative(c *gin.Context) {
	initiativeID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	ws, err := h.svc.Workstream.ListByInitiative(c.Request.Context(), initiativeID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToWorkstreamListResponse(ws))
}

// ListWorkstreamsByVersion GET /api/v2/versions/:id/workstreams
func (h *Handler) ListWorkstreamsByVersion(c *gin.Context) {
	versionID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	ws, err := h.svc.Workstream.ListByVersion(c.Request.Context(), versionID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToWorkstreamListResponse(ws))
}

// ListWorkstreamsByProject GET /api/v2/projects/:id/workstreams
func (h *Handler) ListWorkstreamsByProject(c *gin.Context) {
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	ws, err := h.svc.Workstream.ListByProject(c.Request.Context(), projectID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToWorkstreamListResponse(ws))
}

// GetWorkstream GET /api/v2/workstreams/:id
func (h *Handler) GetWorkstream(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	w, err := h.svc.Workstream.Get(c.Request.Context(), id)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToWorkstreamResponse(w))
}

// UpdateWorkstream PATCH /api/v2/workstreams/:id
func (h *Handler) UpdateWorkstream(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateWorkstreamRequest
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
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.VersionID != nil {
		// 指针非 nil:0 = 移到 backlog;非 0 = 改挂(service 层校验同 project)
		if *req.VersionID == 0 {
			updates["version_id"] = nil
		} else {
			updates["version_id"] = *req.VersionID
		}
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: pm.CodePMInvalidRequest, Message: "no fields to update",
		})
		return
	}
	if err := h.svc.Workstream.Update(c.Request.Context(), id, userID, updates); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "workstream updated", nil)
}
