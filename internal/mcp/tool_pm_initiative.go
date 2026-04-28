package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerInitiativeTools 注册 4 个 Initiative MCP tool(PR-B)。
//
// Tools:
//   - create_initiative          在 project 下创建 initiative(主题轴,"为什么做")
//   - update_initiative          改 initiative 的 status / description / target_outcome
//   - archive_initiative         归档 initiative(前置守卫:无未归档 workstream)
//   - list_initiatives_by_project 列 project 下的所有 initiative
//
// 权限:用户身份(BearerAuth.UserID)必须是 project 所属 org 成员;system initiative
// (Default)拒改 / 拒 archive(由 service 层守卫)。
func (s *Server) registerInitiativeTools() {
	if s.deps.PMSvc == nil {
		return
	}

	createTool := mcp.NewTool("create_initiative",
		mcp.WithDescription("Create an initiative under a project. An initiative is a long-running theme "+
			"(\"why we're doing this\") that groups related workstreams across versions. "+
			"Pass `target_outcome` to record the success criteria for downstream LLM/human reference."),
		mcp.WithNumber("project_id", mcp.Required(), mcp.Description("Parent project id")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Initiative name (unique within active project)")),
		mcp.WithString("description", mcp.Description("Optional short description")),
		mcp.WithString("target_outcome", mcp.Description("Optional success criteria text")),
	)
	s.mcp.AddTool(createTool, s.handleCreateInitiative)

	updateTool := mcp.NewTool("update_initiative",
		mcp.WithDescription("Update an initiative's name / description / target_outcome / status. "+
			"system initiatives (auto-created Default) cannot be modified. "+
			"status: 'planned' | 'active' | 'completed' | 'cancelled'."),
		mcp.WithNumber("initiative_id", mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
		mcp.WithString("description", mcp.Description("New description")),
		mcp.WithString("target_outcome", mcp.Description("New target_outcome")),
		mcp.WithString("status", mcp.Description("New status enum")),
	)
	s.mcp.AddTool(updateTool, s.handleUpdateInitiative)

	archiveTool := mcp.NewTool("archive_initiative",
		mcp.WithDescription("Archive an initiative. Fails if it still has active workstreams "+
			"(status not in done/cancelled, archived_at IS NULL). system initiatives cannot be archived."),
		mcp.WithNumber("initiative_id", mcp.Required()),
	)
	s.mcp.AddTool(archiveTool, s.handleArchiveInitiative)

	listTool := mcp.NewTool("list_initiatives_by_project",
		mcp.WithDescription("List all initiatives (active + archived) under a project."),
		mcp.WithNumber("project_id", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Page size, default 50, max 200"), mcp.DefaultNumber(50)),
		mcp.WithNumber("offset", mcp.Description("Offset"), mcp.DefaultNumber(0)),
	)
	s.mcp.AddTool(listTool, s.handleListInitiativesByProject)
}

func (s *Server) handleCreateInitiative(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	name := stringArg(req, "name", "")
	description := stringArg(req, "description", "")
	targetOutcome := stringArg(req, "target_outcome", "")
	if projectID == 0 || name == "" {
		return mcp.NewToolResultError("project_id and name are required"), nil
	}
	init, err := s.deps.PMSvc.Initiative.Create(ctx, projectID, auth.UserID, name, description, targetOutcome)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(init)
}

func (s *Server) handleUpdateInitiative(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	id := uint64Arg(req, "initiative_id", 0)
	if id == 0 {
		return mcp.NewToolResultError("initiative_id is required"), nil
	}
	updates := map[string]any{}
	if v := stringArg(req, "name", ""); v != "" {
		updates["name"] = v
	}
	if _, ok := req.GetArguments()["description"]; ok {
		updates["description"] = stringArg(req, "description", "")
	}
	if _, ok := req.GetArguments()["target_outcome"]; ok {
		updates["target_outcome"] = stringArg(req, "target_outcome", "")
	}
	if v := stringArg(req, "status", ""); v != "" {
		updates["status"] = v
	}
	if len(updates) == 0 {
		return mcp.NewToolResultError("no fields to update"), nil
	}
	if err := s.deps.PMSvc.Initiative.Update(ctx, id, auth.UserID, updates); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("initiative updated"), nil
}

func (s *Server) handleArchiveInitiative(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	id := uint64Arg(req, "initiative_id", 0)
	if id == 0 {
		return mcp.NewToolResultError("initiative_id is required"), nil
	}
	if err := s.deps.PMSvc.Initiative.Archive(ctx, id, auth.UserID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("initiative archived"), nil
}

func (s *Server) handleListInitiativesByProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, ok := oauthmw.AuthFromContext(ctx); !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	if projectID == 0 {
		return mcp.NewToolResultError("project_id is required"), nil
	}
	limit := intArg(req, "limit", 50)
	offset := intArg(req, "offset", 0)
	list, err := s.deps.PMSvc.Initiative.List(ctx, projectID, limit, offset)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(list)
}
