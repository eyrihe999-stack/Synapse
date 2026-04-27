// Package dto task 模块 HTTP 请求 / 响应 schema。
package dto

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/task/model"
)

// ─── 请求 DTO ────────────────────────────────────────────────────────────────

// CreateTaskRequest 创建 task 的请求体。
//
// AssigneePrincipalID 可选(0 = 未指派,状态进 open 等人 claim);
// ReviewerPrincipalIDs 必须包含全部可 approve 的 principal;RequiredApprovals
// 必须 ≤ len(ReviewerPrincipalIDs)。
type CreateTaskRequest struct {
	ChannelID            uint64   `json:"channel_id" binding:"required"`
	Title                string   `json:"title" binding:"required"`
	Description          string   `json:"description"`
	OutputSpecKind       string   `json:"output_spec_kind" binding:"required"` // markdown / text
	IsLightweight        bool     `json:"is_lightweight,omitempty"`            // 轻量任务,submit 不要文件
	AssigneePrincipalID  uint64   `json:"assignee_principal_id,omitempty"`
	ReviewerPrincipalIDs []uint64 `json:"reviewer_principal_ids"`
	RequiredApprovals    int      `json:"required_approvals"` // 0 自动 1
}

// SubmitTaskRequest 提交产物的请求体。
//
// 普通任务:Content 必填,InlineSummary 可选。
// 轻量任务(task.is_lightweight=true):Content 必须空,InlineSummary 必填。
// Content 直接 body 字节传输(不做 base64 / multipart,简化)。≤ 1MB。
type SubmitTaskRequest struct {
	ContentKind   string `json:"content_kind"`
	Content       string `json:"content"`
	InlineSummary string `json:"inline_summary"`
}

// ReviewTaskRequest 审批请求体。
type ReviewTaskRequest struct {
	SubmissionID uint64 `json:"submission_id" binding:"required"`
	Decision     string `json:"decision" binding:"required"` // approved / request_changes / rejected
	Comment      string `json:"comment"`
}

// UpdateAssigneeRequest 换执行人请求。assignee_principal_id=0 表示清空。
type UpdateAssigneeRequest struct {
	AssigneePrincipalID uint64 `json:"assignee_principal_id"`
}

// UpdateReviewersRequest 换审批人列表请求。
// required_approvals 会被 service 层 clamp 到 [0, len(reviewer_principal_ids)]。
type UpdateReviewersRequest struct {
	ReviewerPrincipalIDs []uint64 `json:"reviewer_principal_ids"`
	RequiredApprovals    int      `json:"required_approvals"`
}

// UpdateReviewersResponse 换审批人响应,附最新 reviewer 列表给前端确认。
type UpdateReviewersResponse struct {
	Task      TaskResponse `json:"task"`
	Reviewers []uint64     `json:"reviewers"`
}

// ─── 响应 DTO ────────────────────────────────────────────────────────────────

// TaskResponse 单条 task 的列表响应。
type TaskResponse struct {
	ID                    uint64     `json:"id"`
	OrgID                 uint64     `json:"org_id"`
	ChannelID             uint64     `json:"channel_id"`
	Title                 string     `json:"title"`
	Description           string     `json:"description,omitempty"`
	CreatedByPrincipalID  uint64     `json:"created_by_principal_id"`
	// CreatedViaPrincipalID 代派 agent 的 principal_id;0 表示手动创建。
	// 前端用于"由 X 通过 Y(agent) 代派"的展示。
	CreatedViaPrincipalID uint64     `json:"created_via_principal_id,omitempty"`
	AssigneePrincipalID   uint64     `json:"assignee_principal_id,omitempty"`
	Status                string     `json:"status"`
	OutputSpecKind        string     `json:"output_spec_kind"`
	IsLightweight         bool       `json:"is_lightweight,omitempty"`
	RequiredApprovals     int        `json:"required_approvals"`
	DueAt                 *time.Time `json:"due_at,omitempty"`
	SubmittedAt           *time.Time `json:"submitted_at,omitempty"`
	ClosedAt              *time.Time `json:"closed_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

func ToTaskResponse(t *model.Task) TaskResponse {
	return TaskResponse{
		ID: t.ID, OrgID: t.OrgID, ChannelID: t.ChannelID,
		Title: t.Title, Description: t.Description,
		CreatedByPrincipalID:  t.CreatedByPrincipalID,
		CreatedViaPrincipalID: t.CreatedViaPrincipalID,
		AssigneePrincipalID:   t.AssigneePrincipalID,
		Status:                t.Status,
		OutputSpecKind:        t.OutputSpecKind,
		IsLightweight:         t.IsLightweight,
		RequiredApprovals:     t.RequiredApprovals,
		DueAt:                 t.DueAt,
		SubmittedAt:           t.SubmittedAt,
		ClosedAt:              t.ClosedAt,
		CreatedAt:             t.CreatedAt,
		UpdatedAt:             t.UpdatedAt,
	}
}

func ToTaskListResponse(ts []model.Task) []TaskResponse {
	out := make([]TaskResponse, 0, len(ts))
	for i := range ts {
		out = append(out, ToTaskResponse(&ts[i]))
	}
	return out
}

// SubmissionResponse
type SubmissionResponse struct {
	ID                   uint64    `json:"id"`
	TaskID               uint64    `json:"task_id"`
	SubmitterPrincipalID uint64    `json:"submitter_principal_id"`
	ContentKind          string    `json:"content_kind"`
	OSSKey               string    `json:"oss_key"`
	ByteSize             int64     `json:"byte_size"`
	InlineSummary        string    `json:"inline_summary,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

func ToSubmissionResponse(s *model.TaskSubmission) SubmissionResponse {
	return SubmissionResponse{
		ID: s.ID, TaskID: s.TaskID,
		SubmitterPrincipalID: s.SubmitterPrincipalID,
		ContentKind:          s.ContentKind,
		OSSKey:               s.OSSKey,
		ByteSize:             s.ByteSize,
		InlineSummary:        s.InlineSummary,
		CreatedAt:            s.CreatedAt,
	}
}

// ReviewResponse
type ReviewResponse struct {
	ID                  uint64    `json:"id"`
	TaskID              uint64    `json:"task_id"`
	SubmissionID        uint64    `json:"submission_id"`
	ReviewerPrincipalID uint64    `json:"reviewer_principal_id"`
	Decision            string    `json:"decision"`
	Comment             string    `json:"comment,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

func ToReviewResponse(r *model.TaskReview) ReviewResponse {
	return ReviewResponse{
		ID: r.ID, TaskID: r.TaskID, SubmissionID: r.SubmissionID,
		ReviewerPrincipalID: r.ReviewerPrincipalID,
		Decision:            r.Decision, Comment: r.Comment, CreatedAt: r.CreatedAt,
	}
}

// TaskDetailResponse Get 的完整视图响应。
type TaskDetailResponse struct {
	Task        TaskResponse         `json:"task"`
	Reviewers   []uint64             `json:"reviewers"`
	Submissions []SubmissionResponse `json:"submissions"`
	Reviews     []ReviewResponse     `json:"reviews"`
}

// CreateTaskResponse 创建 task 后返 task 本身 + reviewers 列表。
type CreateTaskResponse struct {
	Task      TaskResponse `json:"task"`
	Reviewers []uint64     `json:"reviewers"`
}

// SubmitTaskResponse 提交后返 task 新状态 + submission。
type SubmitTaskResponse struct {
	Task       TaskResponse       `json:"task"`
	Submission SubmissionResponse `json:"submission"`
}

// ReviewTaskResponse 审批后返 task 新状态 + review 行。
type ReviewTaskResponse struct {
	Task   TaskResponse   `json:"task"`
	Review ReviewResponse `json:"review"`
}
