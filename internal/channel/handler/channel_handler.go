package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// CreateChannel POST /api/v2/channels
func (h *Handler) CreateChannel(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	var req dto.CreateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	ch, err := h.svc.Channel.Create(c.Request.Context(), req.ProjectID, userID, req.Name, req.Purpose)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "channel created", dto.ToChannelResponse(ch))
}

// GetChannel GET /api/v2/channels/:id
func (h *Handler) GetChannel(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	ch, err := h.svc.Channel.Get(c.Request.Context(), id)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToChannelResponse(ch))
}

// ListChannelsByProject GET /api/v2/projects/:id/channels
func (h *Handler) ListChannelsByProject(c *gin.Context) {
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	cs, err := h.svc.Channel.ListByProject(c.Request.Context(), projectID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToChannelListResponse(cs))
}

// ArchiveChannel POST /api/v2/channels/:id/archive
func (h *Handler) ArchiveChannel(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Channel.Archive(c.Request.Context(), id, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "channel archived", nil)
}

// AttachChannelVersion POST /api/v2/channels/:id/versions/:version_id
func (h *Handler) AttachChannelVersion(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	versionID, ok := parseUint64Param(c, "version_id")
	if !ok {
		return
	}
	if err := h.svc.Channel.AttachVersion(c.Request.Context(), channelID, versionID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "attached", nil)
}

// DetachChannelVersion DELETE /api/v2/channels/:id/versions/:version_id
func (h *Handler) DetachChannelVersion(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	versionID, ok := parseUint64Param(c, "version_id")
	if !ok {
		return
	}
	if err := h.svc.Channel.DetachVersion(c.Request.Context(), channelID, versionID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "detached", nil)
}

// ListChannelVersions GET /api/v2/channels/:id/versions
func (h *Handler) ListChannelVersions(c *gin.Context) {
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	vs, err := h.svc.Channel.ListVersions(c.Request.Context(), channelID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToVersionListResponse(vs))
}
