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

// ── channel ↔ version 多对多关联的 3 个 handler 已废弃 ──────────────────────
// AttachChannelVersion / DetachChannelVersion / ListChannelVersions 整体退役;
// channel.workstream_id → workstream.version_id 是新模型的单向引用,Version 信息
// 通过 pm 模块查 workstream 反查得到。前端如有渲染需要,改调 pm /api/v2/versions
// 路由或 workstream 详情接口(PR-B 落地)。
