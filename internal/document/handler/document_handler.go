// document_handler.go 文档资源的 HTTP handler(上传 / 列表 / 查询 / 下载 / 改标题 / 删除)。
package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/dto"
	"github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
	"github.com/gin-gonic/gin"
)

// UploadDocument POST /api/v2/orgs/:slug/documents
// 接受 multipart/form-data:file(必填) + title(可选,缺省用文件名去扩展名)。
func (h *DocumentHandler) UploadDocument(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "file is required (multipart/form-data field 'file')", err.Error())
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		title = defaultTitleFromFileName(fileHeader.Filename)
	}

	// target_doc_id 非空表示前端显式指定覆盖目标。空或 "0" 均等同未指定,走新建/dedup 分支。
	// 非法值(非数字)直接 400;不静默当 0 用,避免前端传错了但感知不到。
	var targetDocID uint64
	if raw := strings.TrimSpace(c.PostForm("target_doc_id")); raw != "" {
		//sayso-lint:ignore err-shadow
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			//sayso-lint:ignore gin-no-return
			response.BadRequest(c, "invalid target_doc_id", err.Error())
			return
		}
		targetDocID = parsed
	}

	f, err := fileHeader.Open()
	if err != nil {
		response.BadRequest(c, "failed to open uploaded file", err.Error())
		return
	}
	// multipart 读取完立即关闭;读失败会由 Open 本身报,这里 Close 失败不影响已读取字节。
	//sayso-lint:ignore defer-err
	defer f.Close()

	mime := fileHeader.Header.Get("Content-Type")
	if mime == "" {
		mime = guessMIME(fileHeader.Filename)
	}

	resp, err := h.svc.Upload(c.Request.Context(), service.UploadInput{
		OrgID:         org.ID,
		UploaderID:    userID,
		Title:         title,
		FileName:      fileHeader.Filename,
		MIMEType:      mime,
		ContentReader: f,
		TargetDocID:   targetDocID,
	})
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// GetDocument 返回单条文档元信息。路由:GET /api/v2/orgs/:slug/documents/:id。
func (h *DocumentHandler) GetDocument(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	docID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	resp, err := h.svc.Get(c.Request.Context(), org.ID, docID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// ListDocuments 列当前 org 的文档。支持两种模式,由 mode 查询参数选择:
//
//   mode=fuzzy(默认)—— MySQL LIKE 模糊匹配 title/file_name,支持 page+size 分页。
//   mode=semantic      —— pgvector 语义检索,支持 top_k(1..50),固定按相关度降序,不分页。
//
// 路由:GET /api/v2/orgs/:slug/documents?mode=&q=&page=&size=&top_k=
func (h *DocumentHandler) ListDocuments(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	mode := strings.TrimSpace(c.DefaultQuery("mode", document.SearchModeFuzzy))
	query := c.Query("q")

	if mode == document.SearchModeSemantic {
		//sayso-lint:ignore err-swallow
		topK, _ := strconv.Atoi(c.DefaultQuery("top_k", strconv.Itoa(document.DefaultSemanticTopK)))
		//sayso-lint:ignore err-shadow
		resp, err := h.svc.SemanticSearch(c.Request.Context(), org.ID, query, topK)
		if err != nil {
			h.handleServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
		return
	}

	// 默认 fuzzy 分支。Atoi 失败走 0,service 层会归一化成默认值。
	//sayso-lint:ignore err-swallow
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	//sayso-lint:ignore err-swallow
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(document.DefaultPageSize)))
	resp, err := h.svc.List(c.Request.Context(), org.ID, query, page, size)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// SearchChunks GET /api/v2/orgs/:slug/documents/chunks
//
// Chunk 级语义检索,不 dedup 同 doc 的多个 chunk。返回"命中片段列表"(不是"命中文档列表"),
// 每条带 doc_id + chunk_idx + content(原文段落)+ similarity + doc_title + doc_source。
// 前端可直接展示片段内容,不必下载原始 OSS 文件。
//
// Query 参数:
//
//	q      检索词。空串返空列表(非错);过长在 service 层按 MaxQueryLength 截断。
//	top_k  1..MaxSemanticTopK(50)。非法 / 缺省 → DefaultSemanticTopK(20)。
//
// 鲁棒性:
//   - 所有输入非法场景不 panic,走 service 层归一化或 sentinel 返回。
//   - 索引链路不可用(PG / embedder 未配) → ErrDocumentIndexFailed → 503 业务码,前端灰掉此功能。
//   - top_k 负数 / 字符串 / 超范围 → service 层 clamp 到默认,不返错误。
func (h *DocumentHandler) SearchChunks(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	query := c.Query("q")
	// Atoi 失败(含缺省)返回 0,service 层按 DefaultSemanticTopK 归一化。
	//sayso-lint:ignore err-swallow
	topK, _ := strconv.Atoi(c.DefaultQuery("top_k", "0"))

	// TODO(T1.4 → HTTP):当前 HTTP 端不暴露 metadata filter。agent 用例先走 Go API;
	// 需要时再从 query 参数解析(heading_path_contains=..., doc_ids=...)。
	resp, err := h.svc.SearchChunks(c.Request.Context(), org.ID, query, topK, nil)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// UpdateMetadata PATCH /api/v2/orgs/:slug/documents/:id
// Body: {"title": "新标题"}
func (h *DocumentHandler) UpdateMetadata(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	docID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateMetadataRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}
	resp, err := h.svc.UpdateMetadata(c.Request.Context(), org.ID, docID, req)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok", Result: resp})
}

// PrecheckUpload POST /api/v2/orgs/:slug/documents/precheck
//
// Body: { files: [{file_name, size_bytes, mime_type, content_hash}, ...] }
// Resp: { results: [{file_name, action, reason_code, existing?}, ...] }
//
// 预检不写库,仅为前端 UI 分流服务:返回 create/overwrite/duplicate/reject 四种 action,
// 前端只对 create/overwrite 发起真正 Upload。从 precheck 到 Upload 之间状态可能变,
// Upload 自己的三分支逻辑才是权威。
func (h *DocumentHandler) PrecheckUpload(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	var req dto.PrecheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid body", err.Error())
		return
	}

	cands := make([]service.PrecheckCandidate, len(req.Files))
	for i, f := range req.Files {
		cands[i] = service.PrecheckCandidate{
			FileName:    f.FileName,
			SizeBytes:   f.SizeBytes,
			MIMEType:    f.MIMEType,
			ContentHash: f.ContentHash,
		}
	}

	results, err := h.svc.PrecheckUpload(c.Request.Context(), org.ID, cands)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}

	entries := make([]dto.PrecheckResultEntry, len(results))
	for i, r := range results {
		entries[i] = dto.PrecheckResultEntry{
			FileName:     r.FileName,
			Action:       r.Action,
			ReasonCode:   r.ReasonCode,
			Existing:     r.Existing,
			ExistingList: r.ExistingList,
		}
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok",
		Result: dto.PrecheckResponse{Results: entries}})
}

// GetUploadConfig GET /api/v2/orgs/:slug/documents/config
//
// 返回上传约束快照(max_file_size_bytes + allowed_mime_types),前端据此做本地预过滤。
// 稳定值,前端可以缓存(例如 org 切换时重拉一次)。
func (h *DocumentHandler) GetUploadConfig(c *gin.Context) {
	cfg := h.svc.GetUploadConfig()
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok",
		Result: dto.UploadConfigResponse{
			MaxFileSizeBytes:      cfg.MaxFileSizeBytes,
			AllowedMIMETypes:      cfg.AllowedMIMETypes,
			SemanticSearchEnabled: cfg.SemanticSearchEnabled,
		}})
}

// DeleteDocument 按 docID 硬删文档(含 OSS 对象)。路由:DELETE /api/v2/orgs/:slug/documents/:id。
func (h *DocumentHandler) DeleteDocument(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	docID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), org.ID, docID); err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{Code: http.StatusOK, Message: "ok"})
}

// DownloadDocument GET /api/v2/orgs/:slug/documents/:id/download
// 直接把 OSS 上的原始文件字节流回客户端,带 Content-Disposition 让浏览器保存为原文件名。
func (h *DocumentHandler) DownloadDocument(c *gin.Context) {
	org, ok := GetOrg(c)
	if !ok {
		response.InternalServerError(c, "Missing org context", "")
		return
	}
	docID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	result, err := h.svc.Download(c.Request.Context(), org.ID, docID)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	// OSS body 读完即关;Close 返回的 error 通常是底层连接回收失败,无行动项。
	//sayso-lint:ignore defer-err
	defer result.Body.Close()

	mime := result.MIMEType
	if mime == "" {
		mime = "application/octet-stream"
	}
	// RFC 5987 filename* 处理非 ASCII 文件名,ASCII 客户端兜底 filename。
	asciiName := asciiFallback(result.FileName)
	c.Header("Content-Disposition",
		"attachment; filename=\""+asciiName+"\"; filename*=UTF-8''"+url.PathEscape(result.FileName))
	if result.SizeBytes > 0 {
		c.Header("Content-Length", strconv.FormatInt(result.SizeBytes, 10))
	}
	c.DataFromReader(http.StatusOK, result.SizeBytes, mime, result.Body, nil)
}

// ─── helpers ────────────────────────────────────────────────────────────────

func defaultTitleFromFileName(name string) string {
	slash := strings.LastIndexAny(name, "/\\")
	if slash >= 0 {
		name = name[slash+1:]
	}
	if dot := strings.LastIndex(name, "."); dot > 0 {
		name = name[:dot]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "untitled"
	}
	return name
}

// guessMIME 根据文件扩展名猜 MIME type(multipart header 常为空)。
func guessMIME(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	}
	return "application/octet-stream"
}

// asciiFallback 把非 ASCII 字符替换成 '_',给老客户端的 filename="..." 用。
func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == '"' || r == '\\' || r > 0x7E {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "document"
	}
	return out
}
