// models.go agent 模块数据模型定义。
//
// 6 张表:
//   - Agent:agent 主表
//   - AgentMethod:method 声明(每个 agent >= 1 条)
//   - AgentSecret:HMAC secret 加密存储(current + previous 支持 rotate)
//   - AgentPublish:agent → org 的发布关系
//   - AgentInvocation:调用审计主表(月分区,不走 AutoMigrate)
//   - AgentInvocationPayload:调用 payload(月分区,不走 AutoMigrate)
//
// 约定:
//   - 分区表的 struct 标签仅作为参考,不会被 AutoMigrate 使用(由 partitions.go
//     手写 DDL 创建)
//   - 时间字段用 time.Time,序列化由 service 层负责
package model

import (
	"time"

	"gorm.io/datatypes"
)

// ─── 表名常量(避免 import 根包造成循环依赖) ────────────────────────────────

const (
	tableAgents                  = "agents"
	tableAgentMethods            = "agent_methods"
	//sayso-lint:ignore hardcoded-secret
	tableAgentSecrets = "agent_secrets" // 表名,不是凭证
	tableAgentPublishes          = "agent_publishes"
	tableAgentInvocations        = "agent_invocations"
	tableAgentInvocationPayloads = "agent_invocation_payloads"
)

// ─── 模型级常量(与存储紧密绑定) ────────────────────────────────────────────

const (
	// AgentStatusActive agent 正常状态
	AgentStatusActive = "active"
	// AgentStatusBanned agent 被封禁
	AgentStatusBanned = "banned"
)

const (
	// HealthStatusUnknown 未知
	HealthStatusUnknown = "unknown"
	// HealthStatusHealthy 健康
	HealthStatusHealthy = "healthy"
	// HealthStatusUnhealthy 不健康
	HealthStatusUnhealthy = "unhealthy"
)

const (
	// PublishStatusPending 待审核
	PublishStatusPending = "pending"
	// PublishStatusApproved 已通过
	PublishStatusApproved = "approved"
	// PublishStatusRejected 已拒绝
	PublishStatusRejected = "rejected"
	// PublishStatusRevoked 已撤销
	PublishStatusRevoked = "revoked"
)

// ─── Agent 主表 ───────────────────────────────────────────────────────────────

// Agent 表示一个用户注册的 JSON-RPC Agent。
// OwnerUserID 唯一归属一个用户,Slug 在作者内唯一(作者不同允许重名)。
// Endpoint 必须是 HTTPS,作为网关转发目标。
// 限流字段 TimeoutSeconds / RateLimitPerMinute / MaxConcurrent 可被作者调,受上限约束。
// Status=banned 时所有调用拒绝(全局封禁)。
// HealthStatus 由健康检查任务维护,=unhealthy 时调用快速失败。
type Agent struct {
	ID                  uint64         `gorm:"primaryKey;autoIncrement"`
	OwnerUserID         uint64         `gorm:"not null;index:idx_agents_owner;uniqueIndex:uk_agents_owner_slug,priority:1"`
	Slug                string         `gorm:"size:64;not null;uniqueIndex:uk_agents_owner_slug,priority:2"`
	DisplayName         string         `gorm:"size:128;not null"`
	Description         string         `gorm:"size:1000"`
	Protocol            string         `gorm:"size:16;not null;default:jsonrpc"`
	EndpointURL         string         `gorm:"size:512;not null"`
	DiscoveryMode       string         `gorm:"size:16;not null;default:manual"`
	AllowUnknownMethods bool           `gorm:"not null;default:false"`
	IconURL             string         `gorm:"size:512"`
	Tags                datatypes.JSON `gorm:"type:json"`
	HomepageURL         string         `gorm:"size:512"`
	PriceTag            string         `gorm:"size:32"`
	DeveloperContact    string         `gorm:"size:128"`
	Version             string         `gorm:"size:32"`
	TimeoutSeconds      int            `gorm:"not null;default:30"`
	RateLimitPerMinute  int            `gorm:"not null;default:60"`
	MaxConcurrent       int            `gorm:"not null;default:5"`
	Status              string         `gorm:"size:16;not null;default:active;index:idx_agents_status"`
	HealthStatus        string         `gorm:"size:16;not null;default:unknown;index:idx_agents_health"`
	HealthCheckedAt     *time.Time
	HealthFailCount     int       `gorm:"not null;default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TableName 返回 agent 表名。
func (Agent) TableName() string { return tableAgents }

// ─── AgentMethod method 声明 ────────────────────────────────────────────────

// AgentMethod 是 agent 的 method 一等实体。
// 每个 agent 至少有 1 条(强制);删除时必须保留至少 1 条。
// Transport 决定响应模式(http 同步 / sse 流式);ws 第一版拒绝。
// Visibility 控制调用可见性:public 对所有已发布 org 成员,private 仅作者。
// InputSchema / OutputSchema 是预留的 JSON Schema 字段。
type AgentMethod struct {
	ID           uint64         `gorm:"primaryKey;autoIncrement"`
	AgentID      uint64         `gorm:"not null;index:idx_methods_agent;uniqueIndex:uk_methods_agent_name,priority:1"`
	MethodName   string         `gorm:"size:64;not null;uniqueIndex:uk_methods_agent_name,priority:2"`
	DisplayName  string         `gorm:"size:128;not null"`
	Description  string         `gorm:"size:500"`
	Transport    string         `gorm:"size:16;not null;default:http"`
	Visibility   string         `gorm:"size:16;not null;default:public"`
	InputSchema  datatypes.JSON `gorm:"type:json"`
	OutputSchema datatypes.JSON `gorm:"type:json"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName 返回 method 表名。
func (AgentMethod) TableName() string { return tableAgentMethods }

// ─── AgentSecret HMAC secret 加密存储 ───────────────────────────────────────

// AgentSecret 每个 agent 一条,保存当前与上一把 secret 的密文(AES-GCM)。
// Rotate:previous = current,current = new,previous_expires_at = now + 24h。
// 过期后清空 previous 字段(由 service 层懒清理)。
type AgentSecret struct {
	ID                      uint64     `gorm:"primaryKey;autoIncrement"`
	AgentID                 uint64     `gorm:"not null;uniqueIndex:uk_secrets_agent"`
	EncryptedSecret         []byte     `gorm:"type:varbinary(256);not null"`
	PreviousEncryptedSecret []byte     `gorm:"type:varbinary(256)"`
	PreviousExpiresAt       *time.Time
	LastRotatedAt           *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// TableName 返回 secret 表名。
func (AgentSecret) TableName() string { return tableAgentSecrets }

// ─── AgentPublish 发布关系 ──────────────────────────────────────────────────

// AgentPublish 描述 agent 与 org 的发布绑定。
// 同 agent + org 只允许一条 active(approved 或 pending);撤销后记录保留用于审计。
// Status 流转:
//   - pending → approved / rejected(审核)
//   - approved / pending → revoked(作者下架或联动)
type AgentPublish struct {
	ID                 uint64 `gorm:"primaryKey;autoIncrement"`
	AgentID            uint64 `gorm:"not null;index:idx_publishes_agent"`
	OrgID              uint64 `gorm:"not null;index:idx_publishes_org_status,priority:1"`
	SubmittedByUserID  uint64 `gorm:"not null"`
	Status             string `gorm:"size:16;not null;index:idx_publishes_org_status,priority:2"`
	ReviewedByUserID   *uint64
	ReviewedAt         *time.Time
	ReviewNote         string `gorm:"size:500"`
	RevokedAt          *time.Time
	RevokedReason      string `gorm:"size:32"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName 返回 publish 表名。
func (AgentPublish) TableName() string { return tableAgentPublishes }

// ─── AgentInvocation 审计主表(月分区) ─────────────────────────────────────

// AgentInvocation 记录单次调用的基础审计信息。
// **不走 AutoMigrate**:表由 partitions.go 手写 DDL 创建,带 RANGE(TO_DAYS(started_at)) 分区。
// 主键 (id, started_at) 是分区表的硬性要求。
type AgentInvocation struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement"`
	InvocationID      string    `gorm:"size:64;not null"`
	TraceID           string    `gorm:"size:64"`
	OrgID             uint64    `gorm:"not null"`
	CallerUserID      uint64    `gorm:"not null"`
	CallerRoleName    string    `gorm:"size:32"`
	AgentID           uint64    `gorm:"not null"`
	AgentOwnerUserID  uint64    `gorm:"not null"`
	MethodName        string    `gorm:"size:64;not null"`
	Transport         string    `gorm:"size:16;not null"`
	StartedAt         time.Time `gorm:"not null"`
	FinishedAt        *time.Time
	LatencyMs         *int
	Status            string `gorm:"size:32;not null"`
	ErrorCode         string `gorm:"size:32"`
	ErrorMessage      string `gorm:"size:500"`
	RequestSizeBytes  *int
	ResponseSizeBytes *int
	ClientIP          string    `gorm:"size:45"`
	UserAgent         string    `gorm:"size:255"`
	CreatedAt         time.Time
}

// TableName 返回 invocation 表名。
func (AgentInvocation) TableName() string { return tableAgentInvocations }

// ─── AgentInvocationPayload payload 表(月分区) ───────────────────────────

// AgentInvocationPayload 保存调用的 request/response body(截断至 32KB)。
// 仅当 org.record_full_payload=true 时异步写入。保留期 30 天。
type AgentInvocationPayload struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	InvocationID string    `gorm:"size:64;not null"`
	RequestBody  []byte    `gorm:"type:mediumblob"`
	ResponseBody []byte    `gorm:"type:mediumblob"`
	StartedAt    time.Time `gorm:"not null"`
	CreatedAt    time.Time
}

// TableName 返回 payload 表名。
func (AgentInvocationPayload) TableName() string { return tableAgentInvocationPayloads }
