// document_handler.go channel 共享文档(PR #9')HTTP 端点。
//
// 路径前缀 /api/v2/channels/:id/documents,见 router.go。
//
// 权限校验全部在 service 层,handler 只解参 + 翻错。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// CreateChannelDocument POST /api/v2/channels/:id/documents
//
// 创建空白共享文档。channel 成员都能创建。
func (h *Handler) CreateChannelDocument(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.CreateChannelDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	doc, err := h.svc.Document.Create(c.Request.Context(), channelID, userID, req.Title, req.ContentKind)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "document created", dto.ToChannelDocumentResponse(doc, nil))
}

// ListChannelDocuments GET /api/v2/channels/:id/documents
//
// 列 channel 下未软删的共享文档(公共空间视图)。channel 成员可读;archived 也可读。
func (h *Handler) ListChannelDocuments(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docs, err := h.svc.Document.List(c.Request.Context(), channelID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	resp := make([]dto.ChannelDocumentResponse, 0, len(docs))
	for i := range docs {
		resp = append(resp, dto.ToChannelDocumentResponse(&docs[i].Document, docs[i].Lock))
	}
	response.Success(c, "ok", resp)
}

// GetChannelDocument GET /api/v2/channels/:id/documents/:doc_id
//
// 读文档元数据 + 当前锁状态。content 单独走 .../content 拉,避免一次性大字节。
func (h *Handler) GetChannelDocument(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	detail, err := h.svc.Document.Get(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToChannelDocumentResponse(&detail.Document, detail.Lock))
}

// GetChannelDocumentContent GET /api/v2/channels/:id/documents/:doc_id/content
//
// 拉最新版字节(UTF-8 raw 字符串嵌在 JSON content 字段)。
func (h *Handler) GetChannelDocumentContent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	out, err := h.svc.Document.GetContent(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ChannelDocumentContentResponse{
		Document: dto.ToChannelDocumentResponse(&out.Document, nil),
		Version:  dto.ToChannelDocumentVersionResponse(&out.Version),
		Content:  string(out.Content),
	})
}

// DeleteChannelDocument DELETE /api/v2/channels/:id/documents/:doc_id
//
// 软删。仅创建者本人或 channel owner 可删;锁会被同时强制释放。
func (h *Handler) DeleteChannelDocument(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	if err := h.svc.Document.SoftDelete(c.Request.Context(), channelID, docID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "document deleted", nil)
}

// AcquireChannelDocumentLock POST /api/v2/channels/:id/documents/:doc_id/lock
//
// 抢锁:抢成功 / 别人持着 都返 200 + Result.lock(acquired=true/false 区分);
// 真正的错误(channel archived / not member / not found 等)走 sendServiceError。
//
// 把 LockHeld 当业务结果而不是错误,前端不必特例处理"业务码 + 拿 result"两条路径。
func (h *Handler) AcquireChannelDocumentLock(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	state, err := h.svc.Document.AcquireLock(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		// LockHeld 当业务结果:返 200 + acquired=false + 当前持锁人
		if errors.Is(err, chanerr.ErrChannelDocumentLockHeld) && state != nil {
			response.Success(c, "document locked by another principal", dto.LockOperationResponse{
				Lock: dto.ChannelDocumentLockResponse{
					HeldByPrincipalID: state.HeldByPrincipalID,
					LockedAt:          state.LockedAt,
					ExpiresAt:         state.ExpiresAt,
					Acquired:          false,
				},
			})
			return
		}
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "lock acquired", dto.LockOperationResponse{
		Lock: dto.ChannelDocumentLockResponse{
			HeldByPrincipalID: state.HeldByPrincipalID,
			LockedAt:          state.LockedAt,
			ExpiresAt:         state.ExpiresAt,
			Acquired:          true,
		},
	})
}

// HeartbeatChannelDocumentLock POST /api/v2/channels/:id/documents/:doc_id/lock/heartbeat
//
// 续锁。前端编辑期间每 60s 一次。caller 没持锁返 ErrChannelDocumentLockNotHeld。
func (h *Handler) HeartbeatChannelDocumentLock(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	state, err := h.svc.Document.HeartbeatLock(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "lock refreshed", dto.LockOperationResponse{
		Lock: dto.ChannelDocumentLockResponse{
			HeldByPrincipalID: state.HeldByPrincipalID,
			LockedAt:          state.LockedAt,
			ExpiresAt:         state.ExpiresAt,
			Acquired:          true,
		},
	})
}

// ReleaseChannelDocumentLock DELETE /api/v2/channels/:id/documents/:doc_id/lock
//
// 主动释放;非 owner 调返 nil(幂等)。
func (h *Handler) ReleaseChannelDocumentLock(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	if err := h.svc.Document.ReleaseLock(c.Request.Context(), channelID, docID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "lock released", nil)
}

// ForceReleaseChannelDocumentLock POST /api/v2/channels/:id/documents/:doc_id/lock/force
//
// 强制解锁:channel owner 任何时候 / 普通成员仅在锁过期后。
func (h *Handler) ForceReleaseChannelDocumentLock(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	if err := h.svc.Document.ForceReleaseLock(c.Request.Context(), channelID, docID, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "lock force-released", nil)
}

// RequestChannelDocumentUploadURL POST /api/v2/channels/:id/documents/:doc_id/upload-url
//
// 生成 OSS 直传预签名 URL + commit_token。客户端 PUT 字节到 OSS 后调 commit-upload。
// 不要求持锁(commit 时校验);要求 channel 成员 + channel 未归档 + doc 存在未删。
//
// Body 可选 `base_version` 字段实现乐观锁(RMW 模式必传)。空 body 直接 POST 也接受。
func (h *Handler) RequestChannelDocumentUploadURL(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	// body 可选 — ShouldBindJSON 在 body 空 / 只 {} 时不报错;只在格式错时报错(忽略)。
	var req dto.RequestChannelDocumentUploadURLRequest
	_ = c.ShouldBindJSON(&req)

	out, err := h.svc.Document.PresignUpload(c.Request.Context(), channelID, docID, userID, req.BaseVersion)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "presigned", dto.ChannelDocumentUploadURLResponse{
		UploadURL:   out.UploadURL,
		CommitToken: out.CommitToken,
		ContentType: out.ContentType,
		ExpiresAt:   out.ExpiresAt,
		MaxByteSize: out.MaxByteSize,
	})
}

// RequestChannelDocumentDownloadURL POST /api/v2/channels/:id/documents/:doc_id/download-url
//
// 生成 OSS GET 预签名 URL 直拉当前版本字节。读路径,允许 archived channel,
// 不要求持锁。doc 必须至少有一个版本(空文档拒)。
func (h *Handler) RequestChannelDocumentDownloadURL(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	out, err := h.svc.Document.PresignDownload(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "presigned", dto.ChannelDocumentDownloadURLResponse{
		DownloadURL: out.DownloadURL,
		Version:     out.Version,
		ByteSize:    out.ByteSize,
		ContentType: out.ContentType,
		ExpiresAt:   out.ExpiresAt,
	})
}

// CommitChannelDocumentUpload POST /api/v2/channels/:id/documents/:doc_id/upload-commit
//
// commit 阶段:验 token + 必持锁 + HEAD/StreamGet OSS + 写 version 行。
// 同 sha256 已存在 → 幂等返已有 + Created=false + 删新上传对象。
func (h *Handler) CommitChannelDocumentUpload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	var req dto.CommitChannelDocumentUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	out, err := h.svc.Document.CommitUpload(c.Request.Context(), service.CommitUploadInput{
		ChannelID:   channelID,
		DocumentID:  docID,
		ActorUserID: userID,
		CommitToken: req.CommitToken,
		EditSummary: req.EditSummary,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "committed", dto.CommitChannelDocumentUploadResponse{
		Document: dto.ToChannelDocumentResponse(&out.Document, nil),
		Version:  dto.ToChannelDocumentVersionResponse(&out.Version),
		Created:  out.Created,
	})
}

// SaveChannelDocumentVersion POST /api/v2/channels/:id/documents/:doc_id/versions
//
// 提交新版本。caller 必须持有未过期锁。同 sha256 已存在 → Created=false 幂等返已有版本。
func (h *Handler) SaveChannelDocumentVersion(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	var req dto.SaveChannelDocumentVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	out, err := h.svc.Document.SaveVersion(c.Request.Context(), service.SaveVersionInput{
		ChannelID:   channelID,
		DocumentID:  docID,
		ActorUserID: userID,
		Content:     []byte(req.Content),
		EditSummary: req.EditSummary,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "version saved", dto.SaveChannelDocumentVersionResponse{
		Document: dto.ToChannelDocumentResponse(&out.Document, nil),
		Version:  dto.ToChannelDocumentVersionResponse(&out.Version),
		Created:  out.Created,
	})
}

// ListChannelDocumentVersions GET /api/v2/channels/:id/documents/:doc_id/versions
//
// 列文档全部历史版本(append-only,id DESC)。
func (h *Handler) ListChannelDocumentVersions(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	vs, err := h.svc.Document.ListVersions(c.Request.Context(), channelID, docID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToChannelDocumentVersionListResponse(vs))
}

// GetChannelDocumentVersionContent GET /api/v2/channels/:id/documents/:doc_id/versions/:version_id/content
//
// 拉历史某版的字节(用于 diff / 回滚预览)。
func (h *Handler) GetChannelDocumentVersionContent(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUint64Param(c, "doc_id")
	if !ok {
		return
	}
	versionID, ok := parseUint64Param(c, "version_id")
	if !ok {
		return
	}
	out, err := h.svc.Document.GetVersionContent(c.Request.Context(), channelID, docID, versionID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ChannelDocumentContentResponse{
		Document: dto.ToChannelDocumentResponse(&out.Document, nil),
		Version:  dto.ToChannelDocumentVersionResponse(&out.Version),
		Content:  string(out.Content),
	})
}
