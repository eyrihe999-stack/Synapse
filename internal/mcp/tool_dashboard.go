package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	taskerr "github.com/eyrihe999-stack/Synapse/internal/task"
	taskmodel "github.com/eyrihe999-stack/Synapse/internal/task/model"
)

// registerDashboardTools 注册 user-centric 一站式聚合 tool。
//
// 唯一 tool:
//   - get_my_dashboard   一次拉回 caller 的 (我作为 assignee 待做的 task) +
//                        (我作为 reviewer 待审的 task) + (最近 N 个被 @ 的 mention) +
//                        (我参与的 channel 列表)
//
// 设计意图:替代"agent 一次对话调 4-5 个 list_* tool"的笨重模式 —— 用户问"看下
// Synapse 我有什么要处理"时,LLM 调一次本工具就能拿到全景。
//
// 实现:facade 层的 4 次顺序调用,每次都很轻;不并发(errgroup 复杂度不值得)。
// 任一子查询失败整体返错(不做 partial result —— 要么齐要么错,LLM 易理解)。
func (s *Server) registerDashboardTools() {
	if s.deps.ChannelSvc == nil || s.deps.TaskSvc == nil {
		return
	}
	s.mcp.AddTool(mcp.NewTool("get_my_dashboard",
		mcp.WithDescription("One-shot aggregate of the current agent's pending work: "+
			"(1) tasks where I'm the assignee in non-terminal status, "+
			"(2) tasks where I'm a reviewer awaiting my review, "+
			"(3) latest channel mentions of me / my owner-user, "+
			"(4) channels I'm a member of. "+
			"Use this as the entrypoint when a user asks 'what do I have on Synapse'."),
		mcp.WithNumber("task_limit", mcp.Description("Max tasks per role (assignee / reviewer). Default 20, max 50."), mcp.DefaultNumber(20)),
		mcp.WithNumber("mention_limit", mcp.Description("Max recent mentions. Default 10, max 50."), mcp.DefaultNumber(10)),
		mcp.WithNumber("channel_limit", mcp.Description("Max channels listed. Default 30, max 100."), mcp.DefaultNumber(30)),
	), s.handleGetMyDashboard)
}

func (s *Server) handleGetMyDashboard(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	taskLimit := capInt(intArg(req, "task_limit", 20), 1, 50)
	mentionLimit := capInt(intArg(req, "mention_limit", 10), 1, 50)
	channelLimit := capInt(intArg(req, "channel_limit", 30), 1, 100)

	// 1. 我作为 assignee:status="" 拿全部派给我的(LLM 自行筛终态,服务端不做 IN status 过滤简化第一版)
	assigneeTasks, err := s.deps.TaskSvc.ListMyTasksForPrincipal(ctx, auth.AgentPrincipalID, "assignee", "", taskLimit, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_my_dashboard.assignee_tasks: %s", err.Error())), nil
	}
	// 2. 我作为 reviewer 待审:status=submitted(只有 submitted 才需要 reviewer 行动)
	reviewerTasks, err := s.deps.TaskSvc.ListMyTasksForPrincipal(ctx, auth.AgentPrincipalID, "reviewer", taskerr.StatusSubmitted, taskLimit, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_my_dashboard.reviewer_tasks: %s", err.Error())), nil
	}
	// 3. 最近 mentions
	mentions, err := s.deps.ChannelSvc.ListMyMentionsForPrincipal(ctx, auth.AgentPrincipalID, 0, mentionLimit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_my_dashboard.mentions: %s", err.Error())), nil
	}
	// 4. 参与的 channels
	channels, err := s.deps.ChannelSvc.ListChannelsByUserPrincipal(ctx, auth.AgentPrincipalID, channelLimit, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_my_dashboard.channels: %s", err.Error())), nil
	}

	out := map[string]any{
		"assignee_tasks": briefTasks(assigneeTasks, "assignee"),
		"reviewer_tasks": briefTasks(reviewerTasks, "reviewer"),
		"mentions":       briefMentionsForDashboard(mentions),
		"channels":       briefChannelsForDashboard(channels),
	}
	return jsonResult(out)
}

// briefTasks 把 task 截成 dashboard 用的最小集 —— 只留决策用字段,大字段(description)省去。
func briefTasks(ts []taskmodel.Task, role string) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, map[string]any{
			"id":         t.ID,
			"channel_id": t.ChannelID,
			"title":      t.Title,
			"status":     t.Status,
			"role":       role,
		})
	}
	return out
}

func briefMentionsForDashboard(mentions []channelsvc.MentionItem) []map[string]any {
	out := make([]map[string]any, 0, len(mentions))
	for _, m := range mentions {
		out = append(out, map[string]any{
			"message_id":          m.MessageID,
			"channel_id":          m.ChannelID,
			"author_principal_id": m.AuthorPrincipalID,
			"body":                m.Body,
			"kind":                m.Kind,
			"created_at":          m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out
}

func briefChannelsForDashboard(cs []channelmodel.Channel) []map[string]any {
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]any{
			"id":         c.ID,
			"name":       c.Name,
			"project_id": c.ProjectID,
			"status":     c.Status,
		})
	}
	return out
}

// capInt 把 v 夹到 [lo, hi] 区间。
func capInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
