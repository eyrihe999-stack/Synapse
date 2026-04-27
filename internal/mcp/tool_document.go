package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerDocumentTools 注册 channel 共享文档(PR #9')的 MCP tool。
//
// 6 个 tool —— 完整的 agent 协作编辑闭环:
//   - list_channel_documents          列 channel 下未删共享文档(含锁状态)
//   - get_channel_document            元数据 + 当前锁;include_content=true 时附带当前内容
//   - create_channel_document         agent 起新 doc(空白)
//   - acquire_channel_document_lock   抢锁(必须先抢锁才能 save)
//   - save_channel_document           保存新版(同 hash 幂等);依赖持锁
//   - release_channel_document_lock   主动释放锁
//
// 不暴露:heartbeat / force_release / version_list / get_version_content / soft_delete。
// 心跳由 MCP 调用本身的短回合代偿(锁 10min TTL 已够单次保存);治理动作走 Web。
func (s *Server) registerDocumentTools() {
	if s.deps.DocumentSvc == nil {
		return
	}

	s.mcp.AddTool(mcp.NewTool("list_channel_documents",
		mcp.WithDescription("List shared documents in a channel (with current lock state). "+
			"Caller must be a channel member; archived channels return docs read-only."),
		mcp.WithNumber("channel_id", mcp.Required()),
	), s.handleListChannelDocuments)

	s.mcp.AddTool(mcp.NewTool("get_channel_document",
		mcp.WithDescription("Get a shared document's metadata + current lock state. "+
			"Pass include_content=true to also fetch the current version's full content "+
			"(may be large; default false). Caller must be a channel member."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
		mcp.WithBoolean("include_content", mcp.Description("If true, also fetch current version content. Default false.")),
	), s.handleGetChannelDocument)

	s.mcp.AddTool(mcp.NewTool("create_channel_document",
		mcp.WithDescription("Create a new empty shared document in a channel. "+
			"Returns the new document id; the document starts with no content — call "+
			"acquire_channel_document_lock + save_channel_document to write the first version."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithString("title", mcp.Required(), mcp.Description("Document title, ≤128 chars")),
		mcp.WithString("content_kind", mcp.Required(), mcp.Description("Content kind: 'md' (markdown) or 'text' (plain text)")),
	), s.handleCreateChannelDocument)

	s.mcp.AddTool(mcp.NewTool("acquire_channel_document_lock",
		mcp.WithDescription("Acquire (or renew) the exclusive edit lock on a shared document. "+
			"Required before save_channel_document. Lock TTL is 10 minutes; saving counts as "+
			"recent activity but does NOT renew the lock — for long edit sessions, call again "+
			"every few minutes. Returns 409 / lock-held if another principal holds it."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
	), s.handleAcquireChannelDocumentLock)

	s.mcp.AddTool(mcp.NewTool("save_channel_document",
		mcp.WithDescription("Save a new version of a shared document INLINE (content as a tool argument). "+
			"Caller MUST hold an active lock. `content` is the full new document body (NOT a diff); ≤ 1MB. "+
			"Same content as the latest version is idempotent (returns 'no-op' without writing). "+
			"`edit_summary` is optional, ≤255 chars."+
			"\n\n"+
			"PREFER request_document_upload_url + commit_document_upload FOR ANYTHING > ~5 KB — "+
			"this tool forces the entire content through the LLM tool input (slow + token-heavy + no optimistic concurrency check). "+
			"Use this tool only for: (a) small content the LLM is naturally producing (a paragraph, a list), "+
			"(b) cases where you genuinely want a 'blind overwrite' (no read-then-write needed)."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
		mcp.WithString("content", mcp.Required(), mcp.Description("Full document body (UTF-8 string).")),
		mcp.WithString("edit_summary", mcp.Description("Optional change note, ≤255 chars.")),
	), s.handleSaveChannelDocument)

	s.mcp.AddTool(mcp.NewTool("release_channel_document_lock",
		mcp.WithDescription("Release the lock you currently hold on a shared document. "+
			"No-op if you do not hold the lock. Doesn't modify content."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
	), s.handleReleaseChannelDocumentLock)

	// ── OSS 直传(PR #15')─── 大文件场景的 save_channel_document 替代:
	// LLM 调 request_document_upload_url 拿预签名 URL,然后**用 Bash / 文件感知的 fetch 工具**
	// 把字节 PUT 到 OSS(curl --upload-file 或 fetch),最后调 commit_document_upload 通知服务端落 version 行。
	// 字节不经 LLM context、不经 MCP transport,适合 ≥ 几十 KB 的文件。
	s.mcp.AddTool(mcp.NewTool("request_document_upload_url",
		mcp.WithDescription("Get a presigned PUT URL to upload bytes directly to OSS, plus a commit_token. "+
			"USE THIS INSTEAD OF save_channel_document FOR ANY FILE > ~5 KB — save_channel_document forces "+
			"the file content through the LLM tool input which is slow and burns tokens for large files. "+
			"\n\n"+
			"After you get the URL: PUT the file with `curl --upload-file <local_path> '<url>' -H 'Content-Type: <content_type>'` "+
			"(or any HTTP client). The Content-Type header MUST match the returned `content_type` exactly "+
			"or OSS will reject with SignatureDoesNotMatch. After PUT succeeds (HTTP 200), call commit_document_upload "+
			"with the commit_token. The presign + token are valid for 5 minutes. "+
			"Caller must be a channel member; document must exist. Lock is NOT required at this step but IS required at commit. "+
			"\n\n"+
			"REQUIRED FOR READ-MODIFY-WRITE: pass `base_version` set to the `version` field returned by "+
			"`request_document_download_url` (or `get_channel_document`). The server will verify at commit time that "+
			"the document hasn't been modified by someone else since you downloaded — if it has, commit returns "+
			"`document_base_version_stale` and you should re-download, re-apply your edit, and retry. "+
			"OMIT base_version ONLY when you're doing a 'blind write' (overwriting the doc without reading it first)."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
		mcp.WithString("base_version", mcp.Description("sha256 of the version you read; required for safe RMW. Pass the `version` from request_document_download_url. Omit only for blind overwrites.")),
	), s.handleRequestDocumentUploadURL)

	s.mcp.AddTool(mcp.NewTool("commit_document_upload",
		mcp.WithDescription("Commit a previously-uploaded OSS object as the new document version. "+
			"REQUIRES: (1) you previously called request_document_upload_url and PUT the file, "+
			"(2) you currently hold the document edit lock (call acquire_channel_document_lock first if needed). "+
			"Server fetches the OSS object, verifies size ≤ 1MB, computes sha256, writes a new version row, "+
			"and emits the channel_document.updated event. If the same sha256 already exists for this document, "+
			"the call is idempotent (Created=false, no new version written, the OSS object you uploaded is deleted)."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
		mcp.WithString("commit_token", mcp.Required(), mcp.Description("Token from request_document_upload_url. Single-use, expires in 5min.")),
		mcp.WithString("edit_summary", mcp.Description("Optional change note, ≤255 chars.")),
	), s.handleCommitDocumentUpload)

	s.mcp.AddTool(mcp.NewTool("request_document_download_url",
		mcp.WithDescription("Get a presigned GET URL to download the document's CURRENT version directly from OSS. "+
			"USE THIS INSTEAD OF get_channel_document(include_content=true) FOR ANY FILE > ~5 KB — "+
			"include_content forces the file content through the LLM context, which burns tokens. "+
			"After you get the URL: download with `curl '<url>' -o /tmp/doc.md` (or any HTTP client). "+
			"The URL is valid for 5 minutes. "+
			"\n\n"+
			"FULL READ-MODIFY-WRITE WORKFLOW (file bytes NEVER enter LLM context):\n"+
			"  1. acquire_channel_document_lock                   (claim edit ownership)\n"+
			"  2. request_document_download_url                   (returns `version` — REMEMBER IT)\n"+
			"  3. curl '<download_url>' -o /tmp/doc.md            (download to local)\n"+
			"  4. Edit tool to modify /tmp/doc.md locally         (LLM only outputs the diff)\n"+
			"  5. request_document_upload_url with base_version=<the version from step 2>\n"+
			"  6. curl --upload-file /tmp/doc.md '<upload_url>' -H 'Content-Type: <ct>'\n"+
			"  7. commit_document_upload                          (server checks base_version matches; rejects if stale)\n"+
			"  8. release_channel_document_lock\n"+
			"\n"+
			"Caller must be a channel member; the document must have at least one version saved (otherwise returns content empty). "+
			"The `version` field in the response is the sha256 you should pass as `base_version` to upload_url to enable optimistic concurrency."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithNumber("document_id", mcp.Required()),
	), s.handleRequestDocumentDownloadURL)
}

func (s *Server) handleRequestDocumentDownloadURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	out, err := s.deps.DocumentSvc.PresignDownloadByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request_document_download_url: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"download_url": out.DownloadURL,
		"version":      out.Version,
		"byte_size":    out.ByteSize,
		"content_type": out.ContentType,
		"expires_at":   out.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"hint":         "GET this URL with curl/fetch to a local file. Then use Edit tool to modify and request_document_upload_url to push back.",
	})
}

func (s *Server) handleRequestDocumentUploadURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	baseVersion := stringArg(req, "base_version", "")
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	out, err := s.deps.DocumentSvc.PresignUploadByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID, baseVersion)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request_document_upload_url: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"upload_url":    out.UploadURL,
		"commit_token":  out.CommitToken,
		"content_type":  out.ContentType,
		"expires_at":    out.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"max_byte_size": out.MaxByteSize,
		"hint":          "PUT bytes to upload_url with the exact content_type header, then call commit_document_upload with the commit_token. acquire_channel_document_lock before commit.",
	})
}

func (s *Server) handleCommitDocumentUpload(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	token := stringArg(req, "commit_token", "")
	editSummary := stringArg(req, "edit_summary", "")
	if channelID == 0 || docID == 0 || token == "" {
		return mcp.NewToolResultError("channel_id, document_id and commit_token are required"), nil
	}
	out, err := s.deps.DocumentSvc.CommitUploadByPrincipal(ctx, DocumentCommitUploadByPrincipalInput{
		ChannelID:        channelID,
		DocumentID:       docID,
		ActorPrincipalID: auth.AgentPrincipalID,
		CommitToken:      token,
		EditSummary:      editSummary,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("commit_document_upload: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"document_id":       out.Document.ID,
		"version":           out.Version.Version,
		"byte_size":         out.Version.ByteSize,
		"created":           out.Created,
		"current_byte_size": out.Document.CurrentByteSize,
		"updated_at":        out.Document.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (s *Server) handleListChannelDocuments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	rows, err := s.deps.DocumentSvc.ListByPrincipal(ctx, channelID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_channel_documents: %s", err.Error())), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{
			"id":                       r.Document.ID,
			"title":                    r.Document.Title,
			"content_kind":             r.Document.ContentKind,
			"current_version":          r.Document.CurrentVersion,
			"current_byte_size":        r.Document.CurrentByteSize,
			"created_by_principal_id":  r.Document.CreatedByPrincipalID,
			"updated_by_principal_id":  r.Document.UpdatedByPrincipalID,
			"updated_at":               r.Document.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		if r.Lock != nil {
			item["lock"] = map[string]any{
				"locked_by_principal_id": r.Lock.LockedByPrincipalID,
				"expires_at":             r.Lock.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			}
		}
		out = append(out, item)
	}
	return jsonResult(out)
}

func (s *Server) handleGetChannelDocument(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	includeContent := boolArg(req, "include_content", false)

	detail, err := s.deps.DocumentSvc.GetByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_channel_document: %s", err.Error())), nil
	}
	out := map[string]any{
		"id":                       detail.Document.ID,
		"title":                    detail.Document.Title,
		"content_kind":             detail.Document.ContentKind,
		"current_version":          detail.Document.CurrentVersion,
		"current_byte_size":        detail.Document.CurrentByteSize,
		"created_by_principal_id":  detail.Document.CreatedByPrincipalID,
		"updated_by_principal_id":  detail.Document.UpdatedByPrincipalID,
		"updated_at":               detail.Document.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if detail.Lock != nil {
		out["lock"] = map[string]any{
			"locked_by_principal_id": detail.Lock.LockedByPrincipalID,
			"expires_at":             detail.Lock.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	if includeContent {
		content, err := s.deps.DocumentSvc.GetContentByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("get_channel_document content: %s", err.Error())), nil
		}
		out["content"] = string(content.Content)
	}
	return jsonResult(out)
}

func (s *Server) handleCreateChannelDocument(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	title := stringArg(req, "title", "")
	kind := stringArg(req, "content_kind", "")
	if channelID == 0 || title == "" || kind == "" {
		return mcp.NewToolResultError("channel_id, title, content_kind are required"), nil
	}
	doc, err := s.deps.DocumentSvc.CreateByPrincipal(ctx, channelID, auth.AgentPrincipalID, title, kind)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("create_channel_document: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"id":           doc.ID,
		"title":        doc.Title,
		"content_kind": doc.ContentKind,
		"created_at":   doc.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (s *Server) handleAcquireChannelDocumentLock(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	state, err := s.deps.DocumentSvc.AcquireLockByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID)
	// LockHeld 错误也要把当前持锁人返回去给 LLM 自己决定下一步(don't surface as raw error)
	if errors.Is(err, chanerr.ErrChannelDocumentLockHeld) {
		return jsonResult(map[string]any{
			"acquired":               false,
			"held_by_principal_id":   state.HeldByPrincipalID,
			"expires_at":             state.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"reason":                 "lock held by another principal",
		})
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("acquire_channel_document_lock: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"acquired":               true,
		"held_by_principal_id":   state.HeldByPrincipalID,
		"locked_at":              state.LockedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"expires_at":             state.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (s *Server) handleSaveChannelDocument(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	content := stringArg(req, "content", "")
	editSummary := stringArg(req, "edit_summary", "")
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	if content == "" {
		return mcp.NewToolResultError("content is required"), nil
	}
	res, err := s.deps.DocumentSvc.SaveVersionByPrincipal(ctx, DocumentSaveByPrincipalInput{
		ChannelID:        channelID,
		DocumentID:       docID,
		ActorPrincipalID: auth.AgentPrincipalID,
		Content:          []byte(content),
		EditSummary:      editSummary,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("save_channel_document: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"document_id":       res.Document.ID,
		"version":           res.Version.Version,
		"byte_size":         res.Version.ByteSize,
		"created":           res.Created,
		"current_byte_size": res.Document.CurrentByteSize,
		"updated_at":        res.Document.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (s *Server) handleReleaseChannelDocumentLock(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	if err := s.deps.DocumentSvc.ReleaseLockByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("release_channel_document_lock: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{"ok": true})
}
