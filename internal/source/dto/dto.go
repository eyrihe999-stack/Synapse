// dto.go source 模块 HTTP 请求/响应 DTO 定义。
package dto

// ─── Source ───────────────────────────────────────────────────────────────────

// SourceResponse 知识源的展示 DTO。
type SourceResponse struct {
	ID          uint64 `json:"id,string"`
	OrgID       uint64 `json:"org_id,string"`
	Kind        string `json:"kind"`
	OwnerUserID uint64 `json:"owner_user_id,string"`
	ExternalRef string `json:"external_ref,omitempty"`
	Name        string `json:"name"`
	Visibility  string `json:"visibility"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`

	// ─── GitLab 专属(非 gitlab_repo 留零值,前端按 kind 决定渲染)──────────
	GitLabBranch     string `json:"gitlab_branch,omitempty"`
	LastSyncStatus   string `json:"last_sync_status,omitempty"`
	LastSyncedAt     int64  `json:"last_synced_at,omitempty"`
	LastSyncedCommit string `json:"last_synced_commit,omitempty"`
	// LastSyncError 仅 status=failed/auth_failed 时非空。webhook secret 任何场景都不返 ——
	// 创建端点的 CreateGitLabSourceResponse 单独带一次明文,后续不再暴露。
	LastSyncError string `json:"last_sync_error,omitempty"`
}

// CreateGitLabSourceRequest 创建 gitlab_repo 同步源。
//
//   - BaseURL:GitLab 实例 URL,空串 → https://gitlab.com(自托管 GitLab 必填,如 https://gitlab.example.com)
//   - PAT:owner 自己的 GitLab Personal Access Token,scope 至少需 read_api + read_repository
//   - ProjectID:GitLab project 数字 id(string,大数 JS 安全);owner 凭据须对其有 read 权限
//   - Branch:同步分支,空串 → "main"
//   - Visibility:org/group/private,空串 → org
//
// server 流程:验 PAT → upsert user_integrations(provider=gitlab,external_account_id=GitLab user id)→
// 校验 project 可读 → 创建 source(integration_id 指向上一步)→ enqueue 全量 sync。
type CreateGitLabSourceRequest struct {
	BaseURL    string `json:"base_url,omitempty"`
	PAT        string `json:"pat" binding:"required"`
	ProjectID  string `json:"project_id" binding:"required"`
	Branch     string `json:"branch,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// CreateGitLabSourceResponse 创建后的响应。
//
// WebhookSecret 是**唯一一次**返明文的机会:owner 必须立刻粘到 GitLab Project →
// Settings → Webhooks 的 Secret Token 字段。后续 rotate 端点(后续 PR)重生 + 再返一次。
//
// WebhookURL 由后端按 cfg.Server.PublicBaseURL(fallback OAuth.Issuer)拼出完整可点 URL;
// 服务端没配公网基址 → 返空串,前端 fallback 到 window.location.origin 并显示 localhost 警告。
type CreateGitLabSourceResponse struct {
	Source        SourceResponse `json:"source"`
	WebhookSecret string         `json:"webhook_secret"`
	WebhookURL    string         `json:"webhook_url,omitempty"`
	JobID        uint64         `json:"job_id,string"` // 全量 sync 异步任务 id,前端可轮询进度
}

// TriggerResyncResponse 重新触发全量同步的响应。
type TriggerResyncResponse struct {
	JobID uint64 `json:"job_id,string"`
}

// GitLabSyncStatusResponse 单 GitLab source 当前 / 最近一次同步任务的状态。
//
// 找不到任何 job(从未同步过)→ JobID=0 + Status="never",前端按"从未同步"展示。
// running / queued 状态:Progress* 字段实时变化,前端轮询;HeartbeatAt 用来判断 runner 是否卡。
// 终态:FinishedAt 非零;Error 仅 status=failed 时非空。
type GitLabSyncStatusResponse struct {
	JobID          uint64 `json:"job_id,string,omitempty"`
	Status         string `json:"status"` // queued / running / succeeded / failed / canceled / never
	Mode           string `json:"mode,omitempty"` // full / incremental
	ProgressDone   int    `json:"progress_done"`
	ProgressTotal  int    `json:"progress_total"`
	ProgressFailed int    `json:"progress_failed"`
	StartedAt      int64  `json:"started_at,omitempty"`   // unix seconds
	FinishedAt     int64  `json:"finished_at,omitempty"`
	HeartbeatAt    int64  `json:"heartbeat_at,omitempty"`
	Error          string `json:"error,omitempty"`
}

// ListSourcesResponse 分页列 source 响应。
type ListSourcesResponse struct {
	Items []SourceResponse `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Size  int              `json:"size"`
}

// UpdateVisibilityRequest 改 visibility 的请求体。
type UpdateVisibilityRequest struct {
	Visibility string `json:"visibility" binding:"required"`
}

// CreateSourceRequest 用户自建一个 custom 数据源。
// visibility 省略 → 默认 org;name 必填,2-128 字符(前后空白会被 trim 后校验)。
type CreateSourceRequest struct {
	Name       string `json:"name" binding:"required"`
	Visibility string `json:"visibility,omitempty"`
}

// ─── Source ACL ───────────────────────────────────────────────────────────────

// GrantSourceACLRequest 添加一条 source ACL 授权。
type GrantSourceACLRequest struct {
	SubjectType string `json:"subject_type" binding:"required"` // "group" | "user"
	SubjectID   uint64 `json:"subject_id,string" binding:"required"`
	Permission  string `json:"permission" binding:"required"` // "read" | "write"
}

// UpdateSourceACLRequest 改一条 ACL 的 permission(只能改 permission,subject 不可变)。
type UpdateSourceACLRequest struct {
	Permission string `json:"permission" binding:"required"` // "read" | "write"
}

// SourceACLResponse ACL 行的展示 DTO。
type SourceACLResponse struct {
	ID          uint64 `json:"id,string"`
	SourceID    uint64 `json:"source_id,string"`
	SubjectType string `json:"subject_type"`
	SubjectID   uint64 `json:"subject_id,string"`
	Permission  string `json:"permission"`
	GrantedBy   uint64 `json:"granted_by,string"`
	CreatedAt   int64  `json:"created_at"`
}

// ListSourceACLResponse list ACL 响应(不分页,单 source 的 ACL 行天然量级小)。
type ListSourceACLResponse struct {
	Items []SourceACLResponse `json:"items"`
}
