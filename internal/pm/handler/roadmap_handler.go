package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm/dto"
)

// GetProjectRoadmap GET /api/v2/projects/:id/roadmap —— 返 project 的聚合视图。
//
// 用途:Web UI / Architect 客户端读"这个 project 当前 initiative × version 网格"
// 的统一入口。一次 HTTP 拿全 initiatives + versions + workstreams,前端不用拼三次
// 请求 + 自己交叉关联。
//
// 字段过滤(轻量,不进 DB 层):
//   - initiatives:archived_at IS NOT NULL 的不返
//   - versions:status='cancelled' 的不返
//   - workstreams:archived_at IS NOT NULL 的不返
//
// 不做 join / 关联:LLM / 前端按 id 自己关联(initiative_id / version_id 字段都
// 在每个 workstream 上,够用)。
func (h *Handler) GetProjectRoadmap(c *gin.Context) {
	if _, ok := middleware.GetUserID(c); !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	projectID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	inits, err := h.svc.Initiative.List(ctx, projectID, 200, 0)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	versions, err := h.svc.Version.List(ctx, projectID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	workstreams, err := h.svc.Workstream.ListByProject(ctx, projectID, 500, 0)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}

	// 过滤 + DTO 转换
	initOut := make([]dto.InitiativeResponse, 0, len(inits))
	for i := range inits {
		if inits[i].ArchivedAt != nil {
			continue
		}
		initOut = append(initOut, dto.ToInitiativeResponse(&inits[i]))
	}
	verOut := make([]dto.VersionResponse, 0, len(versions))
	for i := range versions {
		if versions[i].Status == "cancelled" {
			continue
		}
		verOut = append(verOut, dto.ToVersionResponse(&versions[i]))
	}
	wsOut := make([]dto.WorkstreamResponse, 0, len(workstreams))
	for i := range workstreams {
		if workstreams[i].ArchivedAt != nil {
			continue
		}
		wsOut = append(wsOut, dto.ToWorkstreamResponse(&workstreams[i]))
	}

	c.JSON(http.StatusOK, response.BaseResponse{
		Code:    200,
		Message: "ok",
		Result: map[string]any{
			"project_id":  projectID,
			"initiatives": initOut,
			"versions":    verOut,
			"workstreams": wsOut,
		},
	})
}
