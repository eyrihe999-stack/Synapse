// const.go agent 模块常量定义。
package agent

import "time"

// ─── 表名 ─────────────────────────────────────────────────────────────────────

const (
	// TableAgents agent 主表
	TableAgents = "agents"
	// TableAgentMethods method 声明表
	TableAgentMethods = "agent_methods"
	// TableAgentSecrets HMAC secret 加密存储表
	//sayso-lint:ignore hardcoded-secret
	TableAgentSecrets = "agent_secrets" // 表名,不是凭证
	// TableAgentPublishes agent → org 的发布关系表
	TableAgentPublishes = "agent_publishes"
	// TableAgentInvocations 调用审计主表(月分区)
	TableAgentInvocations = "agent_invocations"
	// TableAgentInvocationPayloads 调用 payload 表(月分区)
	TableAgentInvocationPayloads = "agent_invocation_payloads"
)

// ─── Agent 协议 / 状态 ────────────────────────────────────────────────────────

const (
	// ProtocolJSONRPC 第一版唯一支持的协议
	ProtocolJSONRPC = "jsonrpc"
)

const (
	// DiscoveryModeManual 手动声明 method(第一版唯一支持)
	DiscoveryModeManual = "manual"
	// DiscoveryModeAuto 自动发现(预留,第一版拒绝)
	DiscoveryModeAuto = "auto"
)

const (
	// AgentStatusActive agent 正常状态
	AgentStatusActive = "active"
	// AgentStatusBanned agent 被封禁
	AgentStatusBanned = "banned"
)

const (
	// HealthStatusUnknown 未知(初始状态)
	HealthStatusUnknown = "unknown"
	// HealthStatusHealthy 健康
	HealthStatusHealthy = "healthy"
	// HealthStatusUnhealthy 不健康(连续失败超阈值)
	HealthStatusUnhealthy = "unhealthy"
)

// ─── Method transport / visibility ───────────────────────────────────────────

const (
	// TransportHTTP 同步 HTTP 响应(第一版支持)
	TransportHTTP = "http"
	// TransportSSE 流式 Server-Sent Events(第一版支持)
	TransportSSE = "sse"
	// TransportWS WebSocket(预留,第一版拒绝)
	TransportWS = "ws"
)

const (
	// VisibilityPublic 对所有已发布 org 的成员可见
	VisibilityPublic = "public"
	// VisibilityPrivate 仅作者可调用
	VisibilityPrivate = "private"
)

// ─── Publish 状态 ─────────────────────────────────────────────────────────────

const (
	// PublishStatusPending 待审核
	PublishStatusPending = "pending"
	// PublishStatusApproved 已通过
	PublishStatusApproved = "approved"
	// PublishStatusRejected 审核被拒
	PublishStatusRejected = "rejected"
	// PublishStatusRevoked 已撤销(含主动撤销、成员离开联动、org 解散联动等)
	PublishStatusRevoked = "revoked"
)

// Publish 撤销原因枚举
const (
	// RevokedReasonAuthorRemoved 作者被踢出 org
	RevokedReasonAuthorRemoved = "author_removed"
	// RevokedReasonMemberLeft 作者主动退出 org
	RevokedReasonMemberLeft = "member_left"
	// RevokedReasonAdminBanned 管理员封禁
	RevokedReasonAdminBanned = "admin_banned"
	// RevokedReasonAuthorUnpublished 作者主动下架
	RevokedReasonAuthorUnpublished = "author_unpublished"
	// RevokedReasonOrgDissolved org 解散联动
	RevokedReasonOrgDissolved = "org_dissolved"
)

// ─── Invocation 状态 ──────────────────────────────────────────────────────────

const (
	// InvocationStatusPending 已创建,尚未发出上游请求
	InvocationStatusPending = "pending"
	// InvocationStatusRunning 正在转发中
	InvocationStatusRunning = "running"
	// InvocationStatusSucceeded 成功完成
	InvocationStatusSucceeded = "succeeded"
	// InvocationStatusFailed 上游返回失败或本地错误
	InvocationStatusFailed = "failed"
	// InvocationStatusTimeout 超时
	InvocationStatusTimeout = "timeout"
	// InvocationStatusCanceled 被客户端取消
	InvocationStatusCanceled = "canceled"
	// InvocationStatusRateLimited 被限流拒绝
	InvocationStatusRateLimited = "rate_limited"
)

// ─── 默认值与上限 ─────────────────────────────────────────────────────────────

const (
	// DefaultTimeoutSeconds agent 默认请求超时
	DefaultTimeoutSeconds = 30
	// MaxTimeoutSeconds agent 请求超时上限
	MaxTimeoutSeconds = 120

	// DefaultRateLimitPerMinute agent 默认限流(每分钟)
	DefaultRateLimitPerMinute = 60
	// MaxRateLimitPerMinute agent 限流上限
	MaxRateLimitPerMinute = 600

	// DefaultMaxConcurrent agent 默认并发上限
	DefaultMaxConcurrent = 5
	// MaxMaxConcurrent agent 并发上限的上限
	MaxMaxConcurrent = 20

	// DefaultHealthCheckIntervalSeconds 健康检查间隔
	DefaultHealthCheckIntervalSeconds = 5
	// DefaultHealthFailThreshold 连续失败阈值,达到后标记 unhealthy
	DefaultHealthFailThreshold = 3
	// DefaultHealthCheckConcurrency 健康检查并发上限
	DefaultHealthCheckConcurrency = 50

	// DefaultHMACTimestampSkewSeconds 签名允许的时间戳偏差
	DefaultHMACTimestampSkewSeconds = 300
	// DefaultHMACNonceCacheSeconds nonce 防重放缓存时长
	DefaultHMACNonceCacheSeconds = 600

	// DefaultAuditBaseRetentionDays 基础审计保留天数
	DefaultAuditBaseRetentionDays = 90
	// DefaultAuditPayloadRetentionDays payload 保留天数
	DefaultAuditPayloadRetentionDays = 30

	// DefaultUserGlobalRatePerMinute 用户全局限流
	DefaultUserGlobalRatePerMinute = 1000
	// DefaultOrgGlobalRatePerMinute org 全局限流
	DefaultOrgGlobalRatePerMinute = 10000
	// DefaultUserAgentRatePerMinute 用户+agent 组合限流
	DefaultUserAgentRatePerMinute = 30

	// PayloadTruncateSize payload 截断上限(32KB)
	PayloadTruncateSize = 32 * 1024

	// DefaultPageSize 列表接口默认分页大小
	DefaultPageSize = 20
	// MaxPageSize 列表接口最大分页大小
	MaxPageSize = 100

	// MinAgentSlugLength agent slug 最小长度
	MinAgentSlugLength = 3
	// MaxAgentSlugLength agent slug 最大长度
	MaxAgentSlugLength = 64
	// MaxAgentDisplayNameLength agent display_name 最大长度
	MaxAgentDisplayNameLength = 128
	// MaxAgentDescriptionLength agent 描述最大长度
	MaxAgentDescriptionLength = 1000

	// MaxMethodNameLength method_name 最大长度
	MaxMethodNameLength = 64
	// MaxMethodDisplayNameLength method display_name 最大长度
	MaxMethodDisplayNameLength = 128
	// MaxMethodDescriptionLength method 描述最大长度
	MaxMethodDescriptionLength = 500

	// SecretByteLength 明文 HMAC secret 字节数
	SecretByteLength = 64
	// SecretRotateGraceHours rotate 期间新旧 secret 共存的时长(小时)
	SecretRotateGraceHours = 24

	// HealthCheckRequestTimeoutSeconds 健康检查单次 HTTP 请求超时
	HealthCheckRequestTimeoutSeconds = 3

	// CancelFlagTTLSeconds 取消标志 Redis TTL
	CancelFlagTTLSeconds = 600
	// CancelPollIntervalSeconds 转发 goroutine 轮询取消标志的间隔
	CancelPollIntervalSeconds = 2
	// CancelPubSubChannel Redis pub/sub 频道名
	CancelPubSubChannel = "synapse:cancel"
)

// ─── 正则 ────────────────────────────────────────────────────────────────────

// AgentSlugPattern agent slug:小写字母开头,允许字母数字和连字符,3-64 字符。
const AgentSlugPattern = `^[a-z][a-z0-9-]{2,63}$`

// MethodNamePattern JSON-RPC method 名:字母开头,允许字母数字、点、下划线。
const MethodNamePattern = `^[A-Za-z][A-Za-z0-9._]{0,63}$`

// ─── 后台任务间隔 ─────────────────────────────────────────────────────────────

// PartitionMaintainInterval 分区维护定时任务执行间隔(每天一次)。
const PartitionMaintainInterval = 24 * time.Hour
