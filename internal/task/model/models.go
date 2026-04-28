// Package model task 模块数据模型 —— tasks / task_reviewers / task_submissions /
// task_reviews 四张表。
//
// 对应设计见 docs/collaboration-design.md §3.5。所有身份列统一 FK principals.id
// (user 和 agent 不分叉)。
package model

import "time"

// Task channel 内的一个结构化任务。
//
// 状态机:draft / open / in_progress / submitted / approved / revision_requested /
// rejected / cancelled —— 见 task/const.go 里的 Status* 常量。
//
// 产物格式:第一版 output_spec_kind ∈ {markdown, text}。output_spec JSON 列留作未来
// 细粒度约束(max_size / frontmatter 等),当前为空 JSON。
//
// 索引:
//   - (channel_id, status):列"这个 channel 所有 open / in_progress 任务"
//   - (assignee_principal_id, status):列"我的待办"
type Task struct {
	ID                     uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	OrgID                  uint64    `gorm:"not null;index:idx_tasks_org" json:"org_id"`
	ChannelID              uint64    `gorm:"not null;index:idx_tasks_channel_status,priority:1" json:"channel_id"`
	// WorkstreamID PR-A 引入:task 主属 workstream 的反向引用;NULL = ad-hoc(临时
	// 直接在 channel 创建,不挂工作切片)。在 pm.workstreams 表中查得到归属。
	WorkstreamID           *uint64   `gorm:"column:workstream_id;index:idx_tasks_workstream" json:"workstream_id,omitempty"`
	Title                  string    `gorm:"size:256;not null" json:"title"`
	Description            string    `gorm:"type:text" json:"description,omitempty"`
	// CreatedByPrincipalID 任务发起人(意图所有者)。
	// 手动创建 = 操作人本身;@Synapse 代派 = 那条 @ 消息的作者。
	CreatedByPrincipalID   uint64    `gorm:"column:created_by_principal_id;not null" json:"created_by_principal_id"`
	// CreatedViaPrincipalID 实际执行创建的代理 principal_id(如 Synapse agent);
	// 0 表示发起人直接创建,非 0 表示代派。权限链用 CreatedBy;审计链用此字段。
	CreatedViaPrincipalID  uint64    `gorm:"column:created_via_principal_id;not null" json:"created_via_principal_id,omitempty"`
	AssigneePrincipalID    uint64    `gorm:"column:assignee_principal_id;not null;default:0;index:idx_tasks_assignee_status,priority:1" json:"assignee_principal_id,omitempty"` // 0 = 未指派
	Status                 string    `gorm:"size:32;not null;index:idx_tasks_channel_status,priority:2;index:idx_tasks_assignee_status,priority:2" json:"status"`
	OutputSpecKind         string    `gorm:"column:output_spec_kind;size:32;not null" json:"output_spec_kind"`
	OutputSpec             []byte    `gorm:"column:output_spec;type:json" json:"output_spec,omitempty"` // nullable JSON,细粒度约束
	// IsLightweight true 表示这是个"轻量任务",submit 时不要求文件 ——
	// content 可空,oss 不上传,只走 inline_summary 描述"做了什么"(必填)。
	// 适合调研、口头汇报、确认 PR review 等不需要交付物的场景。
	// 默认 false:保持现有"必须有 markdown/text 文件"语义。
	IsLightweight          bool      `gorm:"column:is_lightweight;not null;default:false" json:"is_lightweight,omitempty"`
	// RequiredApprovals 所需通过审批人数。**不设 DB default** —— service 层是唯一
	// 真相源(空 reviewer=0 / 非空=clamp 到 [1,len])。之前 default:1 会让 GORM 把
	// service 算出的 0 当零值省略,MySQL 填回 1,造成"无 reviewer 却要 1 人批"的死状态。
	RequiredApprovals      int       `gorm:"column:required_approvals;not null" json:"required_approvals"`
	DueAt                  *time.Time `json:"due_at,omitempty"`
	SubmittedAt            *time.Time `json:"submitted_at,omitempty"`
	ClosedAt               *time.Time `json:"closed_at,omitempty"`
	CreatedAt              time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time `gorm:"not null" json:"updated_at"`
}

// TableName 固定表名。
func (Task) TableName() string { return "tasks" }

// TaskReviewer 任务的审批白名单。
//
// "谁能 approve" 白名单。非白名单内的 reviewer approve 不计数(service 层拦)。
// 复合 PK (task_id, principal_id);级联删 task。
type TaskReviewer struct {
	TaskID      uint64 `gorm:"primaryKey" json:"task_id"`
	PrincipalID uint64 `gorm:"primaryKey;index:idx_task_reviewers_principal" json:"principal_id"`
}

// TableName 固定表名。
func (TaskReviewer) TableName() string { return "task_reviewers" }

// TaskSubmission 任务的产物提交。
//
// 一个 task 可能有多次提交 —— revision_requested 后重做即新一行,旧行保留作审计。
// 产物走 OSS,DB 只存 oss_key / byte_size / 摘要。
type TaskSubmission struct {
	ID                     uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskID                 uint64    `gorm:"not null;index:idx_task_submissions_task,priority:1" json:"task_id"`
	SubmitterPrincipalID   uint64    `gorm:"column:submitter_principal_id;not null" json:"submitter_principal_id"`
	ContentKind            string    `gorm:"column:content_kind;size:32;not null" json:"content_kind"`
	// OSSKey 产物在 OSS 的 key。is_lightweight=true 的轻量任务允许为空(不上 OSS),
	// 这种 submission 只看 inline_summary。NOT NULL 保留(空串占位),避免老查询路径
	// 出现 NULL 处理分歧;service / 前端按 OSSKey == "" 判断"无文件"。
	OSSKey                 string    `gorm:"column:oss_key;size:512;not null;default:''" json:"oss_key"`
	ByteSize               int64     `gorm:"column:byte_size;not null;default:0" json:"byte_size"`
	InlineSummary          string    `gorm:"column:inline_summary;size:512" json:"inline_summary,omitempty"`
	CreatedAt              time.Time `gorm:"not null;index:idx_task_submissions_task,priority:2,sort:desc" json:"created_at"`
}

// TableName 固定表名。
func (TaskSubmission) TableName() string { return "task_submissions" }

// TaskReview reviewer 对某次 submission 的决策。
//
// UNIQUE (submission_id, reviewer_principal_id):同一 reviewer 对同一次 submission
// 只允许一个决策(想改决策要走另外的机制或重做 submission)。
type TaskReview struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskID               uint64    `gorm:"not null;index:idx_task_reviews_task" json:"task_id"`
	SubmissionID         uint64    `gorm:"column:submission_id;not null;uniqueIndex:uk_task_reviews_submission_reviewer,priority:1" json:"submission_id"`
	ReviewerPrincipalID  uint64    `gorm:"column:reviewer_principal_id;not null;uniqueIndex:uk_task_reviews_submission_reviewer,priority:2" json:"reviewer_principal_id"`
	Decision             string    `gorm:"size:32;not null" json:"decision"`
	Comment              string    `gorm:"type:text" json:"comment,omitempty"`
	CreatedAt            time.Time `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (TaskReview) TableName() string { return "task_reviews" }
