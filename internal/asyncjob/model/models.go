// Package model asyncjob 模块的数据模型。
//
// async_jobs 表承载"可能跑很久 + 前端要看进度"的任务。当前使用方:
//   - integration.sync.feishu —— 飞书一键导入
//
// 未来规划:
//   - integration.sync.google / .slack / ...(按 kind 路由到不同 Runner)
//   - document.batch_reembed(整个 org re-embed)
//   - document.import.url_list(从 URL 列表批量导入)
//
// 扩展约束:
//   - 所有 provider/kind 特定数据进 Payload / Result(jsonb),不动 schema。
//   - 并发控制:同一 (user_id, kind) 同时只允许一个 queued/running,由 service 层用行锁保证。
package model

import (
	"time"

	"gorm.io/datatypes"
)

// Kind 任务类型字面量。加新类型时只需在 runner 层注册对应 Runner,
// 表结构不动。字面量格式: "<domain>.<action>.<provider?>"
const (
	KindFeishuSync = "integration.sync.feishu"
	KindGitLabSync = "integration.sync.gitlab"
)

// Status 任务状态机。
//
//	queued → running → (succeeded | failed | canceled)
//
// 只允许单向推进;重跑请新建 job。canceled 目前未暴露给用户(见设计会话记录),
// 但枚举保留,避免将来加"取消"时做 schema 变更。
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// IsTerminal 终态判断。前端轮询到终态即停止。
func (s Status) IsTerminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCanceled
}

// Job 通用异步任务记录。
//
// 索引:
//   - (user_id, kind, status) 查"当前用户是否有同类任务在跑"(防重复提交)
//   - (org_id, kind, created_at desc) 组织维度的历史列表(将来做 UI 时用)
//   - (status, heartbeat_at) 启动时扫 stale running 做僵尸回收
type Job struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID  uint64 `gorm:"not null;index:idx_async_jobs_org_kind_created,priority:1"`
	UserID uint64 `gorm:"not null;index:idx_async_jobs_user_kind_status,priority:1"`

	// Kind 任务类型,见上方常量。size 64 留足 "<domain>.<action>.<provider>" 拼接空间。
	Kind string `gorm:"size:64;not null;index:idx_async_jobs_user_kind_status,priority:2;index:idx_async_jobs_org_kind_created,priority:2"`

	Status Status `gorm:"size:16;not null;index:idx_async_jobs_user_kind_status,priority:3;index:idx_async_jobs_status_heartbeat,priority:1"`

	// 进度三元组。ProgressTotal=0 表示"总量未知"(如飞书 Sync 前不知道要拉多少,
	// 扫完 Sync 后再 SetTotal);前端显示 "已完成 / ?"。
	ProgressTotal  int `gorm:"not null;default:0"`
	ProgressDone   int `gorm:"not null;default:0"`
	ProgressFailed int `gorm:"not null;default:0"`

	// Payload 输入参数 —— 每个 kind 自己定义 schema。飞书 sync 暂时无参数,留空。
	// 扩展:将来前端想选"只同步某几个文件"就序列化 {file_refs: [...]} 进来。
	Payload datatypes.JSON `gorm:"type:json"`

	// Result 成功/失败的摘要 —— 每个 kind 自己定义。飞书 sync: {synced_count: N, failed_refs: [...]}
	Result datatypes.JSON `gorm:"type:json"`

	// Error 失败根因字符串。仅 Status=failed 时非空。stack trace 不入库(走 logger)。
	Error string `gorm:"type:text"`

	// Heartbeat Runner 跑时每 ~10s 更新一次。服务重启时扫 "running + heartbeat 陈旧"
	// 的行标记 failed —— 没心跳 = 进程崩了。
	HeartbeatAt *time.Time `gorm:"index:idx_async_jobs_status_heartbeat,priority:2"`

	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName 固定表名。
func (Job) TableName() string { return "async_jobs" }
