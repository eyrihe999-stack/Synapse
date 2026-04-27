package model

import "time"

// LLMUsage 单次 LLM 调用的 token 消耗 + 估算成本。
//
// 用途:
//  1. 每 org 每日预算 rate limit:runtime 每次响应前 SELECT SUM(cost_usd) WHERE
//     operating_org_id=? AND created_at >= today_start;超限则回"预算用完"。
//  2. 事后审计 / 账单。
//
// 字段语义:
//
//	OperatingOrgID    承担成本的 org —— rate limit 的维度
//	ActorPrincipalID  调用 LLM 的 agent principal(顶级 agent 唯一来源;未来专项 agent 也用)
//	Model             落库用模型标识,形如 "gpt-5.4@azure",和 llm.Chat.Model() 一致
//	PromptTokens      输入 token 数(provider 返回的 usage.prompt_tokens)
//	CompletionTokens  输出 token 数
//	CostUSD           估算成本(美元)。由 llm.EstimateCostUSD 按模型价表算出
//	ChannelID         调用所在 channel;无对应 channel 时填 0
//
// 索引 (operating_org_id, created_at) 覆盖 rate limit 每次查当日 SUM 的最频繁查询。
type LLMUsage struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	OperatingOrgID   uint64    `gorm:"not null;index:idx_usage_org_date,priority:1"`
	ActorPrincipalID uint64    `gorm:"not null"`
	Model            string    `gorm:"size:64;not null"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	// CostUSD 使用 float64 足够:业务场景是"比较日汇总和预算上限"(美元级),不做精确账务。
	// MySQL 侧存为 DOUBLE,和 Go float64 对齐。
	CostUSD   float64   `gorm:"not null;default:0"`
	ChannelID uint64    `gorm:"not null;default:0"`
	CreatedAt time.Time `gorm:"not null;index:idx_usage_org_date,priority:2"`
}

func (LLMUsage) TableName() string { return "llm_usage" }
