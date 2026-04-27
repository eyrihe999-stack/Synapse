// Package model PR #6' 顶级系统 agent runtime 的持久化模型。
//
// 包含两张表:
//   - audit_events:所有系统 agent 的动作审计(响应消息、创建 task、跳过、出错)
//   - llm_usage:LLM token 消耗 + 成本(按 org 计,rate limit 依据)
//
// 表结构详见 docs/collaboration-roadmap.md PR #6' 的"新增表"节。
package model

import (
	"time"

	"gorm.io/datatypes"
)

// AuditEventAction audit_events.action 常量。任何新增的 action 都在这里登记 ——
// grep 得到便于长期维护。
type AuditEventAction string

const (
	// ActionPostMessage 顶级 agent 在 channel 回复了一条消息。target_id = message_id。
	ActionPostMessage AuditEventAction = "post_message"
	// ActionCreateTask 顶级 agent 创建了一个 task。target_id = task_id。
	ActionCreateTask AuditEventAction = "create_task"
	// ActionLLMCall 调用了一次 LLM(无论是否产生工具调用)。detail 含 usage。
	ActionLLMCall AuditEventAction = "llm.call"
	// ActionSkipNotMentioned 事件未 @ 本 agent,跳过。
	ActionSkipNotMentioned AuditEventAction = "skip.not_mentioned"
	// ActionSkipBudget 超过 org 日预算,跳过(已在 channel 回"预算用完")。
	ActionSkipBudget AuditEventAction = "skip.budget"
	// ActionToolOK LLM 请求的某次 tool 成功执行。detail 含 tool / round / result_size / summary。
	// 和 ActionPostMessage / ActionCreateTask 独立并存 —— 这两者是"某类 tool 成功"的专属语义
	// (带 target_id 语义明确),ActionToolOK 补齐其他 tool 的可观测性(list_* 系列)。
	ActionToolOK AuditEventAction = "tool.ok"
	// ActionToolError LLM 请求的某次 tool 调用失败(已在 channel 回错误消息)。
	ActionToolError AuditEventAction = "tool.error"
	// ActionLLMError LLM 请求失败(已在 channel 回"暂时回不上来")。
	ActionLLMError AuditEventAction = "llm.error"
)

// AuditEvent 系统 agent 动作审计日志。
//
// 字段语义:
//
//	ActorPrincipalID  发起动作的 principal(顶级 agent 的 principal_id)
//	OperatingOrgID    动作作用的 org —— 跨 org 隔离的溯源锚点
//	ChannelID         动作所在 channel;非 channel 维度动作填 0
//	Action            见 AuditEventAction 常量
//	TargetID          动作操作的对象 id(message_id / task_id 等),无则 0
//	Detail            结构化细节 JSON,nullable;例如 llm.call 塞 tokens + cost
//
// 索引设计:
//   - (operating_org_id, channel_id, created_at) 覆盖"查某 org 某 channel 某时段审计"
//   - (actor_principal_id, created_at)          覆盖"查某 agent 的动作轨迹"
type AuditEvent struct {
	ID               uint64           `gorm:"primaryKey;autoIncrement"`
	ActorPrincipalID uint64           `gorm:"not null;index:idx_audit_actor,priority:1"`
	OperatingOrgID   uint64           `gorm:"not null;index:idx_audit_org_ch,priority:1"`
	ChannelID        uint64           `gorm:"not null;default:0;index:idx_audit_org_ch,priority:2"`
	Action           string           `gorm:"size:64;not null"`
	TargetID         uint64           `gorm:"not null;default:0"`
	Detail           datatypes.JSON   `gorm:"type:json"`
	CreatedAt        time.Time        `gorm:"not null;index:idx_audit_org_ch,priority:3;index:idx_audit_actor,priority:2"`
}

func (AuditEvent) TableName() string { return "audit_events" }
