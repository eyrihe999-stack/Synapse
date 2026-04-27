package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerKBTools 注册 KB 相关 tool。
//
// Tools:
//   - list_channel_kb_refs   列 channel 挂的 KB 资源关联(source / document)
//   - list_kb_documents      列 channel 经由 source 挂载范围内的可见 KB 文档(LIKE 关键词过滤 + keyset 分页)
//   - get_kb_document        拉单文档元数据 + 文本(text 类走 OSS 原文,二进制回退到 chunks 拼接)
//   - search_kb              语义检索(query → embedding → HNSW),返 top-K chunks
func (s *Server) registerKBTools() {
	if s.deps.KBSvc == nil {
		return
	}

	s.mcp.AddTool(mcp.NewTool("list_channel_kb_refs",
		mcp.WithDescription("List KB (knowledge base) resources attached to a channel. "+
			"Current agent must be a channel member."),
		mcp.WithNumber("channel_id", mcp.Required()),
	), s.handleListChannelKBRefs)

	s.mcp.AddTool(mcp.NewTool("list_kb_documents",
		mcp.WithDescription("List KB documents visible in a channel (via the channel's mounted KB sources). "+
			"Returns metadata only — call get_kb_document for full text. "+
			"Filter by `query` (case-insensitive substring match on title / file_name); "+
			"paginate with `before_id` (last id from previous page) + `limit`."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Channel id; caller must be a member.")),
		mcp.WithString("query", mcp.Description("Optional keyword filter on title / file_name (case-insensitive substring).")),
		mcp.WithNumber("limit", mcp.Description("Page size, default 20, max 100"), mcp.DefaultNumber(20)),
		mcp.WithNumber("before_id", mcp.Description("Keyset cursor: pass last doc.id from previous page (default 0 = first page).")),
	), s.handleListKBDocuments)

	s.mcp.AddTool(mcp.NewTool("get_kb_document",
		mcp.WithDescription("Fetch a KB document's metadata + full text. "+
			"For text-like documents (markdown / plain text) the original OSS object is returned (lossless). "+
			"For binary documents (pdf / docx) the response falls back to chunks concatenated by index. "+
			"`full_text_source` ('oss' or 'chunks_join') tells which path was taken; `truncated=true` indicates the body was clipped. "+
			"Caller must be a channel member AND the document must be visible to the channel "+
			"(its source attached, OR the document directly attached). "+
			"Permission failure returns forbidden without leaking existence."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Channel id used for permission scoping.")),
		mcp.WithNumber("document_id", mcp.Required(), mcp.Description("Target document id.")),
	), s.handleGetKBDocument)

	s.mcp.AddTool(mcp.NewTool("search_kb",
		mcp.WithDescription("Semantic search across the channel's visible KB chunks. "+
			"Returns top-K chunks ranked by cosine similarity to `query`. "+
			"Each hit includes the chunk text, heading_path, the owning doc_id/title, and a 'distance' score (0 = identical, 2 = opposite). "+
			"Use `get_kb_document` to fetch the full document after locating relevant chunks. "+
			"Caller must be a channel member; channels with no KB attached return an empty result."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Channel id used for permission scoping.")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural language query. Will be embedded and used for nearest-neighbor search.")),
		mcp.WithNumber("top_k", mcp.Description("How many chunks to return (default 5, max 20)."), mcp.DefaultNumber(5)),
	), s.handleSearchKB)
}

func (s *Server) handleListKBDocuments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	query := stringArg(req, "query", "")
	limit := intArg(req, "limit", 20)
	beforeID := uint64Arg(req, "before_id", 0)

	docs, err := s.deps.KBSvc.ListKBDocumentsByPrincipal(ctx, channelID, auth.AgentPrincipalID, query, beforeID, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_kb_documents: %s", err.Error())), nil
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, map[string]any{
			"id":                  d.ID,
			"title":               d.Title,
			"file_name":           d.FileName,
			"mime_type":           d.MIMEType,
			"version":             d.Version,
			"chunk_count":         d.ChunkCount,
			"content_byte_size":   d.ContentByteSize,
			"knowledge_source_id": d.KnowledgeSourceID,
			"updated_at":          d.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return jsonResult(out)
}

func (s *Server) handleGetKBDocument(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	docID := uint64Arg(req, "document_id", 0)
	if channelID == 0 || docID == 0 {
		return mcp.NewToolResultError("channel_id and document_id are required"), nil
	}
	res, err := s.deps.KBSvc.GetKBDocumentByPrincipal(ctx, channelID, docID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_kb_document: %s", err.Error())), nil
	}
	d := res.Document
	out := map[string]any{
		"id":                  d.ID,
		"title":               d.Title,
		"file_name":           d.FileName,
		"mime_type":           d.MIMEType,
		"version":             d.Version,
		"chunk_count":         res.ChunkCount,
		"content_byte_size":   d.ContentByteSize,
		"knowledge_source_id": d.KnowledgeSourceID,
		"updated_at":          d.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"full_text":           res.FullText,
		"full_text_source":    res.FullTextSource,
		"truncated":           res.Truncated,
	}
	return jsonResult(out)
}

func (s *Server) handleSearchKB(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	query := stringArg(req, "query", "")
	topK := intArg(req, "top_k", 0)
	if channelID == 0 || query == "" {
		return mcp.NewToolResultError("channel_id and query are required"), nil
	}

	hits, err := s.deps.KBSvc.SearchKBByPrincipal(ctx, channelID, auth.AgentPrincipalID, query, topK)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search_kb: %s", err.Error())), nil
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"doc_id":       h.DocID,
			"doc_title":    h.DocTitle,
			"doc_file_name": h.DocFileName,
			"mime_type":    h.DocMIMEType,
			"chunk_idx":    h.ChunkIdx,
			"content":      h.Content,
			"heading_path": h.HeadingPath,
			"distance":     h.Distance,
		})
	}
	return jsonResult(out)
}

func (s *Server) handleListChannelKBRefs(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}

	rows, err := s.deps.KBSvc.ListChannelKBRefsForPrincipal(ctx, channelID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_channel_kb_refs: %s", err.Error())), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{
			"id":         r.ID,
			"channel_id": r.ChannelID,
			"added_by":   r.AddedBy,
			"added_at":   r.AddedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		if r.KBSourceID != 0 {
			item["kb_source_id"] = r.KBSourceID
		}
		if r.KBDocumentID != 0 {
			item["kb_document_id"] = r.KBDocumentID
		}
		out = append(out, item)
	}
	return jsonResult(out)
}
