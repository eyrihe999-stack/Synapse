// Package dto pm 模块 HTTP 请求 / 响应结构。
//
// 对齐 channel/dto 的做法:所有 gin.ShouldBindJSON 的目标类型都定义在本包,
// handler 只负责解构 + 调 service + 转响应。service 层的 struct(model.Project 等)
// 不直接暴露给前端 —— 字段变更不与 API 契约耦合。
package dto

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// ─── 请求 DTO:Project ──────────────────────────────────────────────────────

// CreateProjectRequest 创建 project 入参。
type CreateProjectRequest struct {
	OrgID       uint64 `json:"org_id" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// ─── 请求 DTO:Version ──────────────────────────────────────────────────────

// CreateVersionRequest 创建 version 入参。
//
// TargetDate 可选(YYYY-MM-DD 由前端解析后转成 *time.Time 发上来)。
type CreateVersionRequest struct {
	Name       string     `json:"name" binding:"required"`
	Status     string     `json:"status" binding:"required"`
	TargetDate *time.Time `json:"target_date,omitempty"`
}

// UpdateVersionRequest 更新 version 入参。各字段 nil 表示不改。
type UpdateVersionRequest struct {
	Status     *string    `json:"status,omitempty"`
	TargetDate *time.Time `json:"target_date,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

// ─── 响应 DTO:Project ──────────────────────────────────────────────────────

// ProjectResponse 单个 project 响应。
type ProjectResponse struct {
	ID          uint64     `json:"id"`
	OrgID       uint64     `json:"org_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	CreatedBy   uint64     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

// ToProjectResponse 单个转换。
func ToProjectResponse(p *model.Project) ProjectResponse {
	return ProjectResponse{
		ID: p.ID, OrgID: p.OrgID, Name: p.Name, Description: p.Description,
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		ArchivedAt: p.ArchivedAt,
	}
}

// ToProjectListResponse 列表转换。
func ToProjectListResponse(ps []model.Project) []ProjectResponse {
	out := make([]ProjectResponse, 0, len(ps))
	for i := range ps {
		out = append(out, ToProjectResponse(&ps[i]))
	}
	return out
}

// ─── 响应 DTO:Version ──────────────────────────────────────────────────────

// VersionResponse 单个 version 响应。新加字段 ReleasedAt / IsSystem 反映新 schema。
type VersionResponse struct {
	ID         uint64     `json:"id"`
	ProjectID  uint64     `json:"project_id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	TargetDate *time.Time `json:"target_date,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	IsSystem   bool       `json:"is_system,omitempty"`
	CreatedBy  uint64     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// ToVersionResponse 单个转换。
func ToVersionResponse(v *model.Version) VersionResponse {
	return VersionResponse{
		ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Status: v.Status,
		TargetDate: v.TargetDate, ReleasedAt: v.ReleasedAt,
		IsSystem: v.IsSystem, CreatedBy: v.CreatedBy,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

// ToVersionListResponse 列表转换。
func ToVersionListResponse(vs []model.Version) []VersionResponse {
	out := make([]VersionResponse, 0, len(vs))
	for i := range vs {
		out = append(out, ToVersionResponse(&vs[i]))
	}
	return out
}

// ─── 请求 / 响应 DTO:Initiative ────────────────────────────────────────────

type CreateInitiativeRequest struct {
	Name          string `json:"name" binding:"required"`
	Description   string `json:"description,omitempty"`
	TargetOutcome string `json:"target_outcome,omitempty"`
}

type UpdateInitiativeRequest struct {
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	TargetOutcome *string `json:"target_outcome,omitempty"`
	Status        *string `json:"status,omitempty"`
}

type InitiativeResponse struct {
	ID            uint64     `json:"id"`
	ProjectID     uint64     `json:"project_id"`
	Name          string     `json:"name"`
	Description   string     `json:"description,omitempty"`
	TargetOutcome string     `json:"target_outcome,omitempty"`
	Status        string     `json:"status"`
	IsSystem      bool       `json:"is_system,omitempty"`
	CreatedBy     uint64     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
}

func ToInitiativeResponse(i *model.Initiative) InitiativeResponse {
	return InitiativeResponse{
		ID: i.ID, ProjectID: i.ProjectID, Name: i.Name,
		Description: i.Description, TargetOutcome: i.TargetOutcome,
		Status: i.Status, IsSystem: i.IsSystem, CreatedBy: i.CreatedBy,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ArchivedAt: i.ArchivedAt,
	}
}

func ToInitiativeListResponse(is []model.Initiative) []InitiativeResponse {
	out := make([]InitiativeResponse, 0, len(is))
	for i := range is {
		out = append(out, ToInitiativeResponse(&is[i]))
	}
	return out
}

// ─── 请求 / 响应 DTO:Workstream ───────────────────────────────────────────

type CreateWorkstreamRequest struct {
	VersionID   *uint64 `json:"version_id,omitempty"` // nil = backlog
	Name        string  `json:"name" binding:"required"`
	Description string  `json:"description,omitempty"`
}

type UpdateWorkstreamRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
	// VersionID:nil 不动;指针指向 0 = 移到 backlog;指针指向非零 = 改挂
	VersionID *uint64 `json:"version_id,omitempty"`
}

type WorkstreamResponse struct {
	ID           uint64     `json:"id"`
	InitiativeID uint64     `json:"initiative_id"`
	VersionID    *uint64    `json:"version_id,omitempty"`
	ProjectID    uint64     `json:"project_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Status       string     `json:"status"`
	ChannelID    *uint64    `json:"channel_id,omitempty"`
	CreatedBy    uint64     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
}

func ToWorkstreamResponse(w *model.Workstream) WorkstreamResponse {
	return WorkstreamResponse{
		ID: w.ID, InitiativeID: w.InitiativeID, VersionID: w.VersionID,
		ProjectID: w.ProjectID, Name: w.Name, Description: w.Description,
		Status: w.Status, ChannelID: w.ChannelID,
		CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		ArchivedAt: w.ArchivedAt,
	}
}

func ToWorkstreamListResponse(ws []model.Workstream) []WorkstreamResponse {
	out := make([]WorkstreamResponse, 0, len(ws))
	for i := range ws {
		out = append(out, ToWorkstreamResponse(&ws[i]))
	}
	return out
}

// ─── 请求 / 响应 DTO:ProjectKBRef ─────────────────────────────────────────

// AttachProjectKBRefRequest 二选一:source 或 doc。另一个传 0 / 省略。
type AttachProjectKBRefRequest struct {
	KBSourceID   uint64 `json:"kb_source_id,omitempty"`
	KBDocumentID uint64 `json:"kb_document_id,omitempty"`
}

type ProjectKBRefResponse struct {
	ID           uint64    `json:"id"`
	ProjectID    uint64    `json:"project_id"`
	KBSourceID   uint64    `json:"kb_source_id,omitempty"`
	KBDocumentID uint64    `json:"kb_document_id,omitempty"`
	AttachedBy   uint64    `json:"attached_by"`
	AttachedAt   time.Time `json:"attached_at"`
}

func ToProjectKBRefResponse(r *model.ProjectKBRef) ProjectKBRefResponse {
	return ProjectKBRefResponse{
		ID: r.ID, ProjectID: r.ProjectID,
		KBSourceID: r.KBSourceID, KBDocumentID: r.KBDocumentID,
		AttachedBy: r.AttachedBy, AttachedAt: r.AttachedAt,
	}
}

func ToProjectKBRefListResponse(rs []model.ProjectKBRef) []ProjectKBRefResponse {
	out := make([]ProjectKBRefResponse, 0, len(rs))
	for i := range rs {
		out = append(out, ToProjectKBRefResponse(&rs[i]))
	}
	return out
}
