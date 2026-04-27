package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerChannelTools 注册 channel 相关的 MCP tools。
//
// Tools:
//   - list_channels           拉 agent 能看到的所有 channel(按其 principal 在 channel_members 的)
//   - get_channel             拿 channel 详情 + 最近 N 条消息 + mentions
//   - post_message            发消息;前端/agent 解析 @xxx 后传 mentions 数组
//
// 所有 tool 都从 ctx 里取当前 agent 的 AgentPrincipalID(由 BearerAuth 中间件注入),
// 不信任 MCP 请求体里传的 principal_id(防越权)。
func (s *Server) registerChannelTools() {
	if s.deps.ChannelSvc == nil {
		return
	}

	// ── list_channels ──────────────────────────────────────────────────
	listTool := mcp.NewTool("list_channels",
		mcp.WithDescription("List channels the current agent is a member of."),
		mcp.WithNumber("limit", mcp.Description("Page size, default 50, max 200"), mcp.DefaultNumber(50)),
		mcp.WithNumber("offset", mcp.Description("Offset for pagination, default 0"), mcp.DefaultNumber(0)),
	)
	s.mcp.AddTool(listTool, s.handleListChannels)

	// ── get_channel ────────────────────────────────────────────────────
	getTool := mcp.NewTool("get_channel",
		mcp.WithDescription("Get channel details + most recent messages (with mentions)."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Target channel id")),
		mcp.WithNumber("message_limit", mcp.Description("Recent messages to include, default 30, max 100"), mcp.DefaultNumber(30)),
	)
	s.mcp.AddTool(getTool, s.handleGetChannel)

	// ── post_message ───────────────────────────────────────────────────
	postTool := mcp.NewTool("post_message",
		mcp.WithDescription("Post a markdown message into the channel. The current agent becomes the author. "+
			"Pass `mentions` as an array of principal_id; the server does NOT parse @xxx text. "+
			"Pass `reply_to_message_id` to mark this post as a reply to another message in the same channel (renders as a quoted card in UI)."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Target channel id")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Markdown message body")),
		mcp.WithArray("mentions", mcp.Description("principal_ids to @ mention"),
			mcp.Items(map[string]any{"type": "number"}),
		),
		mcp.WithNumber("reply_to_message_id", mcp.Description("Optional: id of the message this post replies to (must be in the same channel)")),
	)
	s.mcp.AddTool(postTool, s.handlePostMessage)

	// ── add_reaction / remove_reaction(PR #12')─────────────────────────
	addReactTool := mcp.NewTool("add_reaction",
		mcp.WithDescription("React to a message with an emoji. Allowed emojis: 👍 👎 ❤️ 🎉 🚀 👀 🙏 😂 🔥 ✅ ❌ 🤔. "+
			"Same (message, agent, emoji) is idempotent; archived channels are rejected."),
		mcp.WithNumber("message_id", mcp.Required(), mcp.Description("Target message id")),
		mcp.WithString("emoji", mcp.Required(), mcp.Description("One of the allowed reaction emojis")),
	)
	s.mcp.AddTool(addReactTool, s.handleAddReaction)

	rmReactTool := mcp.NewTool("remove_reaction",
		mcp.WithDescription("Remove a previously-added reaction from the current agent. Missing reaction is treated as success."),
		mcp.WithNumber("message_id", mcp.Required(), mcp.Description("Target message id")),
		mcp.WithString("emoji", mcp.Required(), mcp.Description("Emoji to remove")),
	)
	s.mcp.AddTool(rmReactTool, s.handleRemoveReaction)

	// ── list_channel_members ───────────────────────────────────────────
	membersTool := mcp.NewTool("list_channel_members",
		mcp.WithDescription("List members of a channel with their principal_id, display_name and kind. "+
			"Use this BEFORE create_task / post_message with mentions to translate human names "+
			"(e.g. 'Bob') into principal_id. You must be a member of the channel to call this."),
		mcp.WithNumber("channel_id", mcp.Required(), mcp.Description("Target channel id")),
	)
	s.mcp.AddTool(membersTool, s.handleListChannelMembers)
}

func (s *Server) handleListChannelMembers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	rows, err := s.deps.ChannelSvc.ListChannelMembersForPrincipal(ctx, channelID, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_channel_members: %s", err.Error())), nil
	}
	type item struct {
		PrincipalID   uint64 `json:"principal_id"`
		Role          string `json:"role"`
		DisplayName   string `json:"display_name"`
		Kind          string `json:"kind"`
		IsGlobalAgent bool   `json:"is_global_agent,omitempty"`
	}
	out := make([]item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item{
			PrincipalID:   r.PrincipalID,
			Role:          r.Role,
			DisplayName:   r.DisplayName,
			Kind:          r.Kind,
			IsGlobalAgent: r.IsGlobalAgent,
		})
	}
	return jsonResult(out)
}

func (s *Server) handleAddReaction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	messageID := uint64Arg(req, "message_id", 0)
	emoji := stringArg(req, "emoji", "")
	if messageID == 0 || emoji == "" {
		return mcp.NewToolResultError("message_id and emoji are required"), nil
	}
	if err := s.deps.ChannelSvc.AddReactionByPrincipal(ctx, messageID, auth.AgentPrincipalID, emoji); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("add_reaction: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{"ok": true, "message_id": messageID, "emoji": emoji})
}

func (s *Server) handleRemoveReaction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	messageID := uint64Arg(req, "message_id", 0)
	emoji := stringArg(req, "emoji", "")
	if messageID == 0 || emoji == "" {
		return mcp.NewToolResultError("message_id and emoji are required"), nil
	}
	if err := s.deps.ChannelSvc.RemoveReactionByPrincipal(ctx, messageID, auth.AgentPrincipalID, emoji); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("remove_reaction: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{"ok": true, "message_id": messageID, "emoji": emoji})
}

// ── handlers ────────────────────────────────────────────────────────────

func (s *Server) handleListChannels(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	limit := intArg(req, "limit", 50)
	offset := intArg(req, "offset", 0)

	rows, err := s.deps.ChannelSvc.ListChannelsByUserPrincipal(ctx, auth.AgentPrincipalID, limit, offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_channels: %s", err.Error())), nil
	}

	type item struct {
		ID        uint64 `json:"id"`
		OrgID     uint64 `json:"org_id"`
		ProjectID uint64 `json:"project_id"`
		Name      string `json:"name"`
		Purpose   string `json:"purpose,omitempty"`
		Status    string `json:"status"`
	}
	out := make([]item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item{ID: r.ID, OrgID: r.OrgID, ProjectID: r.ProjectID, Name: r.Name, Purpose: r.Purpose, Status: r.Status})
	}
	return jsonResult(out)
}

func (s *Server) handleGetChannel(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	limit := intArg(req, "message_limit", 30)
	if limit > 100 {
		limit = 100
	}

	cw, err := s.deps.ChannelSvc.GetChannelForPrincipal(ctx, channelID, auth.AgentPrincipalID, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get_channel: %s", err.Error())), nil
	}

	type msg struct {
		ID                uint64   `json:"id"`
		AuthorPrincipalID uint64   `json:"author_principal_id"`
		Body              string   `json:"body"`
		Kind              string   `json:"kind"`
		Mentions          []uint64 `json:"mentions,omitempty"`
		CreatedAt         string   `json:"created_at"`
	}
	msgs := make([]msg, 0, len(cw.Messages))
	for _, m := range cw.Messages {
		msgs = append(msgs, msg{
			ID:                m.ID,
			AuthorPrincipalID: m.AuthorPrincipalID,
			Body:              m.Body,
			Kind:              m.Kind,
			Mentions:          cw.Mentions[m.ID],
			CreatedAt:         m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	out := map[string]any{
		"channel": map[string]any{
			"id":         cw.Channel.ID,
			"org_id":     cw.Channel.OrgID,
			"project_id": cw.Channel.ProjectID,
			"name":       cw.Channel.Name,
			"purpose":    cw.Channel.Purpose,
			"status":     cw.Channel.Status,
		},
		"messages": msgs,
	}
	return jsonResult(out)
}

func (s *Server) handlePostMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	if channelID == 0 {
		return mcp.NewToolResultError("channel_id is required"), nil
	}
	body := stringArg(req, "body", "")
	if body == "" {
		return mcp.NewToolResultError("body is required"), nil
	}
	mentions := uint64ArrayArg(req, "mentions")
	replyTo := uint64Arg(req, "reply_to_message_id", 0)

	m, resolvedMentions, err := s.deps.ChannelSvc.PostMessageAsPrincipal(ctx, channelID, auth.AgentPrincipalID, body, mentions, replyTo)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("post_message: %s", err.Error())), nil
	}

	return jsonResult(map[string]any{
		"id":                   m.ID,
		"channel_id":           m.ChannelID,
		"author_principal_id":  m.AuthorPrincipalID,
		"mentions":             resolvedMentions,
		"reply_to_message_id":  m.ReplyToMessageID,
		"created_at":           m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// ── helpers ────────────────────────────────────────────────────────────

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %s", err.Error())), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func _ensureServerImported() {
	// keep server import referenced even if future tool files don't use it directly
	_ = server.ToolHandlerFunc(nil)
}
