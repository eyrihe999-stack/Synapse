package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerWorkstreamTools 注册 4 个 Workstream MCP tool(PR-B)。
//
// Tools:
//   - create_workstream            initiative 下创建 workstream(可选挂 version)
//   - update_workstream            改 workstream 的 name / description / status / version_id
//   - split_workstream_into_tasks  在 workstream 关联 channel 内一次创建多个 task
//   - invite_to_workstream         把一组 principal 加入 workstream channel(角色 member)
//
// 决策 2:split_workstream_into_tasks 是 batch create,不掺 LLM。LLM 拆分能力让
// Architect 在 chat 路径里通过多次 create_task 调用实现。
func (s *Server) registerWorkstreamTools() {
	if s.deps.PMSvc == nil {
		return
	}

	createTool := mcp.NewTool("create_workstream",
		mcp.WithDescription("Create a workstream under an initiative. A workstream is a unit of "+
			"deliverable work (\"how we do it\"); it must belong to one initiative and may "+
			"optionally attach to a version (or stay in backlog if version_id omitted). "+
			"On success an event triggers lazy-create of a kind=workstream channel; the channel "+
			"becomes available within seconds (consumer is async)."),
		mcp.WithNumber("initiative_id", mcp.Required()),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("description", mcp.Description("Optional description")),
		mcp.WithNumber("version_id", mcp.Description("Optional version to attach (workstream is in backlog if omitted)")),
	)
	s.mcp.AddTool(createTool, s.handleCreateWorkstream)

	updateTool := mcp.NewTool("update_workstream",
		mcp.WithDescription("Update workstream fields. status: 'draft' | 'active' | 'blocked' | 'done' | 'cancelled'. "+
			"Pass version_id=0 to move to backlog; non-zero to attach to a different version (must be same project). "+
			"name change is rejected if empty / too long."),
		mcp.WithNumber("workstream_id", mcp.Required()),
		mcp.WithString("name", mcp.Description("New name")),
		mcp.WithString("description", mcp.Description("New description")),
		mcp.WithString("status", mcp.Description("New status enum")),
		mcp.WithNumber("version_id", mcp.Description("New version_id; 0 = backlog")),
	)
	s.mcp.AddTool(updateTool, s.handleUpdateWorkstream)

	splitTool := mcp.NewTool("split_workstream_into_tasks",
		mcp.WithDescription("Batch-create multiple tasks inside a workstream's channel. "+
			"Caller must provide the task list; this tool does NOT do LLM-driven splitting "+
			"(use the chat path with @Synapse Architect for that). "+
			"Fails if workstream channel hasn't been lazy-created yet (transient; retry after a few seconds)."),
		mcp.WithNumber("workstream_id", mcp.Required()),
		mcp.WithArray("tasks", mcp.Required(),
			mcp.Description("Each item: {title, description?, output_spec_kind?, is_lightweight?, "+
				"assignee_principal_id?, reviewer_principal_ids?, required_approvals?}. "+
				"output_spec_kind defaults to 'markdown'."),
			mcp.Items(map[string]any{"type": "object"}),
		),
	)
	s.mcp.AddTool(splitTool, s.handleSplitWorkstreamIntoTasks)

	inviteTool := mcp.NewTool("invite_to_workstream",
		mcp.WithDescription("Add a list of principals as members of a workstream's channel "+
			"(role=member). Idempotent: already-members are silently skipped. "+
			"Caller must be a member of the project's org."),
		mcp.WithNumber("workstream_id", mcp.Required()),
		mcp.WithArray("principal_ids", mcp.Required(),
			mcp.Description("principal_id list to invite"),
			mcp.Items(map[string]any{"type": "number"}),
		),
	)
	s.mcp.AddTool(inviteTool, s.handleInviteToWorkstream)
}

func (s *Server) handleCreateWorkstream(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	initiativeID := uint64Arg(req, "initiative_id", 0)
	name := stringArg(req, "name", "")
	description := stringArg(req, "description", "")
	if initiativeID == 0 || name == "" {
		return mcp.NewToolResultError("initiative_id and name are required"), nil
	}
	var versionID *uint64
	if v := uint64Arg(req, "version_id", 0); v != 0 {
		versionID = &v
	}
	w, err := s.deps.PMSvc.Workstream.Create(ctx, initiativeID, auth.UserID, versionID, name, description)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(w)
}

func (s *Server) handleUpdateWorkstream(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	id := uint64Arg(req, "workstream_id", 0)
	if id == 0 {
		return mcp.NewToolResultError("workstream_id is required"), nil
	}
	updates := map[string]any{}
	if v := stringArg(req, "name", ""); v != "" {
		updates["name"] = v
	}
	if _, ok := req.GetArguments()["description"]; ok {
		updates["description"] = stringArg(req, "description", "")
	}
	if v := stringArg(req, "status", ""); v != "" {
		updates["status"] = v
	}
	if _, has := req.GetArguments()["version_id"]; has {
		v := uint64Arg(req, "version_id", 0)
		if v == 0 {
			updates["version_id"] = nil // 移到 backlog
		} else {
			updates["version_id"] = v
		}
	}
	if len(updates) == 0 {
		return mcp.NewToolResultError("no fields to update"), nil
	}
	if err := s.deps.PMSvc.Workstream.Update(ctx, id, auth.UserID, updates); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("workstream updated"), nil
}

// handleSplitWorkstreamIntoTasks 简化版 batch create:对每个 task 调一次 TaskFacade。
//
// 失败处理:**部分失败**(N 个 task 中 K 个失败)以"已成功创建的列表 + 失败原因"返回,
// 不整体回滚 —— 调用方按报错重试缺失的 task。task 表的 channel_id / workstream_id 关联
// 由 lazy-create 的 channel 反查得到(未来 TaskService.Create 扩 workstream_id 参数后
// 可以改成显式;v0 简化先不改)。
func (s *Server) handleSplitWorkstreamIntoTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.AgentPrincipalID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	if s.deps.TaskSvc == nil {
		return mcp.NewToolResultError("task service unavailable"), nil
	}
	wsID := uint64Arg(req, "workstream_id", 0)
	if wsID == 0 {
		return mcp.NewToolResultError("workstream_id is required"), nil
	}

	// 拿 workstream → channel_id
	w, err := s.deps.PMSvc.Workstream.Get(ctx, wsID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if w.ChannelID == nil || *w.ChannelID == 0 {
		return mcp.NewToolResultError("workstream channel not yet created (consumer is async); retry in a few seconds"), nil
	}
	channelID := *w.ChannelID

	rawTasks, _ := req.GetArguments()["tasks"].([]any)
	if len(rawTasks) == 0 {
		return mcp.NewToolResultError("tasks array is required and non-empty"), nil
	}

	type taskOK struct {
		Index  int    `json:"index"`
		TaskID uint64 `json:"task_id"`
		Title  string `json:"title"`
	}
	type taskFail struct {
		Index int    `json:"index"`
		Title string `json:"title"`
		Error string `json:"error"`
	}
	created := []taskOK{}
	failed := []taskFail{}

	for i, rawT := range rawTasks {
		t, ok := rawT.(map[string]any)
		if !ok {
			failed = append(failed, taskFail{Index: i, Error: "task item must be object"})
			continue
		}
		title, _ := t["title"].(string)
		description, _ := t["description"].(string)
		outputKind, _ := t["output_spec_kind"].(string)
		if outputKind == "" {
			outputKind = "markdown"
		}
		isLightweight, _ := t["is_lightweight"].(bool)
		var assignee uint64
		switch v := t["assignee_principal_id"].(type) {
		case float64:
			assignee = uint64(v)
		case int:
			assignee = uint64(v)
		}
		var reviewers []uint64
		if rawRev, ok := t["reviewer_principal_ids"].([]any); ok {
			for _, rv := range rawRev {
				switch r := rv.(type) {
				case float64:
					reviewers = append(reviewers, uint64(r))
				case int:
					reviewers = append(reviewers, uint64(r))
				}
			}
		}
		requiredApprovals := 0
		if rawApp, ok := t["required_approvals"].(float64); ok {
			requiredApprovals = int(rawApp)
		}

		if title == "" {
			failed = append(failed, taskFail{Index: i, Error: "task.title is required"})
			continue
		}

		out, _, err := s.deps.TaskSvc.CreateTaskByPrincipal(ctx, CreateTaskByPrincipalInput{
			ChannelID:            channelID,
			CreatorPrincipalID:   auth.AgentPrincipalID,
			Title:                title,
			Description:          description,
			OutputSpecKind:       outputKind,
			IsLightweight:        isLightweight,
			AssigneePrincipalID:  assignee,
			ReviewerPrincipalIDs: reviewers,
			RequiredApprovals:    requiredApprovals,
		})
		if err != nil {
			failed = append(failed, taskFail{Index: i, Title: title, Error: err.Error()})
			continue
		}
		created = append(created, taskOK{Index: i, TaskID: out.ID, Title: title})
	}

	return jsonResult(map[string]any{
		"workstream_id": wsID,
		"channel_id":    channelID,
		"created":       created,
		"failed":        failed,
		"summary":       fmt.Sprintf("%d created, %d failed", len(created), len(failed)),
	})
}

func (s *Server) handleInviteToWorkstream(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok || auth.UserID == 0 {
		return mcp.NewToolResultError("authentication required"), nil
	}
	wsID := uint64Arg(req, "workstream_id", 0)
	if wsID == 0 {
		return mcp.NewToolResultError("workstream_id is required"), nil
	}
	pids := uint64ArrayArg(req, "principal_ids")
	if len(pids) == 0 {
		return mcp.NewToolResultError("principal_ids must be a non-empty array"), nil
	}
	added, channelID, err := s.deps.PMSvc.Workstream.InviteToChannel(ctx, wsID, auth.UserID, pids)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"workstream_id":    wsID,
		"channel_id":       channelID,
		"invited":          added,
	})
}
