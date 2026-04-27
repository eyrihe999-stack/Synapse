// router.go channel 模块路由注册。
//
// 路由分组(全部需要 JWT 登录):
//
//  /api/v2/projects
//    POST   /                       — 新建 project(body.org_id)
//    GET    /?org_id=X              — 列出 org 的 project
//    GET    /:id                    — 获取单个 project
//    POST   /:id/archive            — 归档 project
//    POST   /:id/versions           — 新建 version 挂在 project 下
//    GET    /:id/versions           — 列出 project 的 version
//    GET    /:id/channels           — 列出 project 的 channel
//
//  /api/v2/channels
//    POST   /                       — 新建 channel(body.project_id)
//    GET    /:id                    — 获取 channel
//    POST   /:id/archive            — 归档 channel
//    GET    /:id/members            — 列出成员
//    POST   /:id/members            — 加成员
//    DELETE /:id/members/:principal_id
//    PATCH  /:id/members/:principal_id/role
//    GET    /:id/versions           — 列出 channel 关联的 version
//    POST   /:id/versions/:version_id   — 关联 version
//    DELETE /:id/versions/:version_id   — 取消关联
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

// RegisterRoutes 把 channel 模块的所有 endpoint 挂到 gin.Engine。
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

		// version 挂在 project 下
		projects.POST("/:id/versions", h.CreateVersion)
		projects.GET("/:id/versions", h.ListVersionsByProject)

		// channel 列表查 project 下的
		projects.GET("/:id/channels", h.ListChannelsByProject)
	}

	channels := router.Group("/api/v2/channels")
	channels.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		channels.POST("", h.CreateChannel)
		channels.GET("/:id", h.GetChannel)
		channels.POST("/:id/archive", h.ArchiveChannel)

		// 成员
		channels.GET("/:id/members", h.ListChannelMembers)
		channels.POST("/:id/members", h.AddChannelMember)
		channels.DELETE("/:id/members/:principal_id", h.RemoveChannelMember)
		channels.PATCH("/:id/members/:principal_id/role", h.UpdateChannelMemberRole)

		// 关联 version
		channels.GET("/:id/versions", h.ListChannelVersions)
		channels.POST("/:id/versions/:version_id", h.AttachChannelVersion)
		channels.DELETE("/:id/versions/:version_id", h.DetachChannelVersion)

		// 消息(PR #4' 起)
		channels.POST("/:id/messages", h.PostChannelMessage)
		channels.GET("/:id/messages", h.ListChannelMessages)

		// KB 挂载(PR #4' 起)
		channels.POST("/:id/kb-refs", h.AddChannelKBRef)
		channels.GET("/:id/kb-refs", h.ListChannelKBRefs)
		channels.DELETE("/:id/kb-refs/:ref_id", h.RemoveChannelKBRef)

		// 共享文档(PR #9'):channel 内多人共建文档,独占编辑锁
		channels.POST("/:id/documents", h.CreateChannelDocument)
		channels.GET("/:id/documents", h.ListChannelDocuments)
		channels.GET("/:id/documents/:doc_id", h.GetChannelDocument)
		channels.GET("/:id/documents/:doc_id/content", h.GetChannelDocumentContent)
		channels.DELETE("/:id/documents/:doc_id", h.DeleteChannelDocument)
		channels.POST("/:id/documents/:doc_id/lock", h.AcquireChannelDocumentLock)
		channels.POST("/:id/documents/:doc_id/lock/heartbeat", h.HeartbeatChannelDocumentLock)
		channels.DELETE("/:id/documents/:doc_id/lock", h.ReleaseChannelDocumentLock)
		channels.POST("/:id/documents/:doc_id/lock/force", h.ForceReleaseChannelDocumentLock)
		channels.POST("/:id/documents/:doc_id/versions", h.SaveChannelDocumentVersion)
		channels.GET("/:id/documents/:doc_id/versions", h.ListChannelDocumentVersions)
		channels.GET("/:id/documents/:doc_id/versions/:version_id/content", h.GetChannelDocumentVersionContent)
		// OSS 直传(PR #15'):request-url 拿预签名 PUT URL,客户端 PUT 字节到 OSS 后调 upload-commit
		channels.POST("/:id/documents/:doc_id/upload-url", h.RequestChannelDocumentUploadURL)
		channels.POST("/:id/documents/:doc_id/upload-commit", h.CommitChannelDocumentUpload)
		// OSS 直拉(PR #15' 第二弹):presigned GET URL,客户端 curl 直接下载,字节不经 server
		channels.POST("/:id/documents/:doc_id/download-url", h.RequestChannelDocumentDownloadURL)

		// 频道附件:Markdown 内嵌图片(PR #16')。
		// upload-url + upload-commit 链路镜像 doc;GET /:att_id 鉴权后 302 到 OSS 签名 URL。
		channels.POST("/:id/attachments/upload-url", h.RequestChannelAttachmentUploadURL)
		channels.POST("/:id/attachments/upload-commit", h.CommitChannelAttachmentUpload)
		channels.GET("/:id/attachments/:att_id", h.GetChannelAttachment)
	}

	// 消息表情反应(PR #12')—— 路径不嵌 channel_id,因为前端只持有 message_id
	messages := router.Group("/api/v2/messages")
	messages.Use(middleware.JWTAuthWithSession(jwtManager, sessionStore))
	{
		messages.POST("/:id/reactions", h.AddReaction)
		// emoji 走 path param,多字节字符前端需要 encodeURIComponent
		messages.DELETE("/:id/reactions/:emoji", h.RemoveReaction)
	}
}
