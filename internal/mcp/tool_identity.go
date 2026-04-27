package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerIdentityTools 注册 whoami 一类的"自我介绍"工具。让 LLM 在多 agent /
// 多 channel 场景下清楚"当前调 tool 的我是谁、owner 是谁"。
func (s *Server) registerIdentityTools() {
	if s.deps.IdentitySvc == nil {
		return
	}

	whoamiTool := mcp.NewTool("whoami",
		mcp.WithDescription("Return the current agent's identity: principal_id, agent_id slug, "+
			"display_name, kind ('user' | 'system'), and (for personal agents) the owner user's "+
			"id + display_name. Call this once at the start of a conversation so you know "+
			"which principal_id to skip when @-mentioning channel members or assigning tasks "+
			"(don't @ yourself), and so user-facing messages can use the right name."),
	)
	s.mcp.AddTool(whoamiTool, s.handleWhoami)
}

func (s *Server) handleWhoami(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	id, err := s.deps.IdentitySvc.LookupByAgentPrincipal(ctx, auth.AgentPrincipalID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("whoami: %s", err.Error())), nil
	}
	return jsonResult(id)
}
