// attachment_handler.go channel 级附件 HTTP 端点。
//
// 路径前缀 /api/v2/channels/:id/attachments,见 router.go。
//
// 链路:
//   - POST   /upload-url      请求 presign PUT URL + commit_token
//   - POST   /upload-commit   client PUT 完后通知服务端落 channel_attachments 行
//   - GET    /:att_id         鉴权后 stream proxy OSS 字节(Markdown 内嵌图片用)
//
// 为什么 GET 走 stream proxy 而不是 302 redirect 到 OSS 签名 URL:
//   浏览器原生 <img src="/api/v2/...">**不会带 Authorization header**,直接 401。
//   如果让前端 fetch 转 blob 再 <img>,跨域 follow 后端 302 又要求 OSS 配 CORS。
//   stream proxy 让全程同源 + 走 server 鉴权,前端 + OSS / nginx 都不用动配置。
//   代价:server 占双向带宽。但图片 ≤ 10MB 量级可接受。
//
// 权限校验全部在 service 层,handler 只解参 + 翻错。
package handler

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// attachmentURL 拼相对 URL,放进响应体让 markdown 直接引用:
//
//	/api/v2/channels/<cid>/attachments/<aid>
//
// 前端 <img> 拉这个相对路径 → 浏览器自带 cookie/Authorization → 后端鉴权 → 302。
func attachmentURL(channelID, attachmentID uint64) string {
	return fmt.Sprintf("/api/v2/channels/%d/attachments/%d", channelID, attachmentID)
}

// RequestChannelAttachmentUploadURL POST /api/v2/channels/:id/attachments/upload-url
//
// 拿 OSS presign PUT URL + commit_token。channel 成员 + channel 未归档可调。
// 客户端 PUT 时必须带 Content-Type: <返回的 content_type>(签名绑定)。
func (h *Handler) RequestChannelAttachmentUploadURL(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.RequestChannelAttachmentUploadURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	out, err := h.svc.Attachment.PresignUpload(c.Request.Context(), service.PresignAttachmentUploadInput{
		ChannelID:   channelID,
		ActorUserID: userID,
		MimeType:    req.MimeType,
		Filename:    req.Filename,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "presigned", dto.ChannelAttachmentUploadURLResponse{
		UploadURL:   out.UploadURL,
		CommitToken: out.CommitToken,
		ContentType: out.ContentType,
		ExpiresAt:   out.ExpiresAt,
		MaxByteSize: out.MaxByteSize,
	})
}

// CommitChannelAttachmentUpload POST /api/v2/channels/:id/attachments/upload-commit
//
// commit 阶段:验 token + HEAD/StreamGet OSS + dedup + 写 channel_attachments 行。
// 同 (channel_id, sha256) 已存在 → 幂等返已有 + Reused=true,删新上传对象。
func (h *Handler) CommitChannelAttachmentUpload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.CommitChannelAttachmentUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	out, err := h.svc.Attachment.CommitUpload(c.Request.Context(), service.CommitAttachmentUploadInput{
		ChannelID:   channelID,
		ActorUserID: userID,
		CommitToken: req.CommitToken,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "committed", dto.CommitChannelAttachmentUploadResponse{
		Attachment: dto.ToChannelAttachmentResponse(&out.Attachment, attachmentURL(out.Attachment.ChannelID, out.Attachment.ID)),
		Reused:     out.Reused,
	})
}

// GetChannelAttachment GET /api/v2/channels/:id/attachments/:att_id
//
// 鉴权 + stream proxy OSS 字节。前端 <img src> 通过 fetch 转 blob 拉这个端点
// (使用 axios 自动带 JWT Bearer),server 同源响应字节。
//
// Cache-Control: private, max-age=300 让浏览器在 5min 内复用已下载的图。
//
// 允许 archived channel(读路径)。
func (h *Handler) GetChannelAttachment(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	attID, ok := parseUint64Param(c, "att_id")
	if !ok {
		return
	}
	stream, err := h.svc.Attachment.OpenForStream(c.Request.Context(), channelID, attID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	defer stream.Body.Close()

	c.Header("Content-Type", stream.MimeType)
	if stream.ByteSize > 0 {
		c.Header("Content-Length", strconv.FormatInt(stream.ByteSize, 10))
	}
	c.Header("Cache-Control", fmt.Sprintf("private, max-age=%d", chanerr.AttachmentDownloadCacheMaxAge))
	c.Status(http.StatusOK)
	if _, copyErr := io.Copy(c.Writer, stream.Body); copyErr != nil {
		// client 半路断开很常见(用户切页 / 关 tab),只 debug 级日志,别误报错误
		h.log.DebugCtx(c.Request.Context(), "channel: attachment stream copy interrupted", map[string]any{
			"attachment_id": attID, "err": copyErr.Error(),
		})
	}
}
