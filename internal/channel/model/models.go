// Package model channel 模块数据模型 —— project / version / channel /
// channel_versions / channel_members 五张表。
//
// 对应设计见 docs/collaboration-design.md §3.9。ID 类型统一 BIGINT UNSIGNED
// (对齐 users / orgs / principals 的 autoIncrement)。
package model

import "time"

// Project 项目:org 下的产品 / 项目单元。
//
// 字段:
//   - OrgID:所属 org;权限、审计边界
//   - Name:项目名,一个 org 内活跃项目(archived_at IS NULL)唯一
//   - CreatedBy:创建者 principal_id;FK principals.id
//   - ArchivedAt:非空表示已归档;通过 name_active 生成列实现"归档后释放名字"
//     (见 migration.go 的 ensureProjectNameActiveColumn)
//
// 索引:
//   - idx_projects_org:org 下列表
//   - uk_projects_org_name_active(org_id, name_active):活跃项目名唯一
//     (在 migration 里用 raw DDL 建,因为依赖生成列)
type Project struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrgID       uint64     `gorm:"not null;index:idx_projects_org" json:"org_id"`
	Name        string     `gorm:"size:128;not null" json:"name"`
	Description string     `gorm:"size:512" json:"description,omitempty"`
	CreatedBy   uint64     `gorm:"not null" json:"created_by"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null" json:"updated_at"`
	ArchivedAt  *time.Time `gorm:"index:idx_projects_archived" json:"archived_at,omitempty"`
}

// TableName 固定表名。
func (Project) TableName() string { return "projects" }

// Version project 下的里程碑(弱关联,多对多 channel,见 ChannelVersion)。
type Version struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID  uint64     `gorm:"not null;uniqueIndex:uk_versions_project_name,priority:1" json:"project_id"`
	Name       string     `gorm:"size:64;not null;uniqueIndex:uk_versions_project_name,priority:2" json:"name"`
	Status     string     `gorm:"size:16;not null" json:"status"`
	TargetDate *time.Time `json:"target_date,omitempty"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (Version) TableName() string { return "versions" }

// Channel 协作工作空间。一个 channel 承载一次跨人/agent 的协作。
//
// 生命周期:open -> archived(不可逆,但 archived_at 保留审计)。
// archive 时:Phase 1 只改状态;Phase 2 会触发 3.8 的 artifact 晋升 KB。
//
// 新字段(PR-A):
//   - Kind:'regular'(ad-hoc 临时) / 'workstream'(workstream 自动开的协作面)/
//     'project_console'(每 project 唯一,Architect agent 工作间)。新建 channel
//     默认 regular;workstream 自动 lazy-create 时由 pm 模块带 'workstream' 入参
//   - WorkstreamID:NULLABLE,反向引用 pm.workstreams.id;Kind='workstream' 时必填
type Channel struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrgID         uint64     `gorm:"not null;index:idx_channels_org" json:"org_id"`
	ProjectID     uint64     `gorm:"not null;index:idx_channels_project_status,priority:1" json:"project_id"`
	Name          string     `gorm:"size:128;not null" json:"name"`
	Purpose       string     `gorm:"size:512" json:"purpose,omitempty"`
	Status        string     `gorm:"size:16;not null;default:open;index:idx_channels_project_status,priority:2" json:"status"`
	Kind          string     `gorm:"size:32;not null;default:regular;index:idx_channels_kind" json:"kind"`
	WorkstreamID  *uint64    `gorm:"index:idx_channels_workstream" json:"workstream_id,omitempty"`
	CreatedBy     uint64     `gorm:"not null" json:"created_by"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"not null" json:"updated_at"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
}

// TableName 固定表名。
func (Channel) TableName() string { return "channels" }

// ChannelMember channel 成员。principal_id 指向 principals(user 或 agent 统一入口)。
//
// Role 约束见 const.go 的 MemberRole* 枚举。owner 不能被删成零个 —— service 层兜底。
type ChannelMember struct {
	ChannelID   uint64    `gorm:"primaryKey" json:"channel_id"`
	PrincipalID uint64    `gorm:"primaryKey;index:idx_channel_members_principal" json:"principal_id"`
	Role        string    `gorm:"size:16;not null" json:"role"`
	JoinedAt    time.Time `gorm:"not null" json:"joined_at"`
}

// TableName 固定表名。
func (ChannelMember) TableName() string { return "channel_members" }

// ChannelMessage channel 内的一条消息。
//
// 字段语义:
//   - ChannelID:所属 channel(FK channels.id)
//   - AuthorPrincipalID:作者 principal;user 和 agent 统一(FK principals.id)
//   - Body:markdown 文本。@xxx 显示在 body 里,但 mentions 由独立表表达(不做文本 parse)
//   - Kind:'text'(人 / agent 发的正常消息)/ 'system_event'(channel 创建 /
//     member 加入 / archive 等系统消息,由服务端写入,用户不能直接产生)
//   - ReplyToMessageID:本条消息是对该 id 的回复(同 channel 的另一条);nil 表示普通消息
//   - SourceEventID:仅用于 kind='system_event' —— 记录来源 Redis Stream event ID
//     (形如 "1713865200000-0")。UNIQUE 列保证 consumer 重放同一事件不产生重复卡片。
//     text 消息这一列为空(NULL)。
//
// 索引:
//   - (channel_id, created_at):拉取 channel 消息流,按时间正序 / 倒序分页
//   - reply_to_message_id:反向查"这条消息有哪些回复"(thread 视图未来用)
//   - source_event_id UNIQUE:system_event 幂等
type ChannelMessage struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ChannelID         uint64    `gorm:"not null;index:idx_channel_messages_channel_created,priority:1" json:"channel_id"`
	AuthorPrincipalID uint64    `gorm:"not null;index:idx_channel_messages_author" json:"author_principal_id"`
	Body              string    `gorm:"type:text;not null" json:"body"`
	Kind              string    `gorm:"size:16;not null;default:text" json:"kind"`
	ReplyToMessageID  *uint64   `gorm:"index:idx_channel_messages_reply_to" json:"reply_to_message_id,omitempty"`
	SourceEventID     *string   `gorm:"column:source_event_id;size:64;uniqueIndex:uk_channel_messages_source_event" json:"source_event_id,omitempty"`
	CreatedAt         time.Time `gorm:"not null;index:idx_channel_messages_channel_created,priority:2,sort:desc" json:"created_at"`
}

// TableName 固定表名。
func (ChannelMessage) TableName() string { return "channel_messages" }

// ChannelMessageMention 消息里的 @mention 关联。
//
// 后端**不做**文本 @xxx 解析,由前端 / MCP client 在发消息时把 `@` 目标的
// principal_id 列表显式传上来;后端只按 principal_id 写入本表。
//
// 复合 PK (message_id, principal_id) 保证同一 mention 不重复写;级联删除消息。
type ChannelMessageMention struct {
	MessageID   uint64 `gorm:"primaryKey" json:"message_id"`
	PrincipalID uint64 `gorm:"primaryKey;index:idx_channel_message_mentions_principal" json:"principal_id"`
}

// TableName 固定表名。
func (ChannelMessageMention) TableName() string { return "channel_message_mentions" }

// ChannelMessageReaction 消息的 emoji 反应(PR #12')。
//
// 一个 (message_id, principal_id, emoji) 元组表示"某人对某条消息打了某个 emoji",
// 复合 PK 保证同一三元组不重复;同一人可对同一条消息打多个不同 emoji。
//
// emoji 由 service 层按 `AllowedReactionEmojis` 白名单校验(预设 12 个),防止
// 写入任意 Unicode 造成数据污染。
//
// 级联:消息被删时级联清理(靠 service 层事务,不靠 FK ON DELETE —— GORM 不建 FK)。
//
// 索引:
//   - 复合 PK 自身就是最佳读路径(按 message_id IN (...) 批量查反应列表)
type ChannelMessageReaction struct {
	MessageID   uint64    `gorm:"primaryKey" json:"message_id"`
	PrincipalID uint64    `gorm:"primaryKey" json:"principal_id"`
	Emoji       string    `gorm:"primaryKey;size:16" json:"emoji"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
}

// TableName 固定表名。
func (ChannelMessageReaction) TableName() string { return "channel_message_reactions" }

// ChannelKBRef 已退役 —— channel_kb_refs 表 + per-channel KB 挂载概念整体废弃,
// 改由 pm.ProjectKBRef 在 project 维度管理。表 DROP 由 pm.RunPostMigrations 完成。

// ChannelDocument channel 内的"共享文档"(PR #9')。
//
// 与 task_submissions 的区别:submission 是单人交付物 + 审批,共享文档是多人共建
// 的产物,生命周期跟随 channel,默认 channel 成员都能读 + 抢锁编辑。归档晋升到 KB
// 由后续 PR 处理。
//
// CurrentOSSKey / CurrentVersion / CurrentByteSize:冗余的"最新版指针",避免读
// 文档列表时还要 JOIN versions 表;每次保存随版本一起更新。
//
// DeletedAt:软删,channel 内列表过滤掉,但 versions / locks 历史保留作审计。
//
// 索引:
//   - (channel_id, deleted_at):列 channel 下未删的共享文档
type ChannelDocument struct {
	ID                      uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ChannelID               uint64     `gorm:"not null;index:idx_channel_documents_channel_deleted,priority:1" json:"channel_id"`
	OrgID                   uint64     `gorm:"not null" json:"org_id"`
	Title                   string     `gorm:"size:128;not null" json:"title"`
	ContentKind             string     `gorm:"size:16;not null" json:"content_kind"`
	CurrentOSSKey           string     `gorm:"column:current_oss_key;type:text;not null" json:"current_oss_key"`
	CurrentVersion          string     `gorm:"column:current_version;size:64;not null" json:"current_version"`
	CurrentByteSize         int64      `gorm:"column:current_byte_size;not null;default:0" json:"current_byte_size"`
	CreatedByPrincipalID    uint64     `gorm:"not null" json:"created_by_principal_id"`
	UpdatedByPrincipalID    uint64     `gorm:"not null" json:"updated_by_principal_id"`
	CreatedAt               time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt               time.Time  `gorm:"not null" json:"updated_at"`
	DeletedAt               *time.Time `gorm:"index:idx_channel_documents_channel_deleted,priority:2" json:"deleted_at,omitempty"`
}

// TableName 固定表名。
func (ChannelDocument) TableName() string { return "channel_documents" }

// ChannelDocumentVersion 共享文档的版本历史(append-only)。
//
// Version 字段是 SHA256 hex(64 字符);UNIQUE (document_id, version) 让"重复 save
// 同样字节"幂等(同 hash 撞约束,service 层翻成"无变更")。
//
// EditSummary 调用方填的"我改了啥"备注;最长 255 字符,空串允许。
//
// 索引:
//   - (document_id, id DESC):按时间倒序拉历史
type ChannelDocumentVersion struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	DocumentID           uint64    `gorm:"not null;uniqueIndex:uk_channel_document_versions_doc_version,priority:1;index:idx_channel_document_versions_doc_id,priority:1" json:"document_id"`
	Version              string    `gorm:"size:64;not null;uniqueIndex:uk_channel_document_versions_doc_version,priority:2" json:"version"`
	OSSKey               string    `gorm:"column:oss_key;type:text;not null" json:"oss_key"`
	ByteSize             int64     `gorm:"not null" json:"byte_size"`
	EditedByPrincipalID  uint64    `gorm:"not null" json:"edited_by_principal_id"`
	EditSummary          string    `gorm:"size:255;not null;default:''" json:"edit_summary,omitempty"`
	CreatedAt            time.Time `gorm:"not null;index:idx_channel_document_versions_doc_id,priority:2,sort:desc" json:"created_at"`
}

// TableName 固定表名。
func (ChannelDocumentVersion) TableName() string { return "channel_document_versions" }

// ChannelDocumentLock 共享文档的独占编辑锁(每文档单行,PK=document_id 自带互斥)。
//
// 抢锁:repository 层先 INSERT IGNORE,失败再 UPDATE WHERE expires_at<NOW OR
// locked_by=caller(过期或同人续);两步都失败说明被别人持着 → ErrLockHeld。
//
// 心跳:同人 UPDATE expires_at = NOW + TTL。
//
// 释放:DELETE WHERE document_id AND locked_by_principal_id = caller。
//
// 强制解锁:DELETE WHERE document_id(channel owner 或锁已过期的任意成员)。
//
// 索引:
//   - PK 自身就是单点查找;expires_at 不建索引(扫描清理交给 INSERT 路径自己判断)
type ChannelDocumentLock struct {
	DocumentID           uint64    `gorm:"primaryKey" json:"document_id"`
	LockedByPrincipalID  uint64    `gorm:"not null" json:"locked_by_principal_id"`
	LockedAt             time.Time `gorm:"not null" json:"locked_at"`
	ExpiresAt            time.Time `gorm:"not null" json:"expires_at"`
}

// TableName 固定表名。
func (ChannelDocumentLock) TableName() string { return "channel_document_locks" }

// ChannelAttachment channel 级附件(图片等)。
//
// 用途:Markdown 文档 / 消息内嵌图片。归属 channel 而非 doc / message ——
// 同一附件可在多 doc + 多消息中引用;attachment 是 channel 级公共池。
//
// 鉴权:GET /attachments/:id 校验 caller 是 channel 成员,然后 302 到 OSS
// 短期签名 URL —— 字节不经 server,签名 URL 5min 失效,泄露窗口可控。
//
// 去重:(channel_id, sha256) UNIQUE。同 channel 重传同字节直接复用已有行,
// 不重复占 OSS。
//
// 生命周期:第一版不做主动 GC(反向追踪 markdown 引用成本太高,OSS 便宜);
// channel 整体删除时一并标 deleted_at + 异步 OSS 清理。
//
// 字段:
//   - OrgID:冗余,与 channel.org_id 一致;鉴权 / 多租户隔离用
//   - OSSKey:`<prefix>/<orgID>/channel-attachments/<channelID>/<rand>.<ext>`
//   - MimeType:必须在 AllowedAttachmentMimeTypes 白名单(image/png|jpeg|gif|webp);
//     SVG 不收(嵌入 markdown 渲染存在 JS 执行边界)
//   - Filename:用户上传时给的名字,可空(curl 直传场景没文件名)
//   - Sha256:64 hex;服务端 commit 时 StreamGet 算
//   - DeletedAt:软删,列表过滤掉,但 OSS 对象保留待异步清理
//
// 索引:
//   - (channel_id, deleted_at):列 channel 下未删 attachment(治理用,日常很少)
//   - (channel_id, sha256) UNIQUE:去重命中查
type ChannelAttachment struct {
	ID                    uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ChannelID             uint64     `gorm:"not null;index:idx_channel_attachments_channel_deleted,priority:1;uniqueIndex:uk_channel_attachments_channel_sha,priority:1" json:"channel_id"`
	OrgID                 uint64     `gorm:"not null" json:"org_id"`
	OSSKey                string     `gorm:"column:oss_key;type:text;not null" json:"oss_key"`
	MimeType              string     `gorm:"size:64;not null" json:"mime_type"`
	Filename              string     `gorm:"size:256;not null;default:''" json:"filename,omitempty"`
	ByteSize              int64      `gorm:"not null" json:"byte_size"`
	Sha256                string     `gorm:"size:64;not null;uniqueIndex:uk_channel_attachments_channel_sha,priority:2" json:"sha256"`
	UploadedByPrincipalID uint64     `gorm:"not null" json:"uploaded_by_principal_id"`
	CreatedAt             time.Time  `gorm:"not null" json:"created_at"`
	DeletedAt             *time.Time `gorm:"index:idx_channel_attachments_channel_deleted,priority:2" json:"deleted_at,omitempty"`
}

// TableName 固定表名。
func (ChannelAttachment) TableName() string { return "channel_attachments" }
