// Package repository channel 模块数据访问层。
//
// 设计对齐 internal/organization/repository:一个顶层 Repository interface
// 汇总所有方法,实现按 entity 拆到多个文件(project.go / version.go / channel.go /
// member.go / channel_version.go)。事务支持通过 WithTx 传入同一个 gorm.DB 句柄。
//
// 错误处理:repository 层直接返回底层 gorm 错误或 errors.Is(err, gorm.ErrRecordNotFound)
// 由 service 层翻译成模块的哨兵错误。
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// Repository 所有 channel 子实体数据访问的统一入口。
type Repository interface {
	// ── 事务 ────────────────────────────────────────────────────────────────
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ── Project lookup(轻量,不持有 Project 实体)──────────────────────────
	// Project 主体 CRUD 已迁到 pm 模块(internal/pm/repository)。channel 模块
	// 只保留"读 Project 元信息"的轻量查询,因为创建 channel / 加成员等动作要查
	// project.org_id / archived_at 做权限检查。
	//
	// 返回的 *ChannelProjectInfo 是 channel 视角下需要的最小字段集,避免反向
	// 引入 pm/model 类型(channel ← pm 单向依赖)。
	FindProjectInfo(ctx context.Context, projectID uint64) (*ChannelProjectInfo, error)

	// ── Channel ────────────────────────────────────────────────────────────
	CreateChannel(ctx context.Context, c *model.Channel) error
	FindChannelByID(ctx context.Context, id uint64) (*model.Channel, error)
	ListChannelsByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Channel, error)
	// ListChannelsByPrincipal 列该 principal 作为成员的所有 channel(跨 project / 跨 org 都可能)。
	// JOIN channel_members 过滤。按 id DESC 分页。
	ListChannelsByPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]model.Channel, error)
	UpdateChannelFields(ctx context.Context, id uint64, updates map[string]any) error
	// ArchiveOpenChannelsByProject 把指定 project 下所有 status='open' 的 channel
	// 批量置为 archived + archived_at=now。返回被级联的行数。
	// 给 pm.project.archived 事件 consumer 用(级联归档下属 channel)。
	ArchiveOpenChannelsByProject(ctx context.Context, projectID uint64, now time.Time) (int64, error)

	// ── ChannelMember ─────────────────────────────────────────────────────
	AddMember(ctx context.Context, m *model.ChannelMember) error
	RemoveMember(ctx context.Context, channelID, principalID uint64) error
	UpdateMemberRole(ctx context.Context, channelID, principalID uint64, role string) error
	FindMember(ctx context.Context, channelID, principalID uint64) (*model.ChannelMember, error)
	ListMembers(ctx context.Context, channelID uint64) ([]model.ChannelMember, error)
	// ListMembersWithProfile JOIN users + agents 拿带 display_name + kind 的聚合,
	// 给 orchestrator 组 LLM prompt 用("成员名册")。详见 member.go 的实现注释。
	ListMembersWithProfile(ctx context.Context, channelID uint64) ([]MemberWithProfile, error)
	CountOwners(ctx context.Context, channelID uint64) (int64, error)

	// ── ChannelMessage ────────────────────────────────────────────────────
	CreateMessage(ctx context.Context, m *model.ChannelMessage) error
	AddMessageMentions(ctx context.Context, messageID uint64, principalIDs []uint64) error
	// ListMessages 按 cursor 分页拉 channel 消息,倒序(最新的在前)。
	// beforeID=0 表示从最新开始;否则拉 id < beforeID 的。limit 有效范围 1..100。
	ListMessages(ctx context.Context, channelID uint64, beforeID uint64, limit int) ([]model.ChannelMessage, error)
	// ListMentionsByMessages 批量拉多条消息的 mentions,用于 list 拼装。
	ListMentionsByMessages(ctx context.Context, messageIDs []uint64) ([]model.ChannelMessageMention, error)
	// ListMentionsByPrincipals 跨 channel 列**任一 principal_id 在 candidates 中**
	// 被 @ 的消息(JOIN messages),按 message_id DESC 分页。candidates 通常是
	// [callerAgentPID, ownerUserPID] —— 让 caller=agent 也能看到 user-side 的 @。
	// DISTINCT 去重(同消息 @ 多 candidate 只返一行)。MCP `list_my_mentions` 用。
	ListMentionsByPrincipals(ctx context.Context, candidatePrincipalIDs []uint64, sinceMessageID uint64, limit int) ([]MentionRow, error)
	// FindMessageBySourceEventID 按 source_event_id(Redis Stream event ID)查消息。
	// 用于 system_event consumer 幂等:写入前查是否已有同 event 生成的消息,防重放
	// 产生重复卡片。找不到返 (nil, nil);真实错误返 error。
	FindMessageBySourceEventID(ctx context.Context, sourceEventID string) (*model.ChannelMessage, error)

	// AddReaction 给消息打一个 emoji 反应。UNIQUE (message_id, principal_id, emoji) 复合
	// PK 防重复;调用方撞重复返 duplicate 视为幂等成功,service 层翻译。
	AddReaction(ctx context.Context, r *model.ChannelMessageReaction) error
	// RemoveReaction 撤销一个反应;不存在的 (message, principal, emoji) 返 gorm.ErrRecordNotFound,
	// service 层视为幂等成功。
	RemoveReaction(ctx context.Context, messageID, principalID uint64, emoji string) error
	// ListReactionsByMessages 批量拿多条消息的所有 reactions。给 ListMessages 拼返回用。
	ListReactionsByMessages(ctx context.Context, messageIDs []uint64) ([]model.ChannelMessageReaction, error)
	// FindMessageByID 按主键查消息(不限 channel_id)。reaction add/remove 用:
	// 客户端只传 message_id,服务端查出 channel 后再 gate 权限。
	FindMessageByID(ctx context.Context, messageID uint64) (*model.ChannelMessage, error)
	// FindMessageInChannel 单条 message 查找,限定 channel_id —— 用于 reply_to 校验,
	// 既能确认消息存在,又能保证它属于同 channel(阻断跨 channel 引用)。
	// 查无记录返 gorm.ErrRecordNotFound。
	FindMessageInChannel(ctx context.Context, channelID, messageID uint64) (*model.ChannelMessage, error)
	// FindMessagesByIDsInChannel 批量拉取 reply 预览(作者 + 前若干字正文)用,限定 channel_id。
	FindMessagesByIDsInChannel(ctx context.Context, channelID uint64, messageIDs []uint64) ([]model.ChannelMessage, error)

	// ── KB 可见集查询(走 project_kb_refs,channel_kb_refs 已退役)───────────
	// 实现见 kb_visibility.go;接 channels.project_id JOIN project_kb_refs。
	// 给 MCP `list_kb_documents` / `get_kb_document` / `search_kb` 做可见集计算用。
	ListKBSourceIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error)
	ListKBDocumentIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error)

	// ── ChannelDocument(PR #9' 共享文档)─────────────────────────────────
	CreateChannelDocument(ctx context.Context, d *model.ChannelDocument) error
	FindChannelDocumentByID(ctx context.Context, id uint64) (*model.ChannelDocument, error)
	ListChannelDocumentsByChannel(ctx context.Context, channelID uint64) ([]model.ChannelDocument, error)
	UpdateChannelDocumentFields(ctx context.Context, id uint64, updates map[string]any) error
	SoftDeleteChannelDocument(ctx context.Context, id uint64, now time.Time) error

	// CreateChannelDocumentVersion 写一条版本行;撞 UNIQUE(document_id, version) 返
	// gorm.ErrDuplicatedKey,service 层翻译成"无变更"幂等。
	CreateChannelDocumentVersion(ctx context.Context, v *model.ChannelDocumentVersion) error
	FindChannelDocumentVersionByID(ctx context.Context, id uint64) (*model.ChannelDocumentVersion, error)
	ListChannelDocumentVersions(ctx context.Context, docID uint64) ([]model.ChannelDocumentVersion, error)
	FindChannelDocumentVersionByHash(ctx context.Context, docID uint64, version string) (*model.ChannelDocumentVersion, error)

	// AcquireChannelDocumentLock 抢/续锁。语义:
	//   - 文档无锁 → 抢成功,返回 (callerPID, newExpires, true, nil)
	//   - 锁过期 → 覆盖抢成功
	//   - 同人持锁 → 续到 newExpires
	//   - 别人持锁未过期 → 返回 (heldByPID, currentExpires, false, nil),不报错
	// DB 异常返 (0, zero, false, err)。
	AcquireChannelDocumentLock(ctx context.Context, docID, callerPrincipalID uint64, ttl time.Duration, now time.Time) (heldBy uint64, expiresAt time.Time, acquired bool, err error)

	// ReleaseChannelDocumentLock 仅当 caller 持锁时删除;别人持/无锁均返 nil(幂等)。
	// 返回 released=true 仅当真删了一行。
	ReleaseChannelDocumentLock(ctx context.Context, docID, callerPrincipalID uint64) (released bool, err error)

	// ForceReleaseChannelDocumentLock 不校验持锁人,直接删行(channel owner 用)。
	// 返回 released=true 仅当真删了一行。
	ForceReleaseChannelDocumentLock(ctx context.Context, docID uint64) (released bool, err error)

	FindChannelDocumentLock(ctx context.Context, docID uint64) (*model.ChannelDocumentLock, error)
	// ListChannelDocumentLocksByDocIDs 批量拉一组文档的当前锁(列表视图渲染用);
	// 不在列表里的文档表示无锁。docIDs 空返空列表 + nil。
	ListChannelDocumentLocksByDocIDs(ctx context.Context, docIDs []uint64) ([]model.ChannelDocumentLock, error)

	// ── ChannelAttachment(频道级附件,Markdown 内嵌图片用)──────────────
	// CreateChannelAttachment 写新附件行。撞 (channel_id, sha256) UNIQUE 返
	// gorm.ErrDuplicatedKey,service 层翻成"复用现有"。
	CreateChannelAttachment(ctx context.Context, a *model.ChannelAttachment) error
	// FindChannelAttachmentByID 按 ID 查。不过滤 deleted_at,调用方按需判断。
	FindChannelAttachmentByID(ctx context.Context, id uint64) (*model.ChannelAttachment, error)
	// FindChannelAttachmentByChannelAndHash commit 阶段去重命中查询。
	// 找不到返 (nil, gorm.ErrRecordNotFound)。
	FindChannelAttachmentByChannelAndHash(ctx context.Context, channelID uint64, sha string) (*model.ChannelAttachment, error)
	// SoftDeleteChannelAttachment 软删,幂等。
	SoftDeleteChannelAttachment(ctx context.Context, id uint64, now time.Time) error

	// ── 跨模块查询(轻量,避免引入 user / agents 模块依赖)────────────────────
	// LookupUserPrincipalID 按 users.id 反查 principal_id。JWT sub=users.id,
	// channel 相关表存 principal_id,service 层要这条通路把两者串起来。
	LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error)

	// LookupAgentOwnerUserPrincipalID 反查 agent.principal_id → owner user 的
	// principal_id;非 agent / system agent / 脏数据均返 0。给 list_my_mentions /
	// dashboard 用 —— caller=agent 时把 owner user 加进 candidates,看到落在 user 身
	// 上的 @。实现对齐 task 模块同名方法。
	LookupAgentOwnerUserPrincipalID(ctx context.Context, agentPrincipalID uint64) (uint64, error)

	// LookupAutoIncludeAgentPrincipals 查"该 channel 所在 org 应被自动加为
	// channel member 的 agents" principal_id 列表:
	//   agents.auto_include_in_new_channels = TRUE
	//     AND agents.enabled = TRUE
	//     AND (agents.org_id = 0 OR agents.org_id = <channelOrgID>)
	// 只读 agents 一列,避免引入 agents 模块的 Go 依赖(保持 channel 单向依赖)。
	LookupAutoIncludeAgentPrincipals(ctx context.Context, channelOrgID uint64) ([]uint64, error)
}

// gormRepository Repository 的 GORM 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务,fn 接到一个事务内的 Repository;返回错误自动回滚。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
