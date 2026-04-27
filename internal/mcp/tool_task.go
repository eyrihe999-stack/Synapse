package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	taskmodel "github.com/eyrihe999-stack/Synapse/internal/task/model"
)

// registerTaskTools MCP tool:任务 CRUD + claim/submit/review。
//
// 所有调用者身份都从 ctx 里取 AgentPrincipalID(Bearer 中间件注入)。
func (s *Server) registerTaskTools() {
	if s.deps.TaskSvc == nil {
		return
	}

	s.mcp.AddTool(mcp.NewTool("list_my_tasks",
		mcp.WithDescription(
			"List tasks the current agent should care about: assigned to me, awaiting my review, or both.\n\n"+
				"**IMPORTANT — always call this fresh, do not rely on cached results:**\n"+
				"- The Synapse platform does NOT push notifications for new tasks. New tasks "+
				"can be assigned to you at any moment by a human or another agent without any warning.\n"+
				"- Whenever the user asks about tasks, todos, pending work, what's new, what's waiting, "+
				"or anything that might be affected by task state — re-invoke this tool. Do NOT reuse "+
				"a prior response from earlier in the conversation.\n"+
				"- A task you saw as `approved` last time is closed forever (terminal state), but new "+
				"tasks can appear with any status at any time.\n"+
				"- Cost of this call is tiny; prefer re-calling over staleness.",
		),
		mcp.WithString("role", mcp.Description(
			"Which tasks to return. Valid values:\n"+
				"  - 'either' (default if omitted): tasks assigned to me + tasks where I'm in the reviewer list\n"+
				"  - 'assignee': only tasks assigned to me (I need to do the work)\n"+
				"  - 'reviewer': only tasks where I'm in the reviewer list (I need to approve / request changes)",
		)),
		mcp.WithString("status", mcp.Description(
			"Optional status filter. Valid values: "+
				"open / assigned / in_progress / submitted / approved / rejected / changes_requested / canceled. "+
				"Omit to return all statuses. Open-ended means 'still needs my action': "+
				"assigned + in_progress + changes_requested for assignee role; submitted for reviewer role.",
		)),
		mcp.WithNumber("limit", mcp.DefaultNumber(50)),
		mcp.WithNumber("offset", mcp.DefaultNumber(0)),
	), s.handleListMyTasks)

	s.mcp.AddTool(mcp.NewTool("get_task",
		mcp.WithDescription(
			"Fetch the authoritative current state of a single task + its reviewers + submissions + reviews. "+
				"Always returns fresh data — call this again whenever you need to act on or report about a task, "+
				"even if you've seen it earlier; status / submissions / reviews may have changed.",
		),
		mcp.WithNumber("task_id", mcp.Required()),
	), s.handleGetTask)

	s.mcp.AddTool(mcp.NewTool("create_task",
		mcp.WithDescription(
			"Create a new task in a channel. You (the current agent) become the creator.\n"+
				"- `assignee_principal_id` may be 0 / omitted to leave it open for claim.\n"+
				"- `reviewer_principal_ids` may be empty → task needs no approval (submit closes it as `approved`).\n"+
				"- `required_approvals` must be ≤ len(reviewer_principal_ids); when reviewers is empty it must be 0.\n"+
				"- For task assignment scenarios, use list_channel_members first to resolve display names to principal_ids.\n"+
				"- Set `is_lightweight=true` for tasks that don't need a file deliverable "+
				"(e.g. 'review this PR', 'have a quick chat', 'confirm X is done'). Lightweight tasks "+
				"submit via inline_summary text only — no markdown / text file is uploaded.",
		),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("description", mcp.Description("Markdown description")),
		mcp.WithString("output_spec_kind", mcp.Required(), mcp.Description("markdown | text (still required for lightweight tasks; ignored at submit time)")),
		mcp.WithBoolean("is_lightweight", mcp.Description("true = no file deliverable; submit goes through inline_summary only. Default false.")),
		mcp.WithNumber("assignee_principal_id", mcp.Description("0 or omit = leave unassigned (open for claim)")),
		mcp.WithArray("reviewer_principal_ids", mcp.Description("Principal IDs allowed to review; empty = no approval needed"),
			mcp.Items(map[string]any{"type": "number"})),
		mcp.WithNumber("required_approvals", mcp.Description("Number of approvals required to close the task. 0 = no approval (submit = approved). Must be ≤ len(reviewer_principal_ids)."), mcp.DefaultNumber(0)),
	), s.handleCreateTask)

	s.mcp.AddTool(mcp.NewTool("claim_task",
		mcp.WithDescription(
			"Claim a task — sets assignee=you and status=in_progress. "+
				"Works for: (a) open / unassigned tasks, (b) tasks already assigned to you, "+
				"(c) tasks assigned to your owner user (you're a personal agent representing that user). "+
				"After claiming you're expected to do the work and then call submit_result.",
		),
		mcp.WithNumber("task_id", mcp.Required()),
	), s.handleClaimTask)

	s.mcp.AddTool(mcp.NewTool("submit_result",
		mcp.WithDescription(
			"Submit your deliverable for a task you're assigned to (or assigned to your owner user). "+
				"Moves status → submitted (or directly → approved if the task has no reviewers). "+
				"Call get_task afterward if you want to see the new state with submission id.\n\n"+
				"For NORMAL tasks (task.is_lightweight=false): pass `content_kind` matching task.output_spec_kind + `content` body. inline_summary optional.\n"+
				"For LIGHTWEIGHT tasks (task.is_lightweight=true): leave `content_kind` and `content` empty; pass `inline_summary` describing what was done (required, ≤ 512 chars). The submission has no file.",
		),
		mcp.WithNumber("task_id", mcp.Required()),
		mcp.WithString("content_kind", mcp.Description("Normal task: must match task.output_spec_kind (markdown | text). Lightweight task: omit.")),
		mcp.WithString("content", mcp.Description("Normal task: UTF-8 body ≤ 1MB. Lightweight task: omit.")),
		mcp.WithString("inline_summary", mcp.Description("Normal task: optional short summary ≤ 512 chars. Lightweight task: required ≤ 512 chars (this IS the deliverable).")),
	), s.handleSubmitTask)

	s.mcp.AddTool(mcp.NewTool("review_task",
		mcp.WithDescription(
			"Approve / request changes / reject a submission. **You must be in the task's reviewer list as YOURSELF (the agent principal).** "+
				"Unlike list_my_tasks / claim_task / submit_result, this tool does NOT fall back to your owner user — "+
				"approval is a high-trust action and the platform deliberately requires the reviewer to act under their own identity. "+
				"If your owner is in the reviewer list but you (the agent) are not, you'll get a Forbidden error; "+
				"tell the user to approve via the web UI instead.\n\n"+
				"Enough `approved` decisions (≥ required_approvals) closes the task as approved. "+
				"`request_changes` sends it back to the assignee to submit a new version.",
		),
		mcp.WithNumber("task_id", mcp.Required()),
		mcp.WithNumber("submission_id", mcp.Required()),
		mcp.WithString("decision", mcp.Required(), mcp.Description("approved | request_changes | rejected")),
		mcp.WithString("comment", mcp.Description("Optional review comment; recommended when decision is request_changes or rejected")),
	), s.handleReviewTask)
}

// ── handlers ────────────────────────────────────────────────────────────

func (s *Server) handleListMyTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	role := stringArg(req, "role", "")
	status := stringArg(req, "status", "")
	limit := intArg(req, "limit", 50)
	offset := intArg(req, "offset", 0)
	rows, err := s.deps.TaskSvc.ListMyTasksForPrincipal(ctx, auth.AgentPrincipalID, role, status, limit, offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_my_tasks: %s", err.Error())), nil
	}
	out := make([]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, taskSummary(&t))
	}
	return jsonResult(out)
}

func (s *Server) handleGetTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	taskID := uint64Arg(req, "task_id", 0)
	if taskID == 0 {
		return mcp.NewToolResultError("task_id is required"), nil
	}
	d, err := s.deps.TaskSvc.GetTaskForPrincipal(ctx, taskID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_task: %s", err.Error())), nil
	}
	subs := make([]any, 0, len(d.Submissions))
	for _, sb := range d.Submissions {
		subs = append(subs, map[string]any{
			"id":                     sb.ID,
			"submitter_principal_id": sb.SubmitterPrincipalID,
			"content_kind":           sb.ContentKind,
			"oss_key":                sb.OSSKey,
			"byte_size":              sb.ByteSize,
			"inline_summary":         sb.InlineSummary,
			"created_at":             sb.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	reviews := make([]any, 0, len(d.Reviews))
	for _, rv := range d.Reviews {
		reviews = append(reviews, map[string]any{
			"id":                    rv.ID,
			"submission_id":         rv.SubmissionID,
			"reviewer_principal_id": rv.ReviewerPrincipalID,
			"decision":              rv.Decision,
			"comment":               rv.Comment,
			"created_at":            rv.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return jsonResult(map[string]any{
		"task":        taskSummary(&d.Task),
		"reviewers":   d.Reviewers,
		"submissions": subs,
		"reviews":     reviews,
	})
}

func (s *Server) handleCreateTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	t, reviewerPIDs, err := s.deps.TaskSvc.CreateTaskByPrincipal(ctx, CreateTaskByPrincipalInput{
		ChannelID:            channelID,
		CreatorPrincipalID:   auth.AgentPrincipalID,
		Title:                stringArg(req, "title", ""),
		Description:          stringArg(req, "description", ""),
		OutputSpecKind:       stringArg(req, "output_spec_kind", ""),
		IsLightweight:        boolArg(req, "is_lightweight", false),
		AssigneePrincipalID:  uint64Arg(req, "assignee_principal_id", 0),
		ReviewerPrincipalIDs: uint64ArrayArg(req, "reviewer_principal_ids"),
		RequiredApprovals:    intArg(req, "required_approvals", 1),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("create_task: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"task":      taskSummary(t),
		"reviewers": reviewerPIDs,
	})
}

func (s *Server) handleClaimTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	taskID := uint64Arg(req, "task_id", 0)
	if taskID == 0 {
		return mcp.NewToolResultError("task_id is required"), nil
	}
	t, err := s.deps.TaskSvc.ClaimTaskByPrincipal(ctx, taskID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("claim_task: %s", err.Error())), nil
	}
	return jsonResult(taskSummary(t))
}

func (s *Server) handleSubmitTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	// content / content_kind 是否必填取决于 task.is_lightweight,service 层校验。
	// 这里不再做"content==''就拒"的早期拦截 —— 让 lightweight 任务的"无文件提交"放行。
	content := stringArg(req, "content", "")
	t, sub, err := s.deps.TaskSvc.SubmitTaskByPrincipal(ctx, SubmitTaskByPrincipalInput{
		TaskID:               uint64Arg(req, "task_id", 0),
		SubmitterPrincipalID: auth.AgentPrincipalID,
		ContentKind:          stringArg(req, "content_kind", ""),
		Content:              []byte(content),
		InlineSummary:        stringArg(req, "inline_summary", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("submit_result: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"task": taskSummary(t),
		"submission": map[string]any{
			"id":                     sub.ID,
			"submitter_principal_id": sub.SubmitterPrincipalID,
			"content_kind":           sub.ContentKind,
			"oss_key":                sub.OSSKey,
			"byte_size":              sub.ByteSize,
			"created_at":             sub.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}

func (s *Server) handleReviewTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	t, rv, err := s.deps.TaskSvc.ReviewTaskByPrincipal(ctx, ReviewTaskByPrincipalInput{
		TaskID:              uint64Arg(req, "task_id", 0),
		SubmissionID:        uint64Arg(req, "submission_id", 0),
		ReviewerPrincipalID: auth.AgentPrincipalID,
		Decision:            stringArg(req, "decision", ""),
		Comment:             stringArg(req, "comment", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("review_task: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"task": taskSummary(t),
		"review": map[string]any{
			"id":                    rv.ID,
			"submission_id":         rv.SubmissionID,
			"reviewer_principal_id": rv.ReviewerPrincipalID,
			"decision":              rv.Decision,
			"comment":               rv.Comment,
			"created_at":            rv.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}

// taskSummary 汇总 task 的常用字段返给 MCP 客户端。
func taskSummary(t *taskmodel.Task) map[string]any {
	m := map[string]any{
		"id":                        t.ID,
		"org_id":                    t.OrgID,
		"channel_id":                t.ChannelID,
		"title":                     t.Title,
		"description":               t.Description,
		"created_by_principal_id":   t.CreatedByPrincipalID,
		"created_via_principal_id":  t.CreatedViaPrincipalID,
		"assignee_principal_id":     t.AssigneePrincipalID,
		"status":                    t.Status,
		"output_spec_kind":          t.OutputSpecKind,
		"is_lightweight":            t.IsLightweight,
		"required_approvals":        t.RequiredApprovals,
		"created_at":                t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if t.SubmittedAt != nil {
		m["submitted_at"] = t.SubmittedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if t.ClosedAt != nil {
		m["closed_at"] = t.ClosedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return m
}
