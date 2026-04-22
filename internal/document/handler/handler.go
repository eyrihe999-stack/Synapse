// Package handler document 模块 HTTP 入口。
//
// 路由挂在 org 上下文下:/api/v2/orgs/:slug/documents/*
// org 成员校验由 organization/handler.OrgContextMiddleware 统一处理,本 handler 假定已通过。
package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/runners/docupload"
	asyncsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/idgen"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	docsvc "github.com/eyrihe999-stack/Synapse/internal/document/service"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	permsvc "github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	srcsvc "github.com/eyrihe999-stack/Synapse/internal/source/service"
)

// maxUploadBytes upload 路由单请求 body 上限(10MB,决策 1)。
const maxUploadBytes = 10 * 1024 * 1024

// allowedExts 决策 5 定的 markdown + txt 扩展名白名单。
var allowedExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".mdx":      true,
	".txt":      true,
}

// Handler HTTP 入口。
type Handler struct {
	uploadSvc   docsvc.UploadService
	repo        docrepo.Repository
	asyncSvc    *asyncsvc.Service
	sourceSvc   srcsvc.SourceService    // M2:upload 前 lazy 确保用户的 manual_upload source
	permSvc     permsvc.PermissionService // M3:list/get/content/delete 走 ACL 过滤 + 单条 perm 判定
	oss         ossupload.Client
	ossPrefix   string // 从 cfg.OSS.PathPrefix 传入,作为所有 key 的顶层前缀
	maxVersions int    // 从 cfg.OSS.MaxVersionsPerDocument 传入
	log         logger.LoggerInterface
}

// New 构造。所有依赖都必填。
func New(
	uploadSvc docsvc.UploadService,
	repo docrepo.Repository,
	asyncSvc *asyncsvc.Service,
	sourceSvc srcsvc.SourceService,
	permSvc permsvc.PermissionService,
	oss ossupload.Client,
	ossPrefix string,
	maxVersions int,
	log logger.LoggerInterface,
) *Handler {
	return &Handler{
		uploadSvc:   uploadSvc,
		repo:        repo,
		asyncSvc:    asyncSvc,
		sourceSvc:   sourceSvc,
		permSvc:     permSvc,
		oss:         oss,
		ossPrefix:   ossPrefix,
		maxVersions: maxVersions,
		log:         log,
	}
}

// ─── Upload ──────────────────────────────────────────────────────────────────

// UploadResponse 三种成功返回共用。
//
//	Status = "already_indexed"    已存在同内容 doc,DocID 是已有 id,前端可直接跳转
//	Status = "queued"              新 / 覆盖任务已提交,JobID 非零,前端轮询该 job
//	Status = "filename_conflict"   同名 + 不同内容,前端弹 confirm 后带 overwrite=true 重试
//
// snowflake ID 走 `,string` tag 序列化成 JSON 字符串 —— 18 位 uint64 超过 JS Number 精度(2^53),
// 直接发数字会被 JSON.parse 截断末尾几位,前端据此再回请时就找不到记录。
type UploadResponse struct {
	Status           docsvc.UploadStatus `json:"status"`
	DocID            uint64              `json:"doc_id,string,omitempty"`
	JobID            uint64              `json:"job_id,string,omitempty"`
	ContentHash      string              `json:"content_hash,omitempty"`
	ExistingDocID    uint64              `json:"existing_doc_id,string,omitempty"`
	ExistingFileName string              `json:"existing_file_name,omitempty"`
}

// Upload POST /api/v2/orgs/:slug/documents/upload
//
// multipart/form-data:
//
//	file       required
//	title      optional
//	overwrite  optional, "true"/"1" → 强制覆盖同名不同内容的旧 doc
//	source_id  optional, 指定 doc 归属的 source.id(必须是 caller 作为 owner 的 source);
//	           留空 → 默认落入 caller 的 manual_upload source(lazy 创建)
//
// 响应:
//
//	200  {status: already_indexed, doc_id}
//	202  {status: queued, doc_id, job_id}
//	409  {status: filename_conflict, existing_doc_id, ...}
//	400/403/413/415/500 错误
func (h *Handler) Upload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "file field required", err.Error())
		return
	}
	if fileHeader.Size > maxUploadBytes {
		h.log.WarnCtx(c.Request.Context(), "upload too large", map[string]any{"size": fileHeader.Size})
		c.JSON(http.StatusRequestEntityTooLarge, response.BaseResponse{
			Code: http.StatusRequestEntityTooLarge, Message: "file too large, max 10MB",
		})
		return
	}

	fileName := fileHeader.Filename
	if !isAllowedExt(fileName) {
		c.JSON(http.StatusUnsupportedMediaType, response.BaseResponse{
			Code:    http.StatusUnsupportedMediaType,
			Message: "unsupported file type, allowed: .md .markdown .mdx .txt",
		})
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		response.InternalServerError(c, "open upload file", err.Error())
		return
	}
	//sayso-lint:ignore defer-err
	defer f.Close()

	content, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		response.BadRequest(c, "read file body failed", err.Error())
		return
	}
	if int64(len(content)) > maxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, response.BaseResponse{
			Code: http.StatusRequestEntityTooLarge, Message: "file too large, max 10MB",
		})
		return
	}

	title := strings.TrimSpace(c.PostForm("title"))
	mime := fileHeader.Header.Get("Content-Type")
	overwrite := parseBool(c.PostForm("overwrite"))

	// source_id 可选:指定 doc 归属的 source。必须是 caller 作为 owner 的 source。
	// 非 owner 一律拒绝(不查 ACL);这是本阶段的硬规则,见 PRD "指定 source 的权限门槛 = 只 owner"。
	// 留空 → StatusReady 分支走 EnsureManualUpload 兜底。
	var explicitSourceID uint64
	if raw := strings.TrimSpace(c.PostForm("source_id")); raw != "" {
		sid, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid source_id", err.Error())
			return
		}
		src, err := h.sourceSvc.GetSource(c.Request.Context(), org.ID, sid)
		if err != nil {
			// GetSource 返 ErrSourceNotFound / ErrSourceInternal,这里不透 source 模块错误语义,
			// 统一映射成 upload 视角的 400/403/500:不存在 / 越 org 一律 404,内部错走 500。
			if errors.Is(err, source.ErrSourceNotFound) {
				c.JSON(http.StatusNotFound, response.BaseResponse{
					Code: http.StatusNotFound, Message: "source not found",
				})
				return
			}
			response.InternalServerError(c, "load source failed", err.Error())
			return
		}
		if src.OwnerUserID != userID {
			h.log.WarnCtx(c.Request.Context(), "upload: 非 owner 尝试指定 source", map[string]any{
				"source_id": sid, "caller": userID, "owner": src.OwnerUserID,
			})
			c.JSON(http.StatusForbidden, response.BaseResponse{
				Code: http.StatusForbidden, Message: "only source owner can upload into it",
			})
			return
		}
		explicitSourceID = sid
	}

	outcome, err := h.uploadSvc.PrepareUpload(c.Request.Context(), docsvc.PrepareUploadInput{
		OrgID:      org.ID,
		UploaderID: userID,
		FileName:   fileName,
		Content:    content,
		Overwrite:  overwrite,
	})
	if err != nil {
		h.handleError(c, "prepare upload", err)
		return
	}

	switch outcome.Status {
	case docsvc.StatusAlreadyIndexed:
		// 同内容命中去重 —— OSS 里必然已有老版本对应的 key,不重复上传。
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: http.StatusOK, Message: "already indexed",
			Result: UploadResponse{
				Status:      outcome.Status,
				DocID:       outcome.DocID,
				ContentHash: outcome.ContentHash,
			},
		})
		return

	case docsvc.StatusFilenameConflict:
		c.JSON(http.StatusConflict, response.BaseResponse{
			Code: http.StatusConflict, Message: "filename conflict, pass overwrite=true to replace",
			Result: UploadResponse{
				Status:           outcome.Status,
				ContentHash:      outcome.ContentHash,
				ExistingDocID:    outcome.ExistingDocID,
				ExistingFileName: outcome.ExistingFileName,
			},
		})
		return

	case docsvc.StatusReady:
		// 解析 doc 归属的 source.id:
		//   - 用户显式传了 source_id(上方已校验 owner 合法)→ 直接用
		//   - 没传 → 走 EnsureManualUpload 兜底(lazy 创建默认收件箱)
		// 得到的 source.id 作为 doc.knowledge_source_id 传给 pipeline。
		var knowledgeSourceID uint64
		if explicitSourceID != 0 {
			knowledgeSourceID = explicitSourceID
		} else {
			sourceResp, err := h.sourceSvc.EnsureManualUpload(c.Request.Context(), org.ID, userID)
			if err != nil {
				h.log.ErrorCtx(c.Request.Context(), "upload: ensure manual_upload source failed", err, map[string]any{
					"org_id": org.ID, "user_id": userID,
				})
				response.InternalServerError(c, "ensure source failed", err.Error())
				return
			}
			knowledgeSourceID = sourceResp.ID
		}

		// 1) 上传 OSS(同步完成才进 asyncjob,否则 runner 拉不到会失败)
		ossKey := buildOSSKey(h.ossPrefix, org.ID, outcome.DocID, outcome.ContentHash, fileName)
		if _, err := h.oss.PutObject(c.Request.Context(), ossKey, content, mimeOrDefault(mime)); err != nil {
			h.log.ErrorCtx(c.Request.Context(), "upload: oss put failed", err, map[string]any{
				"org_id": org.ID, "doc_id": outcome.DocID, "oss_key": ossKey,
			})
			response.InternalServerError(c, "oss upload failed", err.Error())
			return
		}

		// 2) 记录版本 + 裁剪旧版本。失败只 warn 不回滚 —— 主流程已经上 OSS 成功。
		if err := h.trackVersionAndPrune(c, org.ID, outcome.DocID, ossKey, outcome.ContentHash, len(content)); err != nil {
			h.log.WarnCtx(c.Request.Context(), "upload: track/prune version failed (non-fatal)", map[string]any{
				"org_id": org.ID, "doc_id": outcome.DocID, "err": err.Error(),
			})
		}

		// 3) 调 asyncjob.Schedule → runner 从 OSS 拉回字节跑 pipeline
		payload := docupload.Input{
			OrgID:             org.ID,
			UploaderID:        userID,
			DocID:             outcome.DocID,
			KnowledgeSourceID: knowledgeSourceID,
			FileName:          fileName,
			Title:             title,
			MIMEType:          mime,
			ContentHash:       outcome.ContentHash,
			OSSKey:            ossKey,
		}
		//sayso-lint:ignore err-shadow
		job, err := h.asyncSvc.Schedule(c.Request.Context(), asyncsvc.ScheduleInput{
			OrgID:   org.ID,
			UserID:  userID,
			Kind:    docupload.Kind,
			Payload: payload,
		})
		if err != nil {
			// docupload runner 实现 ConcurrentRunner,理论上不会 ErrDuplicateJob。
			// 若仍返错,给客户端透出(OSS 对象变成孤儿,后续靠 GC 兜底,短期内忽略)。
			h.log.ErrorCtx(c.Request.Context(), "schedule docupload failed", err, map[string]any{
				"org_id": org.ID, "doc_id": outcome.DocID,
			})
			response.InternalServerError(c, "schedule upload task", err.Error())
			return
		}
		c.JSON(http.StatusAccepted, response.BaseResponse{
			Code: http.StatusAccepted, Message: "queued",
			Result: UploadResponse{
				Status:      "queued",
				DocID:       outcome.DocID,
				JobID:       job.ID,
				ContentHash: outcome.ContentHash,
			},
		})
		return
	}

	// unreachable
	response.InternalServerError(c, "unknown upload outcome", "")
}

// trackVersionAndPrune 插入 document_versions 一行 + 裁剪超出 MaxVersions 的最老版本(OSS + DB 双清)。
// MaxVersions ≤ 0 时跳过裁剪(配置关掉)。
func (h *Handler) trackVersionAndPrune(c *gin.Context, orgID, docID uint64, ossKey, hash string, fileSize int) error {
	versionID, err := idgen.GenerateID()
	if err != nil {
		return fmt.Errorf("generate version id: %w", err)
	}
	v := &docmodel.DocumentVersion{
		ID:          uint64(versionID),
		DocID:       docID,
		OrgID:       orgID,
		OSSKey:      ossKey,
		VersionHash: hash,
		FileSize:    fileSize,
	}
	if err := h.repo.InsertVersion(c.Request.Context(), v); err != nil {
		return fmt.Errorf("insert version: %w", err)
	}

	if h.maxVersions <= 0 {
		return nil
	}
	prunedKeys, err := h.repo.PruneOldVersions(c.Request.Context(), docID, h.maxVersions)
	if err != nil {
		return fmt.Errorf("prune old versions: %w", err)
	}
	// OSS 删失败只 warn —— DB 已清,最多是孤儿 OSS 对象,后续可加 GC cron 清。
	for _, k := range prunedKeys {
		if delErr := h.oss.DeleteObject(c.Request.Context(), k); delErr != nil {
			h.log.WarnCtx(c.Request.Context(), "upload: prune oss object failed", map[string]any{
				"doc_id": docID, "oss_key": k, "err": delErr.Error(),
			})
		}
	}
	return nil
}

// ─── List / Get / Delete / Content ──────────────────────────────────────────

// ListDocsResponse 列表响应。next_cursor 非零时前端可继续翻页。
type ListDocsResponse struct {
	Docs       []DocumentDTO `json:"docs"`
	NextCursor uint64        `json:"next_cursor,string,omitempty"`
}

// DocumentDTO List / Get 的 JSON 返回体,只暴露前端需要的元数据字段。
// ID / UploaderID 走 `,string` tag:见 UploadResponse 注释。
type DocumentDTO struct {
	ID              uint64 `json:"id,string"`
	Title           string `json:"title"`
	FileName        string `json:"file_name"`
	Provider        string `json:"provider"`
	MIMEType        string `json:"mime_type"`
	Version         string `json:"version"`
	ChunkCount      int    `json:"chunk_count"`
	ContentByteSize int    `json:"content_byte_size"`
	UploaderID      uint64 `json:"uploader_id,string"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// List 列出当前 org 下的文档。
//
// 路由 GET /api/v2/orgs/:slug/documents?provider=&before_id=&limit=&doc_id=&source_id=&q=
// 按 id DESC + keyset 分页;next_cursor 非零时前端可带回继续翻页。
//
// 搜索支持三种互斥模式(前端切换):
//   - q=:       LOWER(title/file_name) LIKE 模糊匹配
//   - doc_id=:  按 doc.id 精确匹配(仍受 visible source 约束)
//   - source_id=: 按 doc.knowledge_source_id 精确匹配;source 不在可见集 → 返空
//
// M3:列表只返该 user 在该 org 内 read 权限可见的文档。
// 通过 permSvc.VisibleSourceIDsInOrg 拿到可见 source_id 集合,作为 IN 过滤项传 repo。
func (h *Handler) List(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}

	// M3 ACL 过滤:先算用户可见的 source ids
	visibleSourceIDs, err := h.permSvc.VisibleSourceIDsInOrg(c.Request.Context(), org.ID, userID, permsvc.PermRead)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "compute visible source ids failed", err, map[string]any{
			"org_id": org.ID, "user_id": userID,
		})
		response.InternalServerError(c, "permission check failed", err.Error())
		return
	}

	opts := docrepo.ListOptions{
		Provider:           c.Query("provider"),
		Query:              c.Query("q"),
		KnowledgeSourceIDs: visibleSourceIDs, // 空 slice 也传 → repo 走短路返空
	}
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			opts.Limit = n
		}
	}
	if raw := c.Query("before_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			opts.BeforeID = n
		}
	}
	if raw := c.Query("doc_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			opts.DocID = n
		}
	}
	if raw := c.Query("source_id"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			opts.KnowledgeSourceID = n
		}
	}
	docs, err := h.repo.ListByOrg(c.Request.Context(), org.ID, opts)
	if err != nil {
		response.InternalServerError(c, "list docs", err.Error())
		return
	}

	out := make([]DocumentDTO, 0, len(docs))
	for _, d := range docs {
		out = append(out, toDTO(d))
	}
	var nextCursor uint64
	if len(out) == opts.Limit && opts.Limit > 0 {
		nextCursor = out[len(out)-1].ID
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: ListDocsResponse{Docs: out, NextCursor: nextCursor},
	})
}

// Get 查单条文档详情,并附带 chunks indexed/failed 计数。
//
// 路由 GET /api/v2/orgs/:slug/documents/:id
// doc 不存在 → 404;chunks 计数失败不影响主返回(只日志告警)。
//
// M3:doc 加载后调 permSvc.PermOnSource 校验 read 权限,perm=none → 404
// (与"不存在"等价表达,避免 leak doc 存在性)。
func (h *Handler) Get(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	d, err := h.repo.GetByID(c.Request.Context(), org.ID, id)
	if err != nil {
		h.handleError(c, "get doc", err)
		return
	}
	if !h.requireDocPerm(c, d, permsvc.PermRead) {
		return
	}
	indexed, failed, countErr := h.repo.CountChunks(c.Request.Context(), d.ID)
	if countErr != nil {
		// 非关键路径:计数失败仍返主数据,只记日志不回 500
		h.log.WarnCtx(c.Request.Context(), "count chunks failed", map[string]any{
			"doc_id": d.ID, "err": countErr.Error(),
		})
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: gin.H{
			"doc":            toDTO(d),
			"chunks_indexed": indexed,
			"chunks_failed":  failed,
		},
	})
}

// Content GET /api/v2/orgs/:slug/documents/:id/content?version=<hash>
//
// 返回原文字节(默认最新版本;version 指定时按 version_hash 精确匹配历史版本)。
// bucket 私读假设:服务端拿 AK 代下载,前端不接触 OSS 直链。
// Cache-Control 长 cache:versionHash 变了 URL 变,天然 cache-bust。
func (h *Handler) Content(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	d, err := h.repo.GetByID(c.Request.Context(), org.ID, id)
	if err != nil {
		h.handleError(c, "get doc", err)
		return
	}
	if !h.requireDocPerm(c, d, permsvc.PermRead) {
		return
	}

	// 决定要拉哪个 oss_key
	ossKey := d.OSSKey
	reqVersion := strings.TrimSpace(c.Query("version"))
	if reqVersion != "" && reqVersion != d.Version {
		versions, err := h.repo.ListVersionsByDoc(c.Request.Context(), d.ID)
		if err != nil {
			response.InternalServerError(c, "list versions", err.Error())
			return
		}
		var matched *docmodel.DocumentVersion
		for _, v := range versions {
			if v.VersionHash == reqVersion {
				matched = v
				break
			}
		}
		if matched == nil {
			response.NotFound(c, "version not found", reqVersion)
			return
		}
		ossKey = matched.OSSKey
	}
	if ossKey == "" {
		// 历史数据 / 未完成上传时可能没 key。明确回 404 比 500 好定位。
		response.NotFound(c, "document content unavailable", "no oss key bound")
		return
	}

	data, err := h.oss.GetObject(c.Request.Context(), ossKey)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "content: oss get failed", err, map[string]any{
			"doc_id": d.ID, "oss_key": ossKey,
		})
		response.InternalServerError(c, "fetch content from oss", err.Error())
		return
	}

	ct := d.MIMEType
	if ct == "" {
		ct = "text/markdown; charset=utf-8"
	}
	// 版本化 URL:hash 不变内容不变,给长 max-age 让 CDN / 浏览器充分缓存
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Data(http.StatusOK, ct, data)
}

// DocumentVersionDTO 单条历史版本的展示 DTO。
// VersionHash 是 sha256 hex(128 字符),前端可截断展示前几位 + tooltip 完整 hash;
// IsCurrent 在服务端直接算好,避免前端再做 docs.version == versions.hash 的比较。
type DocumentVersionDTO struct {
	VersionHash string `json:"version_hash"`
	FileSize    int    `json:"file_size"`
	CreatedAt   int64  `json:"created_at"`
	IsCurrent   bool   `json:"is_current"`
}

// ListVersionsResponse /:id/versions 响应。
// 按 created_at DESC 排序,最新在前;list 大小受 cfg.OSS.MaxVersionsPerDocument 约束,
// 通常 ≤ 10,不做分页。
type ListVersionsResponse struct {
	Items []DocumentVersionDTO `json:"items"`
}

// ListVersions GET /api/v2/orgs/:slug/documents/:id/versions
//
// 列出该文档的所有历史版本(按 created_at DESC)。每条返回 hash + size + 创建时间 + 是否当前版本。
// 前端据此渲染"版本历史"面板,点某条即带 version=<hash> 走 /content 拉取该版本原文。
//
// 404 → doc 不存在;500 → repo 查失败。
func (h *Handler) ListVersions(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	// 先拿 doc 来知道"哪一条 hash 是当前版本"。顺便校验 doc 归属 org。
	d, err := h.repo.GetByID(c.Request.Context(), org.ID, id)
	if err != nil {
		h.handleError(c, "get doc", err)
		return
	}
	if !h.requireDocPerm(c, d, permsvc.PermRead) {
		return
	}

	versions, err := h.repo.ListVersionsByDoc(c.Request.Context(), id)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "list versions failed", err, map[string]any{
			"doc_id": id, "org_id": org.ID,
		})
		response.InternalServerError(c, "list versions", err.Error())
		return
	}

	out := make([]DocumentVersionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, DocumentVersionDTO{
			VersionHash: v.VersionHash,
			FileSize:    v.FileSize,
			CreatedAt:   v.CreatedAt.Unix(),
			IsCurrent:   v.VersionHash == d.Version,
		})
	}

	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: ListVersionsResponse{Items: out},
	})
}

// Delete 按 ID 删除文档(CASCADE 连带删 chunks + 版本记录行;OSS 对象由本 handler 显式删)。
//
// 路由 DELETE /api/v2/orgs/:slug/documents/:id
// 不存在视为幂等成功(repository 层已处理)。OSS 删失败只 warn。
//
// M3:删除需要 source.write 权限(source owner 或被授予 write 的 ACL)。
// 加载 doc → 校验 perm ≥ write,缺失 → 403/404 视情况。
func (h *Handler) Delete(c *gin.Context) {
	org, ok := orghandler.GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	// 先加载 doc 做权限判定。doc 不存在 → 走原路径(404 或幂等)。
	d, getErr := h.repo.GetByID(c.Request.Context(), org.ID, id)
	if getErr != nil {
		// 不存在 → 幂等成功;其他错走错误映射
		if errors.Is(getErr, document.ErrDocumentNotFound) {
			c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "deleted"})
			return
		}
		h.handleError(c, "get doc for delete", getErr)
		return
	}
	if !h.requireDocPerm(c, d, permsvc.PermWrite) {
		return
	}

	// 先列出所有版本 oss_key(后续要删)。不存在则列表为空,继续流程幂等。
	versions, listErr := h.repo.ListVersionsByDoc(c.Request.Context(), id)
	if listErr != nil && !errors.Is(listErr, document.ErrDocumentNotFound) {
		// 读失败只 warn,不阻塞删:宁肯留孤儿 OSS 也不让用户的"删"按钮卡住
		h.log.WarnCtx(c.Request.Context(), "delete: list versions failed (non-fatal)", map[string]any{
			"doc_id": id, "err": listErr.Error(),
		})
	}

	if err := h.repo.DeleteByID(c.Request.Context(), org.ID, id); err != nil {
		h.handleError(c, "delete doc", err)
		return
	}

	// DB 删除成功 → 清 OSS 对象。失败只 warn(孤儿对象,等 GC)。
	for _, v := range versions {
		if err := h.oss.DeleteObject(c.Request.Context(), v.OSSKey); err != nil {
			h.log.WarnCtx(c.Request.Context(), "delete: oss delete failed", map[string]any{
				"doc_id": id, "oss_key": v.OSSKey, "err": err.Error(),
			})
		}
	}

	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "deleted",
	})
}

// ─── helpers ────────────────────────────────────────────────────────────────

// buildOSSKey 拼 OSS 对象 key:`<prefix>/<orgID>/<docID>/<versionHash><.ext>`。
// 带扩展名便于肉眼 / 工具识别;prefix 为空时退化为 `<orgID>/<docID>/...`。
func buildOSSKey(prefix string, orgID, docID uint64, versionHash, fileName string) string {
	ext := fileExt(fileName)
	var b strings.Builder
	if prefix != "" {
		b.WriteString(strings.Trim(prefix, "/"))
		b.WriteByte('/')
	}
	b.WriteString(strconv.FormatUint(orgID, 10))
	b.WriteByte('/')
	b.WriteString(strconv.FormatUint(docID, 10))
	b.WriteByte('/')
	b.WriteString(versionHash)
	b.WriteString(ext)
	return b.String()
}

// fileExt 返回包含点号的小写扩展名(.md)或空串(无扩展)。和 allowedExts 的判断一致。
func fileExt(name string) string {
	lower := strings.ToLower(name)
	idx := strings.LastIndexByte(lower, '.')
	if idx < 0 {
		return ""
	}
	return lower[idx:]
}

// mimeOrDefault 浏览器填的 mime 空串时兜底 text/markdown。OSS 读回时依然会被
// documents.mime_type 覆盖,这里只是给 OSS 对象元数据一个合理的 Content-Type。
func mimeOrDefault(mime string) string {
	if mime == "" {
		return "text/markdown; charset=utf-8"
	}
	return mime
}

func isAllowedExt(name string) bool {
	ext := fileExt(name)
	if ext == "" {
		return false
	}
	return allowedExts[ext]
}

func parseBool(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func parseIDParam(c *gin.Context) (uint64, bool) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		response.BadRequest(c, "invalid id", raw)
		return 0, false
	}
	return id, true
}

// requireDocPerm 检查当前 user 对该 doc(经它的 source)是否有 minPerm(read / write)。
//
// 策略:
//   - PermOnSource 内部错误 → 500
//   - 完全无权限(PermNone) → 404,不 leak doc 存在性
//   - 有权限但级别不够(如只读用户尝试 write) → 403,给前端明确反馈
//
// 返 true 表示通过校验,可以继续;false 表示已经写过响应,handler 应直接 return。
//
//sayso-lint:ignore handler-no-response
func (h *Handler) requireDocPerm(c *gin.Context, d *docmodel.Document, minPerm string) bool {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return false
	}
	perm, err := h.permSvc.PermOnSource(c.Request.Context(), userID, d.KnowledgeSourceID)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "perm check failed", err, map[string]any{
			"doc_id": d.ID, "source_id": d.KnowledgeSourceID, "user_id": userID,
		})
		response.InternalServerError(c, "permission check failed", err.Error())
		return false
	}
	if !permGE(perm, minPerm) {
		h.log.WarnCtx(c.Request.Context(), "doc perm denied", map[string]any{
			"doc_id": d.ID, "source_id": d.KnowledgeSourceID, "user_id": userID,
			"want": minPerm, "have": perm,
		})
		if perm == permsvc.PermNone {
			response.NotFound(c, "document not found", "")
		} else {
			response.Forbidden(c, "insufficient permission", "")
		}
		return false
	}
	return true
}

// permGE 比较两个权限级别:got >= want?
//   - want=read:  got 是 read 或 write 都算
//   - want=write: 仅 got=write 算
func permGE(got, want string) bool {
	if want == permsvc.PermRead {
		return got == permsvc.PermRead || got == permsvc.PermWrite
	}
	if want == permsvc.PermWrite {
		return got == permsvc.PermWrite
	}
	return false
}

func toDTO(d *docmodel.Document) DocumentDTO {
	return DocumentDTO{
		ID:              d.ID,
		Title:           d.Title,
		FileName:        d.FileName,
		Provider:        d.Provider,
		MIMEType:        d.MIMEType,
		Version:         d.Version,
		ChunkCount:      d.ChunkCount,
		ContentByteSize: d.ContentByteSize,
		UploaderID:      d.UploaderID,
		CreatedAt:       d.CreatedAt.Unix(),
		UpdatedAt:       d.UpdatedAt.Unix(),
	}
}
