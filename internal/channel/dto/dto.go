// Package dto channel 模块 HTTP 请求 / 响应结构。
//
// 对齐 internal/organization/dto 的做法:所有 gin.ShouldBindJSON 的目标类型都
// 定义在本包,handler 只负责解构 + 调 service + 转响应。service 层的 struct
// (model.Project 等)不直接暴露给前端 —— 字段变更不与 API 契约耦合。
package dto

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// ─── 请求 DTO ────────────────────────────────────────────────────────────────

type CreateProjectRequest struct {
	OrgID       uint64 `json:"org_id" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type CreateVersionRequest struct {
	Name   string `json:"name" binding:"required"`
	Status string `json:"status" binding:"required"`
}

type CreateChannelRequest struct {
	ProjectID uint64 `json:"project_id" binding:"required"`
	Name      string `json:"name" binding:"required"`
	Purpose   string `json:"purpose"`
}

type AddMemberRequest struct {
	PrincipalID uint64 `json:"principal_id" binding:"required"`
	Role        string `json:"role" binding:"required"`
}

type UpdateMemberRoleRequest struct {
	Role string `json:"role" binding:"required"`
}

// PostMessageRequest 发送 channel 消息的请求体。
//
// Mentions 可选;由前端 / MCP client 按 principal_id 显式传入。后端不做 @
// 文本解析(body 里的 @xxx 是人类可读形式,服务端不试图解析 / 校验 @xxx 文字
// 和 Mentions 一一对应,只信任 Mentions 数组)。
//
// ReplyToMessageID 可选:本条消息作为对指定消息的"回复"(前端渲染引用卡片)。
// 校验:目标消息必须存在且属于同 channel。
type PostMessageRequest struct {
	Body             string   `json:"body" binding:"required"`
	Mentions         []uint64 `json:"mentions,omitempty"`
	ReplyToMessageID *uint64  `json:"reply_to_message_id,omitempty"`
}

// ─── 响应 DTO ────────────────────────────────────────────────────────────────

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

func ToProjectResponse(p *model.Project) ProjectResponse {
	return ProjectResponse{
		ID: p.ID, OrgID: p.OrgID, Name: p.Name, Description: p.Description,
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		ArchivedAt: p.ArchivedAt,
	}
}

func ToProjectListResponse(ps []model.Project) []ProjectResponse {
	out := make([]ProjectResponse, 0, len(ps))
	for i := range ps {
		out = append(out, ToProjectResponse(&ps[i]))
	}
	return out
}

type VersionResponse struct {
	ID         uint64     `json:"id"`
	ProjectID  uint64     `json:"project_id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	TargetDate *time.Time `json:"target_date,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func ToVersionResponse(v *model.Version) VersionResponse {
	return VersionResponse{
		ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Status: v.Status,
		TargetDate: v.TargetDate, CreatedAt: v.CreatedAt,
	}
}

func ToVersionListResponse(vs []model.Version) []VersionResponse {
	out := make([]VersionResponse, 0, len(vs))
	for i := range vs {
		out = append(out, ToVersionResponse(&vs[i]))
	}
	return out
}

type ChannelResponse struct {
	ID         uint64     `json:"id"`
	OrgID      uint64     `json:"org_id"`
	ProjectID  uint64     `json:"project_id"`
	Name       string     `json:"name"`
	Purpose    string     `json:"purpose,omitempty"`
	Status     string     `json:"status"`
	CreatedBy  uint64     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

func ToChannelResponse(c *model.Channel) ChannelResponse {
	return ChannelResponse{
		ID: c.ID, OrgID: c.OrgID, ProjectID: c.ProjectID, Name: c.Name, Purpose: c.Purpose,
		Status: c.Status, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		ArchivedAt: c.ArchivedAt,
	}
}

func ToChannelListResponse(cs []model.Channel) []ChannelResponse {
	out := make([]ChannelResponse, 0, len(cs))
	for i := range cs {
		out = append(out, ToChannelResponse(&cs[i]))
	}
	return out
}

type ChannelMemberResponse struct {
	ChannelID   uint64    `json:"channel_id"`
	PrincipalID uint64    `json:"principal_id"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

func ToMemberResponse(m *model.ChannelMember) ChannelMemberResponse {
	return ChannelMemberResponse{
		ChannelID: m.ChannelID, PrincipalID: m.PrincipalID, Role: m.Role, JoinedAt: m.JoinedAt,
	}
}

func ToMemberListResponse(ms []model.ChannelMember) []ChannelMemberResponse {
	out := make([]ChannelMemberResponse, 0, len(ms))
	for i := range ms {
		out = append(out, ToMemberResponse(&ms[i]))
	}
	return out
}

// ─── Message DTOs ────────────────────────────────────────────────────────────

// ReplyPreviewResponse 引用卡片预览(作者 + 前若干字正文)。
// Missing=true 表示原消息已不存在(被硬删除等极少情况,目前代码不会走到,留给未来兜底)。
type ReplyPreviewResponse struct {
	MessageID         uint64 `json:"message_id"`
	AuthorPrincipalID uint64 `json:"author_principal_id"`
	BodySnippet       string `json:"body_snippet"`
	Missing           bool   `json:"missing"`
}

// ReactionEntryResponse 单个 emoji 的反应聚合(同一 emoji 多人合并)。
type ReactionEntryResponse struct {
	Emoji        string   `json:"emoji"`
	PrincipalIDs []uint64 `json:"principal_ids"`
}

// AddReactionRequest POST /v2/messages/:id/reactions body。
type AddReactionRequest struct {
	Emoji string `json:"emoji" binding:"required"`
}

// ChannelMessageResponse 单条消息响应 —— 含 mentions / reactions 数组。
// ReplyToMessageID / ReplyToPreview 只在本消息确实在引用另一条时填充(否则 omitempty)。
type ChannelMessageResponse struct {
	ID                uint64                  `json:"id"`
	ChannelID         uint64                  `json:"channel_id"`
	AuthorPrincipalID uint64                  `json:"author_principal_id"`
	Body              string                  `json:"body"`
	Kind              string                  `json:"kind"`
	Mentions          []uint64                `json:"mentions"`
	Reactions         []ReactionEntryResponse `json:"reactions,omitempty"`
	ReplyToMessageID  *uint64                 `json:"reply_to_message_id,omitempty"`
	ReplyToPreview    *ReplyPreviewResponse   `json:"reply_to_preview,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
}

// ToMessageResponse 单条消息 + mentions + 可选 reply 预览 + reactions 拼 DTO。
//
// replyPreview 接 service 层的 *service.ReplyPreview 指针(可 nil);
// 为避免 dto 包直接 import service 包,签名用 any,调用方自己转。
func ToMessageResponse(m *model.ChannelMessage, mentions []uint64, replyPreview *ReplyPreviewResponse, reactions []ReactionEntryResponse) ChannelMessageResponse {
	if mentions == nil {
		mentions = []uint64{}
	}
	return ChannelMessageResponse{
		ID:                m.ID,
		ChannelID:         m.ChannelID,
		AuthorPrincipalID: m.AuthorPrincipalID,
		Body:              m.Body,
		Kind:              m.Kind,
		Mentions:          mentions,
		Reactions:         reactions,
		ReplyToMessageID:  m.ReplyToMessageID,
		ReplyToPreview:    replyPreview,
		CreatedAt:         m.CreatedAt,
	}
}

// ListMessagesResponse 列表响应 —— 附 cursor(下一页起点 = 本页最老一条的 id-1)。
// 前端循环 GET ...&before_id=<cursor> 翻页;cursor=0 表示无更多。
type ListMessagesResponse struct {
	Messages []ChannelMessageResponse `json:"messages"`
	Cursor   uint64                   `json:"cursor"` // 0 = 无更多
}

// ─── KBRef DTOs ──────────────────────────────────────────────────────────────

// AddKBRefRequest 挂载 KB 资源到 channel 的请求体。
// kb_source_id 和 kb_document_id 二选一(恰好一个非零)。
type AddKBRefRequest struct {
	KBSourceID   uint64 `json:"kb_source_id,omitempty"`
	KBDocumentID uint64 `json:"kb_document_id,omitempty"`
}

// KBRefResponse 单个 KBRef 响应。
type KBRefResponse struct {
	ID           uint64    `json:"id"`
	ChannelID    uint64    `json:"channel_id"`
	KBSourceID   uint64    `json:"kb_source_id,omitempty"`
	KBDocumentID uint64    `json:"kb_document_id,omitempty"`
	AddedBy      uint64    `json:"added_by"`
	AddedAt      time.Time `json:"added_at"`
}

func ToKBRefResponse(r *model.ChannelKBRef) KBRefResponse {
	return KBRefResponse{
		ID:           r.ID,
		ChannelID:    r.ChannelID,
		KBSourceID:   r.KBSourceID,
		KBDocumentID: r.KBDocumentID,
		AddedBy:      r.AddedBy,
		AddedAt:      r.AddedAt,
	}
}

func ToKBRefListResponse(rs []model.ChannelKBRef) []KBRefResponse {
	out := make([]KBRefResponse, 0, len(rs))
	for i := range rs {
		out = append(out, ToKBRefResponse(&rs[i]))
	}
	return out
}

// ─── Channel 共享文档 DTOs(PR #9') ───────────────────────────────────────────

// CreateChannelDocumentRequest 创建空白共享文档。
type CreateChannelDocumentRequest struct {
	Title       string `json:"title" binding:"required"`
	ContentKind string `json:"content_kind" binding:"required"` // "md" | "text"
}

// SaveChannelDocumentVersionRequest 保存新版本(单 JSON 形态;Content 直接放 body)。
//
// 用 base64 string 而不是 []byte —— gin JSON 自动 base64 解码 []byte 字段时,
// JS Blob.text() 上来的内容不会被 base64 编码,显式 string 才能让前端选择。
// 此处 Content 是文本 raw string(md / text 都是 UTF-8 文本)。
type SaveChannelDocumentVersionRequest struct {
	Content     string `json:"content" binding:"required"`
	EditSummary string `json:"edit_summary,omitempty"`
}

// ChannelDocumentResponse 单文档元数据 + 锁状态。
type ChannelDocumentResponse struct {
	ID                   uint64                       `json:"id"`
	ChannelID            uint64                       `json:"channel_id"`
	OrgID                uint64                       `json:"org_id"`
	Title                string                       `json:"title"`
	ContentKind          string                       `json:"content_kind"`
	CurrentVersion       string                       `json:"current_version,omitempty"`
	CurrentByteSize      int64                        `json:"current_byte_size"`
	CreatedByPrincipalID uint64                       `json:"created_by_principal_id"`
	UpdatedByPrincipalID uint64                       `json:"updated_by_principal_id"`
	CreatedAt            time.Time                    `json:"created_at"`
	UpdatedAt            time.Time                    `json:"updated_at"`
	DeletedAt            *time.Time                   `json:"deleted_at,omitempty"`
	Lock                 *ChannelDocumentLockResponse `json:"lock,omitempty"`
}

// ChannelDocumentLockResponse 锁状态。Acquired 仅在抢/续锁返回时有意义。
type ChannelDocumentLockResponse struct {
	HeldByPrincipalID uint64    `json:"held_by_principal_id"`
	LockedAt          time.Time `json:"locked_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	Acquired          bool      `json:"acquired"`
}

func ToChannelDocumentResponse(d *model.ChannelDocument, lock *model.ChannelDocumentLock) ChannelDocumentResponse {
	resp := ChannelDocumentResponse{
		ID:                   d.ID,
		ChannelID:            d.ChannelID,
		OrgID:                d.OrgID,
		Title:                d.Title,
		ContentKind:          d.ContentKind,
		CurrentVersion:       d.CurrentVersion,
		CurrentByteSize:      d.CurrentByteSize,
		CreatedByPrincipalID: d.CreatedByPrincipalID,
		UpdatedByPrincipalID: d.UpdatedByPrincipalID,
		CreatedAt:            d.CreatedAt,
		UpdatedAt:            d.UpdatedAt,
		DeletedAt:            d.DeletedAt,
	}
	if lock != nil {
		resp.Lock = &ChannelDocumentLockResponse{
			HeldByPrincipalID: lock.LockedByPrincipalID,
			LockedAt:          lock.LockedAt,
			ExpiresAt:         lock.ExpiresAt,
		}
	}
	return resp
}

func ToChannelDocumentListResponse(ds []model.ChannelDocument) []ChannelDocumentResponse {
	out := make([]ChannelDocumentResponse, 0, len(ds))
	for i := range ds {
		out = append(out, ToChannelDocumentResponse(&ds[i], nil))
	}
	return out
}

// ChannelDocumentUploadURLResponse Presign 直传场景返:URL + commit_token。
//
// 客户端用 PUT 把字节传到 UploadURL,**必须带 `Content-Type: <ContentType>`**(签名时绑定),
// 然后调 commit-upload 端点带 commit_token。token 在 ExpiresAt 后失效。
type ChannelDocumentUploadURLResponse struct {
	UploadURL   string    `json:"upload_url"`
	CommitToken string    `json:"commit_token"`
	ContentType string    `json:"content_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	MaxByteSize int64     `json:"max_byte_size"`
}

// CommitChannelDocumentUploadRequest commit 阶段入参。
type CommitChannelDocumentUploadRequest struct {
	CommitToken string `json:"commit_token" binding:"required"`
	EditSummary string `json:"edit_summary,omitempty"`
}

// RequestChannelDocumentUploadURLRequest presign-upload 阶段可选入参。
//
// BaseVersion 乐观锁:RMW 模式必传 download 时拿到的 version。空 = 跳过校验(盲写)。
type RequestChannelDocumentUploadURLRequest struct {
	BaseVersion string `json:"base_version,omitempty"`
}

// CommitChannelDocumentUploadResponse commit 成功:版本 + 文档新指针。
type CommitChannelDocumentUploadResponse struct {
	Document ChannelDocumentResponse        `json:"document"`
	Version  ChannelDocumentVersionResponse `json:"version"`
	Created  bool                           `json:"created"`
}

// ChannelDocumentDownloadURLResponse Presign GET 直拉返:URL + 版本元数据快照。
type ChannelDocumentDownloadURLResponse struct {
	DownloadURL string    `json:"download_url"`
	Version     string    `json:"version"`
	ByteSize    int64     `json:"byte_size"`
	ContentType string    `json:"content_type"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ChannelDocumentVersionResponse 单版本元数据(不含字节;字节单独走 GET .../content)。
type ChannelDocumentVersionResponse struct {
	ID                  uint64    `json:"id"`
	DocumentID          uint64    `json:"document_id"`
	Version             string    `json:"version"`
	ByteSize            int64     `json:"byte_size"`
	EditedByPrincipalID uint64    `json:"edited_by_principal_id"`
	EditSummary         string    `json:"edit_summary,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

func ToChannelDocumentVersionResponse(v *model.ChannelDocumentVersion) ChannelDocumentVersionResponse {
	return ChannelDocumentVersionResponse{
		ID:                  v.ID,
		DocumentID:          v.DocumentID,
		Version:             v.Version,
		ByteSize:            v.ByteSize,
		EditedByPrincipalID: v.EditedByPrincipalID,
		EditSummary:         v.EditSummary,
		CreatedAt:           v.CreatedAt,
	}
}

func ToChannelDocumentVersionListResponse(vs []model.ChannelDocumentVersion) []ChannelDocumentVersionResponse {
	out := make([]ChannelDocumentVersionResponse, 0, len(vs))
	for i := range vs {
		out = append(out, ToChannelDocumentVersionResponse(&vs[i]))
	}
	return out
}

// ChannelDocumentContentResponse 读最新版/某版的字节响应。Content 是 UTF-8 raw 字符串。
type ChannelDocumentContentResponse struct {
	Document ChannelDocumentResponse        `json:"document"`
	Version  ChannelDocumentVersionResponse `json:"version"`
	Content  string                         `json:"content"`
}

// SaveChannelDocumentVersionResponse 保存返回。Created=false 表示同 hash 已存在,
// 未实际写新版(Document.UpdatedAt 不变)。
type SaveChannelDocumentVersionResponse struct {
	Document ChannelDocumentResponse        `json:"document"`
	Version  ChannelDocumentVersionResponse `json:"version"`
	Created  bool                           `json:"created"`
}

// LockOperationResponse 抢/续锁的统一响应:Acquired=true 表示本次拿到/续到锁,
// false 表示别人持着(配合 ChannelDocumentLockHeld 错误码使用)。
type LockOperationResponse struct {
	Lock ChannelDocumentLockResponse `json:"lock"`
}

// ─── Channel 附件 DTOs ───────────────────────────────────────────────────────

// RequestChannelAttachmentUploadURLRequest presign-upload 入参。
//
// MimeType 必须在 chanerr.AllowedAttachmentMimeTypes 白名单(image/png|jpeg|gif|webp)。
// Filename 可空,透传到 commit。
type RequestChannelAttachmentUploadURLRequest struct {
	MimeType string `json:"mime_type" binding:"required"`
	Filename string `json:"filename,omitempty"`
}

// ChannelAttachmentUploadURLResponse Presign 直传场景返:URL + commit_token。
//
// 客户端 PUT 字节到 UploadURL,**必须带 `Content-Type: <ContentType>`**(签名时绑定),
// 然后调 upload-commit 端点带 commit_token。token 在 ExpiresAt 后失效。
type ChannelAttachmentUploadURLResponse struct {
	UploadURL   string    `json:"upload_url"`
	CommitToken string    `json:"commit_token"`
	ContentType string    `json:"content_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	MaxByteSize int64     `json:"max_byte_size"`
}

// CommitChannelAttachmentUploadRequest commit 阶段入参。
type CommitChannelAttachmentUploadRequest struct {
	CommitToken string `json:"commit_token" binding:"required"`
}

// ChannelAttachmentResponse 单 attachment 元数据。
//
// URL 是相对路径 `/api/v2/channels/<cid>/attachments/<aid>`,可直接拷进 markdown:
// `![alt](/api/v2/channels/123/attachments/456)`。前端 <img> 引用此 URL,浏览器
// 拉到后端 → 鉴权 → 302 到 OSS 短期签名 URL。
type ChannelAttachmentResponse struct {
	ID                    uint64    `json:"id"`
	ChannelID             uint64    `json:"channel_id"`
	OrgID                 uint64    `json:"org_id"`
	URL                   string    `json:"url"`
	MimeType              string    `json:"mime_type"`
	Filename              string    `json:"filename,omitempty"`
	ByteSize              int64     `json:"byte_size"`
	Sha256                string    `json:"sha256"`
	UploadedByPrincipalID uint64    `json:"uploaded_by_principal_id"`
	CreatedAt             time.Time `json:"created_at"`
}

// CommitChannelAttachmentUploadResponse commit 成功响应。Reused=true 表示同
// (channel_id, sha256) 已有行,本次未实际写新 attachment(返已有);此时新上传
// 的 OSS 对象已被删。
type CommitChannelAttachmentUploadResponse struct {
	Attachment ChannelAttachmentResponse `json:"attachment"`
	Reused     bool                      `json:"reused"`
}

// ToChannelAttachmentResponse 拼 DTO。urlBase 为相对路径前缀(handler 拼)。
func ToChannelAttachmentResponse(a *model.ChannelAttachment, urlBase string) ChannelAttachmentResponse {
	return ChannelAttachmentResponse{
		ID:                    a.ID,
		ChannelID:             a.ChannelID,
		OrgID:                 a.OrgID,
		URL:                   urlBase,
		MimeType:              a.MimeType,
		Filename:              a.Filename,
		ByteSize:              a.ByteSize,
		Sha256:                a.Sha256,
		UploadedByPrincipalID: a.UploadedByPrincipalID,
		CreatedAt:             a.CreatedAt,
	}
}
