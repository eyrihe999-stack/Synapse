package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerMentionTools 注册"被 @"相关 inbox 类 tool。
//
// 当前唯一 tool:
//   - list_my_mentions   跨 channel 列 caller 被 @ 的消息(按 message_id DESC)
//
// 用途:agent / user 用本工具实现"我离开后被人 @ 了哪些"的 pull 模式 inbox。
// 配合参数 since_message_id(取自上次最大 id)做增量查询。
func (s *Server) registerMentionTools() {
	if s.deps.ChannelSvc == nil {
		return
	}
	s.mcp.AddTool(mcp.NewTool("list_my_mentions",
		mcp.WithDescription("List messages across all channels where the current agent / its owner-user "+
			"was @ mentioned. Sorted newest first. "+
			"Pass `since_message_id` (last seen mention id) to get only new ones; "+
			"pass `limit` to control page size (default 20, max 100)."),
		mcp.WithNumber("since_message_id", mcp.Description("Cursor: only return mentions with message_id > this. Default 0 = latest page.")),
		mcp.WithNumber("limit", mcp.Description("Page size, default 20, max 100"), mcp.DefaultNumber(20)),
	), s.handleListMyMentions)
}

func (s *Server) handleListMyMentions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	since := uint64Arg(req, "since_message_id", 0)
	limit := intArg(req, "limit", 20)

	rows, err := s.deps.ChannelSvc.ListMyMentionsForPrincipal(ctx, auth.AgentPrincipalID, since, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_my_mentions: %s", err.Error())), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"message_id":          r.MessageID,
			"channel_id":          r.ChannelID,
			"author_principal_id": r.AuthorPrincipalID,
			"body":                r.Body,
			"kind":                r.Kind,
			"created_at":          r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return jsonResult(out)
}
