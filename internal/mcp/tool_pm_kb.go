package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerProjectKBTools 注册 3 个 project 级 KB tool(PR-B):
//
//   - attach_kb_to_project    给 project 挂载 KB(source 或 doc 二选一)
//   - detach_kb_from_project  按 project_kb_ref_id 卸载
//   - list_project_kb_refs    列 project 当前所有 KB 挂载
//
// 老的 channel-级 KB tool(list_channel_kb_refs / list_kb_documents / get_kb_document)
// 暂时保留不动 —— 内部仍走 channel_kb_refs 表;DROP 老表 + 改造老 tool 内部实现
// 走 project_kb_refs 留到 PR-C 完成(避免 PR-B 范围爆炸)。
func (s *Server) registerProjectKBTools() {
	if s.deps.PMSvc == nil {
		return
	}

	attachTool := mcp.NewTool("attach_kb_to_project",
		mcp.WithDescription("Attach a KB resource (knowledge_source or document) to a project. "+
			"Pass exactly ONE of `kb_source_id` or `kb_document_id` (not both). "+
			"All members of the project's org can read attached KB across all channels of the project. "+
			"Returns ErrProjectKBRefDuplicated (409290061) if the same target is already attached."),
		mcp.WithNumber("project_id", mcp.Required()),
		mcp.WithNumber("kb_source_id", mcp.Description("Knowledge source id (whole-source attach)")),
		mcp.WithNumber("kb_document_id", mcp.Description("Single document id (fine-grained attach)")),
	)
	s.mcp.AddTool(attachTool, s.handleAttachKBToProject)

	detachTool := mcp.NewTool("detach_kb_from_project",
		mcp.WithDescription("Detach a KB attachment by its project_kb_ref id. Idempotent."),
		mcp.WithNumber("project_kb_ref_id", mcp.Required()),
	)
	s.mcp.AddTool(detachTool, s.handleDetachKBFromProject)

	listTool := mcp.NewTool("list_project_kb_refs",
		mcp.WithDescription("List all KB attachments on a project (both source-level and document-level). "+
			"This is the project-scope replacement of list_channel_kb_refs (the old per-channel "+
			"semantics is being phased out)."),
		mcp.WithNumber("project_id", mcp.Required()),
	)
	s.mcp.AddTool(listTool, s.handleListProjectKBRefs)
}

func (s *Server) handleAttachKBToProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	sourceID := uint64Arg(req, "kb_source_id", 0)
	docID := uint64Arg(req, "kb_document_id", 0)
	if projectID == 0 {
		return mcp.NewToolResultError("project_id is required"), nil
	}
	if (sourceID == 0 && docID == 0) || (sourceID != 0 && docID != 0) {
		return mcp.NewToolResultError("provide exactly one of kb_source_id or kb_document_id"), nil
	}
	ref, err := s.deps.PMSvc.ProjectKBRef.Attach(ctx, projectID, auth.UserID, sourceID, docID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(ref)
}

func (s *Server) handleDetachKBFromProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	refID := uint64Arg(req, "project_kb_ref_id", 0)
	if refID == 0 {
		return mcp.NewToolResultError("project_kb_ref_id is required"), nil
	}
	if err := s.deps.PMSvc.ProjectKBRef.Detach(ctx, refID, auth.UserID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("kb ref detached"), nil
}

func (s *Server) handleListProjectKBRefs(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, ok := oauthmw.AuthFromContext(ctx); !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	if projectID == 0 {
		return mcp.NewToolResultError("project_id is required"), nil
	}
	list, err := s.deps.PMSvc.ProjectKBRef.List(ctx, projectID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(list)
}
