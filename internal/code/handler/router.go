// router.go code 模块 HTTP 路由注册。
//
// 当前挂:
//
//	GET /api/v2/orgs/:slug/code/repositories   列 org 下已同步的代码仓库
//	GET /api/v2/orgs/:slug/code/search         跨 repo 代码语义+符号检索
//
// 权限:JWT + org member(OrgContextMiddleware 校验)。不要求 integration.manage 权限,
// 任何 org 成员都能搜组织知识库。
// search 端点在后端未配 embedder 时返 503。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgsvc "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
)

// RegisterRoutes 挂 code 模块所有路由。
func RegisterRoutes(
	router *gin.Engine,
	h *Handler,
	jwtManager *utils.JWTManager,
	orgSvc orgsvc.OrgService,
	roleSvc orgsvc.RoleService,
	log logger.LoggerInterface,
) {
	g := router.Group("/api/v2/orgs/:slug/code")
	g.Use(
		middleware.JWTAuth(jwtManager),
		orghandler.OrgContextMiddleware(orgSvc, roleSvc, log),
	)
	g.GET("/repositories", h.ListRepositories)
	g.GET("/search", h.Search)
}
