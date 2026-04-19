// const.go agent 模块常量定义。
package agent

// ─── 表名 ─────────────────────────────────────────────────────────────────────

const (
	// TableAgents agent 主表名。
	TableAgents = "agents"
	// TableAgentSessions agent session 表名。
	TableAgentSessions = "agent_sessions"
	// TableAgentMessages agent 消息表名。
	TableAgentMessages = "agent_messages"
	// TableAgentPublishes agent 发布关系表名。
	TableAgentPublishes = "agent_publishes"
)

// ─── Agent 类型 ──────────────────────────────────────────────────────────────

const (
	// AgentTypeChat 交互式对话 agent(Synapse 自定义 JSON + SSE 协议)。
	AgentTypeChat = "chat"
	// AgentTypeTool 工具型 agent,自包含完成特定任务(同 chat 协议)。
	AgentTypeTool = "tool"
	// AgentTypeMCP endpoint 说 MCP 协议(JSON-RPC 2.0 over HTTP)。
	// Synapse 作为透明代理把调用方的 MCP 请求转发到 agent.endpoint_url,响应回传。
	// 调用方(Claude.ai / Cursor / 其它 agent)只要讲 MCP 就行,不用理会 Synapse 私有 chat 协议。
	AgentTypeMCP = "mcp"
)

// ValidAgentTypes 当前系统支持的 agent 类型集合,用于创建/更新时校验。
var ValidAgentTypes = map[string]struct{}{
	AgentTypeChat: {},
	AgentTypeTool: {},
	AgentTypeMCP:  {},
}

// ─── Context 模式 ────────────────────────────────────────────────────────────

const (
	// ContextModeStateless Synapse 管理上下文,每次透传完整 messages 数组
	ContextModeStateless = "stateless"
	// ContextModeStateful Agent 自行管理上下文,Synapse 仅传当前消息 + session_id
	ContextModeStateful = "stateful"
)

// ─── Agent 状态 ──────────────────────────────────────────────────────────────

const (
	// AgentStatusActive agent 处于正常可用状态。
	AgentStatusActive = "active"
	// AgentStatusBanned agent 已被封禁。
	AgentStatusBanned = "banned"
)

// ─── Publish 状态 ────────────────────────────────────────────────────────────

const (
	// PublishStatusPending 发布请求待审核。
	PublishStatusPending = "pending"
	// PublishStatusApproved 发布请求已通过审核。
	PublishStatusApproved = "approved"
	// PublishStatusRejected 发布请求已被拒绝。
	PublishStatusRejected = "rejected"
	// PublishStatusRevoked 发布已被撤销。
	PublishStatusRevoked = "revoked"
)

// ─── Publish 撤销原因 ────────────────────────────────────────────────────────

const (
	// RevokedReasonAuthorRemoved 作者被移出组织导致撤销。
	RevokedReasonAuthorRemoved = "author_removed"
	// RevokedReasonMemberLeft 成员主动离开组织导致撤销。
	RevokedReasonMemberLeft = "member_left"
	// RevokedReasonAdminBanned 管理员封禁 agent 导致撤销。
	RevokedReasonAdminBanned = "admin_banned"
	// RevokedReasonAuthorUnpublished 作者主动取消发布。
	RevokedReasonAuthorUnpublished = "author_unpublished"
	// RevokedReasonOrgDissolved 组织解散导致撤销。
	RevokedReasonOrgDissolved = "org_dissolved"
)

// ─── Message 角色 ────────────────────────────────────────────────────────────

const (
	// RoleUser 用户发送的消息。
	RoleUser = "user"
	// RoleAssistant agent 回复的消息。
	RoleAssistant = "assistant"
	// RoleSystem 系统消息。
	RoleSystem = "system"
)

// ─── 默认值与上限 ────────────────────────────────────────────────────────────

const (
	// DefaultMaxContextRounds 默认最大上下文轮数。
	DefaultMaxContextRounds = 20
	// MinMaxContextRounds 最大上下文轮数下限。
	MinMaxContextRounds = 1
	// MaxMaxContextRounds 最大上下文轮数上限。
	MaxMaxContextRounds = 100

	// DefaultTimeoutSeconds 默认超时时间(秒)。
	DefaultTimeoutSeconds = 30
	// MinTimeoutSeconds 超时时间下限(秒)。
	MinTimeoutSeconds = 5
	// MaxTimeoutSeconds 超时时间上限(秒)。
	MaxTimeoutSeconds = 300

	// DefaultChatRateLimitPerMinute 默认每分钟对话请求限流数。
	DefaultChatRateLimitPerMinute = 60

	// DefaultPageSize 默认分页大小。
	DefaultPageSize = 20
	// MaxPageSize 最大分页大小。
	MaxPageSize = 100

	// MaxTagsCount 单个 agent 最多标签数。
	MaxTagsCount = 20
	// MaxTagLength 单个标签最大字符数。
	MaxTagLength = 50

	// MinAgentSlugLength agent slug 最小长度。
	MinAgentSlugLength = 3
	// MaxAgentSlugLength agent slug 最大长度。
	MaxAgentSlugLength = 64
	// MaxAgentDisplayNameLength agent 显示名最大长度。
	MaxAgentDisplayNameLength = 128
	// MaxAgentDescriptionLength agent 描述最大长度。
	MaxAgentDescriptionLength = 1000
	// MaxSessionTitleLength session 标题最大长度。
	MaxSessionTitleLength = 256

	// MaxUpstreamResponseBytes 上游非流式响应体最大读取字节数(10MB)。
	MaxUpstreamResponseBytes = 10 * 1024 * 1024

	// MaxChatMessageLength 单条对话消息最大字符数。
	MaxChatMessageLength = 8000

	// MaxChatRequestBodyBytes chat 路由单请求 body 上限(128KB)。
	// 计算依据:MaxChatMessageLength=8000 runes × 最多 4 bytes/rune ≈ 32KB,再预留 4 倍给
	// JSON 开销和将来字段扩展。比全局 1MB 限制更严,避免恶意请求在 service 层 rune 校验前
	// 吃掉大量 IO + JSON 解析 CPU。
	MaxChatRequestBodyBytes = 128 * 1024

	// MaxSSELineBytes SSE 单行最大字节数(128KB)。
	MaxSSELineBytes = 128 * 1024
	// MaxStreamContentBytes 流式响应累计 content 最大字节数(1MB)。
	MaxStreamContentBytes = 1024 * 1024
)

// ─── 正则 ────────────────────────────────────────────────────────────────────

// AgentSlugPattern agent slug:小写字母开头,允许字母数字和连字符,3-64 字符。
const AgentSlugPattern = `^[a-z][a-z0-9-]{2,63}$`
