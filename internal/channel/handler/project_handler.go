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

// CreateProject POST /api/v2/projects —— 新建 project。
func (h *Handler) CreateProject(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	var req dto.CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	p, err := h.svc.Project.Create(c.Request.Context(), req.OrgID, userID, req.Name, req.Description)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "project created", dto.ToProjectResponse(p))
}

// ListProjects GET /api/v2/projects?org_id=X&limit=Y&offset=Z
func (h *Handler) ListProjects(c *gin.Context) {
	if _, ok := middleware.GetUserID(c); !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	orgID, err := strconv.ParseUint(c.Query("org_id"), 10, 64)
	if err != nil || orgID == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "org_id query required",
		})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	ps, err := h.svc.Project.List(c.Request.Context(), orgID, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToProjectListResponse(ps))
}

// GetProject GET /api/v2/projects/:id
func (h *Handler) GetProject(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	p, err := h.svc.Project.Get(c.Request.Context(), id)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToProjectResponse(p))
}

// ArchiveProject POST /api/v2/projects/:id/archive
func (h *Handler) ArchiveProject(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Project.Archive(c.Request.Context(), id, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "project archived", nil)
}

// parseUint64Param 提取 gin path param 为 uint64;失败直接写 400 响应并返 false。
func parseUint64Param(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    chanerr.CodeChannelInvalidRequest,
			Message: "invalid path param: " + name,
		})
		return 0, false
	}
	return v, true
}
