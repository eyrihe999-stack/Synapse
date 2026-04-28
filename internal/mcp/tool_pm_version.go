package mcp

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerVersionTools 注册 3 个 Version MCP tool(PR-B)。
//
// Tools:
//   - create_version           project 下新建发版窗口(时间轴)
//   - update_version           改 version 的 status / target_date / released_at
//   - list_versions_by_project 列 project 所有 version
//
// system version(Backlog,project 自动建)拒改 / 拒删 — service 层守卫。
func (s *Server) registerVersionTools() {
	if s.deps.PMSvc == nil {
		return
	}

	createTool := mcp.NewTool("create_version",
		mcp.WithDescription("Create a version under a project. A version is a delivery window "+
			"(\"when shipped\") that workstreams from any initiative can attach to. "+
			"name is unique within the project. status: 'planning' | 'active' | 'released' | 'cancelled'."),
		mcp.WithNumber("project_id", mcp.Required()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Version name e.g. 'v1.0'")),
		mcp.WithString("status", mcp.Required(), mcp.Description("Initial status enum")),
	)
	s.mcp.AddTool(createTool, s.handleCreateVersion)

	updateTool := mcp.NewTool("update_version",
		mcp.WithDescription("Update a version's status / target_date / released_at. "+
			"system Backlog version cannot be modified. "+
			"target_date / released_at are RFC3339 timestamps; pass empty string to skip a field."),
		mcp.WithNumber("version_id", mcp.Required()),
		mcp.WithString("status", mcp.Description("New status enum")),
		mcp.WithString("target_date", mcp.Description("Planned release date, RFC3339")),
		mcp.WithString("released_at", mcp.Description("Actual release time, RFC3339")),
	)
	s.mcp.AddTool(updateTool, s.handleUpdateVersion)

	listTool := mcp.NewTool("list_versions_by_project",
		mcp.WithDescription("List all versions of a project (active + cancelled)."),
		mcp.WithNumber("project_id", mcp.Required()),
	)
	s.mcp.AddTool(listTool, s.handleListVersionsByProject)
}

func (s *Server) handleCreateVersion(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	name := stringArg(req, "name", "")
	status := stringArg(req, "status", "")
	if projectID == 0 || name == "" || status == "" {
		return mcp.NewToolResultError("project_id, name and status are required"), nil
	}
	v, err := s.deps.PMSvc.Version.Create(ctx, projectID, auth.UserID, name, status)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(v)
}

func (s *Server) handleUpdateVersion(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	id := uint64Arg(req, "version_id", 0)
	if id == 0 {
		return mcp.NewToolResultError("version_id is required"), nil
	}
	updates := map[string]any{}
	if v := stringArg(req, "status", ""); v != "" {
		updates["status"] = v
	}
	if v := stringArg(req, "target_date", ""); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return mcp.NewToolResultError("target_date must be RFC3339"), nil
		}
		updates["target_date"] = t
	}
	if v := stringArg(req, "released_at", ""); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return mcp.NewToolResultError("released_at must be RFC3339"), nil
		}
		updates["released_at"] = t
	}
	if len(updates) == 0 {
		return mcp.NewToolResultError("no fields to update"), nil
	}
	if err := s.deps.PMSvc.Version.Update(ctx, id, auth.UserID, updates); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("version updated"), nil
}

func (s *Server) handleListVersionsByProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, ok := oauthmw.AuthFromContext(ctx); !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	projectID := uint64Arg(req, "project_id", 0)
	if projectID == 0 {
		return mcp.NewToolResultError("project_id is required"), nil
	}
	list, err := s.deps.PMSvc.Version.List(ctx, projectID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(list)
}
