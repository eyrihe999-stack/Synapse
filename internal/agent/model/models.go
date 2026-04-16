// models.go agent 模块数据模型定义。
//
// 4 张表:
//   - Agent:agent 主表
//   - AgentSession:对话 session
//   - AgentMessage:对话消息
//   - AgentPublish:agent → org 的发布关系
package model

import (
	"time"

	"gorm.io/datatypes"
)

// ─── 表名常量 ────────────────────────────────────────────────────────────────

const (
	tableAgents         = "agents"
	tableAgentSessions  = "agent_sessions"
	tableAgentMessages  = "agent_messages"
	tableAgentPublishes = "agent_publishes"
)

// ─── 模型级常量 ──────────────────────────────────────────────────────────────

// AgentStatusActive 表示 agent 处于正常可用状态。
const AgentStatusActive = "active"

// AgentStatusBanned 表示 agent 已被封禁。
const AgentStatusBanned = "banned"

// PublishStatusPending 表示发布请求待审核。
const PublishStatusPending = "pending"

// PublishStatusApproved 表示发布请求已通过审核。
const PublishStatusApproved = "approved"

// PublishStatusRejected 表示发布请求已被拒绝。
const PublishStatusRejected = "rejected"

// PublishStatusRevoked 表示发布已被撤销。
const PublishStatusRevoked = "revoked"

// ─── Agent 主表 ──────────────────────────────────────────────────────────────

// Agent 表示一个用户注册的 chat agent。
// OwnerUserID 唯一归属一个用户,Slug 在作者内唯一。
// EndpointURL 允许 HTTP 和 HTTPS。
// ContextMode 决定上下文管理方式:stateless 由 Synapse 管,stateful 由 Agent 管。
type Agent struct {
	ID                   uint64         `gorm:"primaryKey;autoIncrement"`
	OwnerUserID          uint64         `gorm:"not null;index:idx_agents_owner;uniqueIndex:uk_agents_owner_slug,priority:1"`
	Slug                 string         `gorm:"size:64;not null;uniqueIndex:uk_agents_owner_slug,priority:2"`
	DisplayName          string         `gorm:"size:128;not null"`
	Description          string         `gorm:"size:1000"`
	AgentType            string         `gorm:"size:16;not null;default:chat"`
	EndpointURL          string         `gorm:"size:512;not null"`
	ContextMode          string         `gorm:"size:16;not null;default:stateless"`
	MaxContextRounds     int            `gorm:"not null;default:20"`
	AuthTokenEncrypted   []byte         `gorm:"type:varbinary(512)"`
	TimeoutSeconds       int            `gorm:"not null;default:30"`
	IconURL              string         `gorm:"size:512"`
	Tags                 datatypes.JSON `gorm:"type:json"`
	Status               string         `gorm:"size:16;not null;default:active;index:idx_agents_status"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// TableName 返回 Agent 模型对应的数据库表名。
func (Agent) TableName() string { return tableAgents }

// ─── AgentSession 对话 session ───────────────────────────────────────────────

// AgentSession 代表一个用户与 agent 的对话 session。
// SessionID 是 UUID,用于 X-Synapse-Session-ID header 传递给 stateful agent。
// ContextMode 是 session 创建时 agent 的 context_mode 快照。
type AgentSession struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	SessionID   string `gorm:"size:64;not null;uniqueIndex:uk_sessions_sid"`
	OrgID       uint64 `gorm:"not null;index:idx_sessions_org_user_agent,priority:1"`
	UserID      uint64 `gorm:"not null;index:idx_sessions_org_user_agent,priority:2;index:idx_sessions_user"`
	AgentID     uint64 `gorm:"not null;index:idx_sessions_org_user_agent,priority:3"`
	Title       string `gorm:"size:256"`
	ContextMode string `gorm:"size:16;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回 AgentSession 模型对应的数据库表名。
func (AgentSession) TableName() string { return tableAgentSessions }

// ─── AgentMessage 对话消息 ───────────────────────────────────────────────────

// AgentMessage 存储 session 内的单条消息(user / assistant / system)。
type AgentMessage struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	SessionID string `gorm:"size:64;not null;index:idx_messages_session_created,priority:1"`
	Role      string `gorm:"size:16;not null"`
	Content   string `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"index:idx_messages_session_created,priority:2"`
}

// TableName 返回 AgentMessage 模型对应的数据库表名。
func (AgentMessage) TableName() string { return tableAgentMessages }

// ─── AgentPublish 发布关系 ──────────────────────────────────────────────────

// AgentPublish 描述 agent 与 org 的发布绑定。
type AgentPublish struct {
	ID                uint64     `gorm:"primaryKey;autoIncrement"`
	AgentID           uint64     `gorm:"not null;index:idx_publishes_agent"`
	OrgID             uint64     `gorm:"not null;index:idx_publishes_org_status,priority:1"`
	SubmittedByUserID uint64     `gorm:"not null"`
	Status            string     `gorm:"size:16;not null;index:idx_publishes_org_status,priority:2"`
	ReviewedByUserID  *uint64
	ReviewedAt        *time.Time
	ReviewNote        string     `gorm:"size:500"`
	RevokedAt         *time.Time
	RevokedReason     string     `gorm:"size:32"`
	CreatedAt         time.Time
	UpdatedAt         time.Time

	Agent *Agent `gorm:"foreignKey:AgentID"`
}

// TableName 返回 AgentPublish 模型对应的数据库表名。
func (AgentPublish) TableName() string { return tableAgentPublishes }
