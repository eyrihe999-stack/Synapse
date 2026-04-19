// Package mcpserver 把 retrieval registry 以 MCP-over-HTTP 方式暴露出来。
//
// 传输:JSON-RPC 2.0 over HTTP POST(MCP streamable-http transport 的最小子集)。
// 支持方法:
//   - initialize      握手,返 protocolVersion + 能力声明
//   - ping            健康检查
//   - tools/list      返 retrieval.BuildTools(reg) 转出的 tool 列表
//   - tools/call      按 tool name 前缀(search_ / fetch_)分发到 Retriever
//
// 暂不实现 resources / prompts / notifications / batch 请求 —— 当前只走工具调用路径。
//
// 鉴权:复用 gin 的 JWTAuth + OrgContextMiddleware;orgID 永远从 gin context 的 Org 对象取,
// 绝不从 LLM 传入的 tool arguments 读取 —— 多租户越权防线。
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	oauthhandler "github.com/eyrihe999-stack/Synapse/internal/oauth/handler"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	oauthsvc "github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgsvc "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/retrieval"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/utils"
)

// mcpProtocolVersion MCP 规范里 server 声明支持的协议版本。2024-11-05 是当前稳定版。
const mcpProtocolVersion = "2024-11-05"

// Server 持 registry + logger + 可选 extra tool handlers,无其他可变状态,并发安全。
type Server struct {
	reg          *retrieval.Registry
	log          logger.LoggerInterface
	extraTools   map[string]ExtraToolHandler // name → handler
}

// New 构造。reg / log 必须非 nil。extraHandlers 可空,用来挂 invoke_agent 这类"非检索"的工具。
func New(reg *retrieval.Registry, log logger.LoggerInterface, extraHandlers ...ExtraToolHandler) *Server {
	if reg == nil || log == nil {
		panic("mcpserver: reg and log must be non-nil")
	}
	extras := make(map[string]ExtraToolHandler, len(extraHandlers))
	for _, h := range extraHandlers {
		if h == nil {
			continue
		}
		name := h.Name()
		if _, dup := extras[name]; dup {
			panic("mcpserver: duplicate extra tool name: " + name)
		}
		extras[name] = h
	}
	return &Server{reg: reg, log: log, extraTools: extras}
}

// RegisterRoutes 挂 JWT-保护 legacy 路径 /api/v2/orgs/:slug/retrieval/mcp。
// 保留是为了 stdio bridge 过渡;新 agent(Claude Desktop 一键连)走 RegisterOAuthRoutes。
func RegisterRoutes(
	r *gin.Engine,
	s *Server,
	jwtManager *utils.JWTManager,
	orgSvc orgsvc.OrgService,
	roleSvc orgsvc.RoleService,
	log logger.LoggerInterface,
) {
	g := r.Group("/api/v2/orgs/:slug/retrieval")
	g.Use(
		middleware.JWTAuth(jwtManager),
		orghandler.OrgContextMiddleware(orgSvc, roleSvc, log),
	)
	g.POST("/mcp", s.Handle)
}

// RegisterOAuthRoutes 挂 OAuth-保护的 /api/v2/retrieval/mcp。
// 路径不带 :slug —— org 从 access token 的 claims 取,天然防篡改。
// Claude Desktop / Cursor 这类 MCP client 完成 OAuth flow 后直接连此端点。
//
// resourceMetadataURL 是 /.well-known/oauth-protected-resource 的绝对 URL;MCP 客户端
// 首次 POST 无 token → 401 带此 URL → 发现 AS → 走 OAuth → 重试。
func RegisterOAuthRoutes(
	r *gin.Engine,
	s *Server,
	oauthSvc oauthsvc.Service,
	resourceMetadataURL string,
	log logger.LoggerInterface,
) {
	// CORS 先于 OAuth middleware —— preflight(OPTIONS)不带 Authorization,走不到 token 校验就得回 204。
	cors := oauthhandler.CORSAllowAll()
	g := r.Group("/api/v2/retrieval", cors)
	g.OPTIONS("/mcp") // preflight,cors middleware 直接 204
	// 真正的 POST 走 OAuth token 校验
	g.POST("/mcp", oauthmw.AccessToken(oauthSvc, resourceMetadataURL, log), s.Handle)
}

// ─── JSON-RPC 2.0 envelope ──────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // 可以是 string / number / null,保留 RawMessage 避免类型误判
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC 2.0 错误码。-32000..-32099 段留给 MCP 自定义实现,目前没用。
const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternalError  = -32603
)

// ─── MCP 响应结构 ───────────────────────────────────────────────────────────

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      map[string]any `json:"serverInfo"`
}

type toolsListResult struct {
	Tools []mcpTool `json:"tools"`
}

// mcpTool MCP 规范 tool descriptor。注意:MCP 里字段名是 camelCase 的 inputSchema,
// 和 retrieval.Tool 的 input_schema 不同,此处做显式转换。
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ContentBlock tool call 响应的内容块;当前只用 text 类型。
// 导出给 ExtraToolHandler 实现方构造返回值用。
type ContentBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// contentBlock 内部别名保持现有代码一致,新代码用 ContentBlock。
type contentBlock = ContentBlock

// toolsCallResult MCP 规范:工具执行错误走 isError+content,而非 JSON-RPC error —— 让 LLM
// 能看到错误文本并决定 retry / 换策略,不是把整条 RPC 当失败吃掉。
type toolsCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ExtraToolHandler 用来挂"不属于 Retriever 搜索/获取语义"的工具(例如 invoke_agent)。
// 这类工具不经 retrieval.Registry,Server 直接按 Name() 分发。
//
// 为什么不合进 Retriever:
//   - Retriever 语义是 Search + FetchByID(检索),invoke_agent 是 action(调用)
//   - 如果强行把 action 塞进 Retriever,Retriever 接口变臃肿,破坏分层
//
// 约定:
//   - Name() 不带 search_ / fetch_ 前缀,避免和 Retriever 衍生的 tool 撞名
//   - Invoke 返回的 content + isError 原样装进 MCP tools/call 响应
//   - err != nil 会转成 "content=['tool error: ...'] + isError=true",让 LLM 看到
type ExtraToolHandler interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Invoke(ctx context.Context, orgID uint64, args json.RawMessage) (content []ContentBlock, isError bool, err error)
}

// ─── Dispatcher ─────────────────────────────────────────────────────────────

// Handle 处理一次 JSON-RPC 请求。不支持 batch(数组请求)—— 现阶段 YAGNI。
func (s *Server) Handle(c *gin.Context) {
	var req rpcRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, nil, errParseError, "parse error", err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeError(c, req.ID, errInvalidRequest, "jsonrpc must be \"2.0\"", nil)
		return
	}

	switch req.Method {
	case "initialize":
		writeResult(c, req.ID, initializeResult{
			ProtocolVersion: mcpProtocolVersion,
			Capabilities: map[string]any{
				// listChanged=false 声明我们不会发 tools/list_changed 通知;
				// 本服务器当前根本不发 notifications,所以任何 listChanged 都填 false。
				"tools": map[string]any{"listChanged": false},
			},
			ServerInfo: map[string]any{
				"name":    "synapse-retrieval",
				"version": "0.1.0",
			},
		})
	case "ping":
		writeResult(c, req.ID, map[string]any{})
	case "tools/list":
		s.handleToolsList(c, req.ID)
	case "tools/call":
		s.handleToolsCall(c, &req)
	default:
		writeError(c, req.ID, errMethodNotFound, "method not found: "+req.Method, nil)
	}
}

func (s *Server) handleToolsList(c *gin.Context, id json.RawMessage) {
	raw := retrieval.BuildTools(s.reg)
	tools := make([]mcpTool, 0, len(raw)+len(s.extraTools))
	for _, t := range raw {
		tools = append(tools, mcpTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	// extras 顺序不保证稳定(map 遍历),但名字唯一不会重复。
	for _, h := range s.extraTools {
		tools = append(tools, mcpTool{
			Name:        h.Name(),
			Description: h.Description(),
			InputSchema: h.InputSchema(),
		})
	}
	writeResult(c, id, toolsListResult{Tools: tools})
}

func (s *Server) handleToolsCall(c *gin.Context, req *rpcRequest) {
	// orgID 来源两条路径:OAuth claims(新路径)/ OrgContextMiddleware 注入的 Org 模型(legacy)。
	orgID, ok := extractOrgID(c)
	if !ok {
		writeError(c, req.ID, errInternalError, "org context missing", nil)
		return
	}

	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(c, req.ID, errInvalidParams, "invalid tools/call params", err.Error())
		return
	}
	if params.Name == "" {
		writeError(c, req.ID, errInvalidParams, "tool name required", nil)
		return
	}

	ctx := c.Request.Context()
	result, err := s.dispatch(ctx, orgID, params.Name, params.Arguments)
	if err != nil {
		// 记一条 warn 便于运维排查(审计 + prompt injection 调试);
		// 返给 LLM 的仍然是 content+isError,保证它能看到错误并决定下一步。
		s.log.WarnCtx(ctx, "mcpserver: tool call failed", map[string]any{
			"org_id": orgID, "tool": params.Name, "err": err.Error(),
		})
		writeResult(c, req.ID, toolsCallResult{
			Content: []contentBlock{{Type: "text", Text: "tool error: " + err.Error()}},
			IsError: true,
		})
		return
	}
	writeResult(c, req.ID, result)
}

// extractOrgID 优先 OAuth claims(新路径),fallback 到 OrgContextMiddleware 注入的 Org(legacy)。
// 两条路径任一命中就够,middleware 层已保证 orgID 来源可信。
func extractOrgID(c *gin.Context) (uint64, bool) {
	if claims, ok := oauthmw.ClaimsFromContext(c); ok && claims != nil && claims.OrgID != 0 {
		return claims.OrgID, true
	}
	if org, ok := orghandler.GetOrg(c); ok && org != nil {
		return org.ID, true
	}
	return 0, false
}

// dispatch 按 tool name 前缀分发:
//   - "search_{modality}" → Retriever.Search
//   - "fetch_{modality}"  → Retriever.FetchByID
//   - 其它               → 查 extraTools(invoke_agent 一类)
//
// 解耦模态扩展:将来新加 image / bug adapter 只需在 registry 里注册,此处零改动。
func (s *Server) dispatch(ctx context.Context, orgID uint64, name string, argsRaw json.RawMessage) (toolsCallResult, error) {
	switch {
	case strings.HasPrefix(name, "search_"):
		return s.doSearch(ctx, orgID, retrieval.Modality(strings.TrimPrefix(name, "search_")), argsRaw)
	case strings.HasPrefix(name, "fetch_"):
		return s.doFetch(ctx, orgID, retrieval.Modality(strings.TrimPrefix(name, "fetch_")), argsRaw)
	}
	if h, ok := s.extraTools[name]; ok {
		content, isErr, err := h.Invoke(ctx, orgID, argsRaw)
		if err != nil {
			return toolsCallResult{}, err
		}
		if len(content) == 0 {
			// 工具返空但不算错:给个空 text content 防 LLM 客户端因 content=[] 而报错
			content = []ContentBlock{{Type: "text", Text: ""}}
		}
		return toolsCallResult{Content: content, IsError: isErr}, nil
	}
	return toolsCallResult{}, fmt.Errorf("unknown tool: %s", name)
}

func (s *Server) doSearch(ctx context.Context, orgID uint64, m retrieval.Modality, argsRaw json.RawMessage) (toolsCallResult, error) {
	rv, ok := s.reg.Get(m)
	if !ok {
		return toolsCallResult{}, fmt.Errorf("modality not registered: %s", m)
	}

	var args struct {
		Query  string          `json:"query"`
		TopK   int             `json:"top_k"`
		Mode   string          `json:"mode"`
		Rerank bool            `json:"rerank"`
		Filter json.RawMessage `json:"filter,omitempty"`
	}
	if len(argsRaw) > 0 {
		if err := json.Unmarshal(argsRaw, &args); err != nil {
			return toolsCallResult{}, fmt.Errorf("parse arguments: %w", err)
		}
	}
	if args.Query == "" {
		return toolsCallResult{}, errors.New("query required")
	}

	hits, err := rv.Search(ctx, retrieval.Query{
		OrgID:    orgID, // 强制由 middleware 注入的 org,不吃 LLM args 里的 org
		Modality: m,
		Text:     args.Query,
		TopK:     args.TopK,
		Mode:     retrieval.RetrieveMode(args.Mode),
		Rerank:   args.Rerank,
		Filter:   args.Filter,
	})
	if err != nil {
		return toolsCallResult{}, err
	}
	return toolJSON(hits), nil
}

func (s *Server) doFetch(ctx context.Context, orgID uint64, m retrieval.Modality, argsRaw json.RawMessage) (toolsCallResult, error) {
	rv, ok := s.reg.Get(m)
	if !ok {
		return toolsCallResult{}, fmt.Errorf("modality not registered: %s", m)
	}

	var args struct {
		ID string `json:"id"`
	}
	if len(argsRaw) > 0 {
		if err := json.Unmarshal(argsRaw, &args); err != nil {
			return toolsCallResult{}, fmt.Errorf("parse arguments: %w", err)
		}
	}
	if args.ID == "" {
		return toolsCallResult{}, errors.New("id required")
	}

	hit, err := rv.FetchByID(ctx, orgID, args.ID)
	if err != nil {
		return toolsCallResult{}, err
	}
	if hit == nil {
		return toolJSON(map[string]any{"found": false, "id": args.ID}), nil
	}
	return toolJSON(hit), nil
}

// toolJSON 把任意结构序列化为 text content block;LLM 解析 JSON 比解析自由文本靠谱得多。
// MarshalIndent 代价小但可读性好,对 LLM 上下文窗口也只多几个空格。
func toolJSON(v any) toolsCallResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolsCallResult{
			Content: []contentBlock{{Type: "text", Text: "marshal error: " + err.Error()}},
			IsError: true,
		}
	}
	return toolsCallResult{Content: []contentBlock{{Type: "text", Text: string(b)}}}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func writeResult(c *gin.Context, id json.RawMessage, result any) {
	c.JSON(http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(c *gin.Context, id json.RawMessage, code int, message string, data any) {
	c.JSON(http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
