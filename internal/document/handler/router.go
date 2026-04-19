// router.go document 模块路由注册。
//
// 路由结构:
//
//	/api/v2/orgs/:slug/documents           — 列表 / 上传
//	/api/v2/orgs/:slug/documents/:id       — 查询 / 改标题 / 删除
//	/api/v2/orgs/:slug/documents/:id/download — 下载原始文件
//
// 上传路由独立收紧 body 上限到 MaxUploadBodyBytes(20MB),避免全局 1MB 限制拦截。
package handler

import (
	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册 document 模块的所有路由。
func RegisterRoutes(
	router *gin.Engine,
	h *DocumentHandler,
	jwtManager *utils.JWTManager,
	orgPort service.OrgPort,
	log logger.LoggerInterface,
) {
	orgCtx := router.Group("/api/v2/orgs/:slug")
	orgCtx.Use(
		middleware.JWTAuth(jwtManager),
		OrgContextMiddleware(orgPort, log),
	)
	{
		// 列表(读权限)
		orgCtx.GET("/documents",
			PermissionMiddleware(document.PermDocumentRead, log),
			h.ListDocuments,
		)

		// Chunk 级语义检索(读权限)。
		// 必须注册在 /documents/:id 之前,让 gin 的 radix tree 把 "/documents/chunks"
		// 优先匹配到这个静态路径,不是作为 :id 参数被 GetDocument 处理。
		orgCtx.GET("/documents/chunks",
			PermissionMiddleware(document.PermDocumentRead, log),
			h.SearchChunks,
		)

		// 单个文档查询 / 下载(读权限)
		orgCtx.GET("/documents/:id",
			PermissionMiddleware(document.PermDocumentRead, log),
			h.GetDocument,
		)
		orgCtx.GET("/documents/:id/download",
			PermissionMiddleware(document.PermDocumentRead, log),
			h.DownloadDocument,
		)

		// 上传配置(读权限,给前端预过滤用)
		orgCtx.GET("/documents/config",
			PermissionMiddleware(document.PermDocumentRead, log),
			h.GetUploadConfig,
		)

		// 上传预检(写权限,要先有上传资格才能 precheck)
		orgCtx.POST("/documents/precheck",
			PermissionMiddleware(document.PermDocumentWrite, log),
			h.PrecheckUpload,
		)

		// 上传(写权限,body 上限放大到 MaxUploadBodyBytes)
		orgCtx.POST("/documents",
			middleware.MaxBodySize(document.MaxUploadBodyBytes),
			PermissionMiddleware(document.PermDocumentWrite, log),
			h.UploadDocument,
		)

		// 改标题(写权限)
		orgCtx.PATCH("/documents/:id",
			PermissionMiddleware(document.PermDocumentWrite, log),
			h.UpdateMetadata,
		)

		// 删除(删权限)
		orgCtx.DELETE("/documents/:id",
			PermissionMiddleware(document.PermDocumentDelete, log),
			h.DeleteDocument,
		)
	}
}
