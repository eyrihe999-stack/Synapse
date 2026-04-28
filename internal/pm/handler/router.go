// router.go pm 模块路由注册。
//
// 路由分组(全部需要 JWT 登录):
//
//  /api/v2/projects
//    POST   /                              — 新建 project(body.org_id)
//    GET    /?org_id=X                     — 列出 org 的 project
//    GET    /:id                           — 获取单个 project
//    POST   /:id/archive                   — 归档 project
//    POST   /:id/initiatives               — 在 project 下新建 initiative
//    GET    /:id/initiatives               — 列出 project 的 initiative
//    POST   /:id/versions                  — 新建 version 挂在 project 下
//    GET    /:id/versions                  — 列出 project 的 version
//    GET    /:id/workstreams               — 列出 project 下所有 workstream
//    POST   /:id/kb-refs                   — 给 project 挂 KB
//    GET    /:id/kb-refs                   — 列 project 的 KB 挂载
//
//  /api/v2/initiatives
//    GET    /:id                           — 获取 initiative
//    PATCH  /:id                           — 改 initiative
//    POST   /:id/archive                   — 归档 initiative
//    POST   /:id/workstreams               — 在 initiative 下建 workstream
//    GET    /:id/workstreams               — 列 initiative 的 workstream
//
//  /api/v2/versions
//    GET    /:id                           — 获取 version
//    PATCH  /:id                           — 改 version(status / target_date / released_at)
//    GET    /:id/workstreams               — 列 version 下交付的 workstream
//
//  /api/v2/workstreams
//    GET    /:id                           — 获取 workstream
//    PATCH  /:id                           — 改 workstream
//
//  /api/v2/project-kb-refs
//    DELETE /:ref_id                       — 卸载 KB 挂载
//
// 权限校验全部在 service 层(见 service/service.go 顶部注释),handler 只做
// 参数解析 + 错误翻译 + DTO 转换。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/user"
)

// RegisterRoutes 把 pm 模块的所有 endpoint 挂到 gin.Engine。
func RegisterRoutes(
	router *gin.Engine,
	h *Handler,
	jwtManager *jwt.JWTManager,
	sessionStore user.SessionStore,
) {
	projects := router.Group("/api/v2/projects")
	projects.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		projects.POST("", h.CreateProject)
		projects.GET("", h.ListProjects)
		projects.GET("/:id", h.GetProject)
		projects.POST("/:id/archive", h.ArchiveProject)

		// initiative 在 project 下嵌套
		projects.POST("/:id/initiatives", h.CreateInitiative)
		projects.GET("/:id/initiatives", h.ListInitiativesByProject)

		// version 在 project 下嵌套
		projects.POST("/:id/versions", h.CreateVersion)
		projects.GET("/:id/versions", h.ListVersionsByProject)

		// workstream 全局视图(by project)
		projects.GET("/:id/workstreams", h.ListWorkstreamsByProject)

		// project 级 KB 挂载
		projects.POST("/:id/kb-refs", h.AttachProjectKBRef)
		projects.GET("/:id/kb-refs", h.ListProjectKBRefs)

		// roadmap 聚合视图(initiatives + versions + workstreams 一次拉)
		projects.GET("/:id/roadmap", h.GetProjectRoadmap)
	}

	initiatives := router.Group("/api/v2/initiatives")
	initiatives.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		initiatives.GET("/:id", h.GetInitiative)
		initiatives.PATCH("/:id", h.UpdateInitiative)
		initiatives.POST("/:id/archive", h.ArchiveInitiative)

		// workstream 在 initiative 下嵌套
		initiatives.POST("/:id/workstreams", h.CreateWorkstream)
		initiatives.GET("/:id/workstreams", h.ListWorkstreamsByInitiative)
	}

	versions := router.Group("/api/v2/versions")
	versions.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		versions.GET("/:id", h.GetVersion)
		versions.PATCH("/:id", h.UpdateVersion)
		versions.GET("/:id/workstreams", h.ListWorkstreamsByVersion)
	}

	workstreams := router.Group("/api/v2/workstreams")
	workstreams.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		workstreams.GET("/:id", h.GetWorkstream)
		workstreams.PATCH("/:id", h.UpdateWorkstream)
	}

	kbRefs := router.Group("/api/v2/project-kb-refs")
	kbRefs.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		kbRefs.DELETE("/:ref_id", h.DetachProjectKBRef)
	}
}
