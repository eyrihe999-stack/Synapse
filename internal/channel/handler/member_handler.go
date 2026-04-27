package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// ListChannelMembers GET /api/v2/channels/:id/members
func (h *Handler) ListChannelMembers(c *gin.Context) {
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	ms, err := h.svc.Member.List(c.Request.Context(), channelID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToMemberListResponse(ms))
}

// AddChannelMember POST /api/v2/channels/:id/members
func (h *Handler) AddChannelMember(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.AddMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	m, err := h.svc.Member.Add(c.Request.Context(), channelID, userID, req.PrincipalID, req.Role)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "member added", dto.ToMemberResponse(m))
}

// RemoveChannelMember DELETE /api/v2/channels/:id/members/:principal_id
func (h *Handler) RemoveChannelMember(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	principalID, ok := parseUint64Param(c, "principal_id")
	if !ok {
		return
	}
	if err := h.svc.Member.Remove(c.Request.Context(), channelID, userID, principalID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "member removed", nil)
}

// UpdateChannelMemberRole PATCH /api/v2/channels/:id/members/:principal_id/role
func (h *Handler) UpdateChannelMemberRole(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	principalID, ok := parseUint64Param(c, "principal_id")
	if !ok {
		return
	}
	var req dto.UpdateMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	if err := h.svc.Member.UpdateRole(c.Request.Context(), channelID, userID, principalID, req.Role); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "role updated", nil)
}
