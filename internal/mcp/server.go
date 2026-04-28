// Package mcp Synapse 对 Claude Desktop / Cursor / Codex 等 MCP client 暴露的
// Streamable HTTP server。基于 github.com/mark3labs/mcp-go(支持 MCP spec 2025-11-25)。
//
// 职责:
//   - 装配 MCPServer + 注册所有 tool(channel / task / KB 三组)
//   - 提供 StreamableHTTPServer 作为 http.Handler 挂到 gin 路由 /api/v2/mcp
//   - 不做鉴权 —— 鉴权靠 gin 层的 oauth/middleware.BearerAuth
//     (它把 BearerAuthResult 写进 request.Context 供 tool handler 读)
//
// 设计约束(对齐 docs/collaboration-design.md §3.6):
//   - Tool handler 必须**按 operating_org 严格隔离**:所有数据访问查询带 org_id
//     (通过参数里的 channel_id 反查 operating_org_id,service 层硬过滤)
//   - 禁止任何 tool 跨 operating_org 读数据;单测覆盖 org A 的 call 不应泄露 org B 数据
//
// 历史记录(PR #7' 回滚):
//   - 曾在此处接过 presence tracker + OnRegister/OnUnregister hook,用于服务端 push
//     (notifications/resources/updated)。端到端验证后评估推送对 Claude Desktop 场景
//     收益过低(每 turn 重建 transport → push 窗口极短,和 pull 差异 ≈ 0),于 2026-04-24
//     整体回滚。若未来生态里出现持久 GET 的 client(Cursor / 自建 agent / Synapse-Web
//     的非浏览器桥接等),再按当时需求重新设计,不复用此前代码。
//   - 唯一**保留**的独立修复是 `WithSessionIdleTTL`,用于清理 mcp-go v0.49.0 下
//     POST initialize 注册的 session 永不自动 Unregister 的泄漏 bug —— 此 bug 和推送无关。
package mcp

import (
	"context"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	pmsvc "github.com/eyrihe999-stack/Synapse/internal/pm/service"
)

// sessionIdleTTL mcp-go session sweeper 的 idle 清理阈值。
//
// 为什么必须开:mcp-go v0.49.0 的 POST initialize 会 RegisterSession 并把 session
// 存进 activeSessions;client 若不发 DELETE(Claude Desktop 就不发),session 永不触发
// Unregister → 内部状态 map + registry 累积,久跑后 server 内存泄漏。
//
// 开启后 sweeper 周期性扫描(每 TTL/2 一次),超过 TTL 无活动的 session 会被
// cleanupSessionState 清理。Claude Desktop 典型对话间隔分钟级,5 分钟 idle 阈值足够
// 覆盖对话间隙 + 避免活跃 session 被误清。
//
// **此修复和推送机制无关** —— 即使不做 notifications push 也必须保留。
const sessionIdleTTL = 5 * time.Minute

// passThroughContext 当前是 no-op;保留 hook 点未来加 trace_id / request_id 提取。
// 认证已经由 gin middleware 写进了 request.Context,不需要在这里做。
func passThroughContext(ctx context.Context, _ *http.Request) context.Context {
	return ctx
}

// Config 构造 MCP server 的配置。
type Config struct {
	// ServerName / Version 出现在 MCP initialize handshake 里。
	ServerName    string
	ServerVersion string
}

// Deps 注入的外部依赖 —— channel / task / KB / 共享文档 / 附件的 service。
// 这里刻意用 interface 而非具体 struct 便于测试;实际实现在 main.go 直接注入具体 service 对象。
type Deps struct {
	ChannelSvc    ChannelFacade
	TaskSvc       TaskFacade
	KBSvc         KBFacade
	DocumentSvc   DocumentFacade
	AttachmentSvc AttachmentFacade
	IdentitySvc   IdentityFacade
	// PMSvc PR-B 新加:Project / Initiative / Version / Workstream / KBRef 的 CRUD
	// 直接复用 pm.Service struct(不绕 facade,因为 PM 工具是机械 CRUD 包装,
	// 没有 channel/task 那种"by user vs by principal"的双轨复杂度)。
	PMSvc *pmsvc.Service
	Log   logger.LoggerInterface
}

// Server 是一个 MCP server + Streamable HTTP transport 的组合体。
// 对外只暴露 HTTPHandler(),调用方把它挂到 gin.Any 即可。
type Server struct {
	cfg        Config
	deps       Deps
	mcp        *server.MCPServer
	streamable *server.StreamableHTTPServer
}

// New 构造并注册所有 tools。
func New(cfg Config, deps Deps) *Server {
	if cfg.ServerName == "" {
		cfg.ServerName = "Synapse"
	}
	if cfg.ServerVersion == "" {
		cfg.ServerVersion = "0.1.0"
	}
	s := &Server{cfg: cfg, deps: deps}

	s.mcp = server.NewMCPServer(
		cfg.ServerName,
		cfg.ServerVersion,
		server.WithToolCapabilities(false), // 不动态变 tool list(第一版静态注册)
		server.WithLogging(),
	)

	// 注册 tool —— 由各 tool 文件里的 Register* 函数填充
	s.registerChannelTools()
	s.registerTaskTools()
	s.registerKBTools()
	s.registerIdentityTools()
	s.registerDocumentTools()
	s.registerAttachmentTools()
	s.registerMentionTools()
	s.registerDashboardTools()
	// PR-B PM 工具组
	s.registerInitiativeTools()
	s.registerVersionTools()
	s.registerWorkstreamTools()
	s.registerProjectKBTools()

	// Streamable HTTP 本体
	s.streamable = server.NewStreamableHTTPServer(s.mcp,
		// HTTPContextFunc 不做认证(认证在 gin middleware 里已完成,它已经把身份
		// 写进了 request.Context);这里只是一个 hook 点,未来加 trace_id 等可扩
		server.WithHTTPContextFunc(passThroughContext),
		// Session sweeper:清 mcp-go v0.49.0 的 session 泄漏 bug。
		// 详见 sessionIdleTTL 常量注释。
		server.WithSessionIdleTTL(sessionIdleTTL),
	)
	return s
}

// HTTPHandler 返回符合 http.Handler 接口的 MCP 端点处理器。
// gin 调用方:r.Any("/api/v2/mcp/*any", gin.WrapH(mcpServer.HTTPHandler()))
func (s *Server) HTTPHandler() interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
} {
	return s.streamable
}
