package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
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
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
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
