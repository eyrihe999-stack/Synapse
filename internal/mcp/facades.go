package mcp

import (
	"context"
	"time"

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
	taskmodel "github.com/eyrihe999-stack/Synapse/internal/task/model"
	tasksvc "github.com/eyrihe999-stack/Synapse/internal/task/service"
)

// 复用 channel.service 里定义的类型,避免 MCP 这边再起一份。
//
// 业务规则(成员校验 / 可见集合 / OSS-vs-chunks 文本来源 / 截断标志)都在 channelsvc 一侧;
// MCP 这层就是 LLM JSON ↔ Go 类型的薄壳。
type (
	// KBDocumentContent 见 channelsvc.KBDocumentContent。
	KBDocumentContent = channelsvc.KBDocumentContent
	// KBSearchHit 见 channelsvc.KBSearchHit。
	KBSearchHit = channelsvc.KBSearchHit
)

// Facade 模式:把各业务模块 service 中 "MCP tool 用得到的方法" 汇成精简接口,
// internal/mcp 只依赖这些接口不依赖具体 service struct。
//
// 方便:
//   - 单测可 mock
//   - 不引入 channel / task / kb 整包 API
//   - 接口面尽量小,阅读时能看到 tool 到底用哪些能力

// ChannelFacade MCP tool 用到的 channel 能力。
type ChannelFacade interface {
	// ListChannelsByUserPrincipal 列一个 principal 能看到的所有 channel
	// (作为 member 的 channel)。用于 list_channels tool。
	// 返回的 channel 严格按 principal 所在的 channel_members 过滤,不跨 org。
	ListChannelsByUserPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]channelmodel.Channel, error)

	// GetChannelForPrincipal 按 channel_id 查 channel + 最近 N 条消息 + mentions。
	// 权限:principal 必须是 channel 成员;否则 NotFound(不暴露存在性)。
	GetChannelForPrincipal(ctx context.Context, channelID, principalID uint64, messageLimit int) (*ChannelWithMessages, error)

	// PostMessageAsPrincipal 用 principalID 作为 author 发消息。
	// 和 HTTP 路径的 Message.Post 语义一致,只是跳过 "从 user_id 反查 principal_id" 那一步
	// (MCP 侧 principal 是 agent,不是 user)。
	// replyToMessageID=0 表示普通消息;非 0 则引用同 channel 的另一条消息(前端渲染引用卡片)。
	PostMessageAsPrincipal(ctx context.Context, channelID, authorPrincipalID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*channelmodel.ChannelMessage, []uint64, error)

	// AddReactionByPrincipal / RemoveReactionByPrincipal 给消息打 / 撤 emoji 反应(PR #12')。
	// 以 principal 为 caller 身份校验 channel membership + emoji 白名单。
	AddReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error
	RemoveReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error

	// ListChannelMembersForPrincipal 列 channel 成员及其 display_name / kind。
	// caller 必须是 channel 成员;否则 ErrForbidden。MCP list_channel_members tool 用 ——
	// agent 在 @ / 派任务前需要把"郝哥"翻译成 principal_id。
	ListChannelMembersForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]channelrepo.MemberWithProfile, error)

	// ListMyMentionsForPrincipal 跨 channel 列出 caller 被 @ 的消息(按 message_id DESC)。
	// sinceMessageID=0 → 拉最新 limit 个;>0 → 增量拉"自上次最大 message_id 之后"。
	// MCP list_my_mentions tool 用,inbox 入口必备。
	ListMyMentionsForPrincipal(ctx context.Context, callerPrincipalID, sinceMessageID uint64, limit int) ([]channelsvc.MentionItem, error)
}

// ChannelWithMessages GetChannel 的返回,含 channel 基本信息 + 最近消息 + mentions。
type ChannelWithMessages struct {
	Channel      channelmodel.Channel
	Messages     []channelmodel.ChannelMessage
	Mentions     map[uint64][]uint64 // message_id → [principal_id...]
}

// TaskFacade MCP tool 用到的 task 能力。
//
// 注意:原 HTTP 层的 tasksvc.TaskService 的方法签名接受 userID,但 MCP 路径里
// 是 agent principal。需要把 "权限校验按 principal" 的变体暴露出来。
// 第一版简化:我们复用 tasksvc.TaskService 里现有的 "以 user 视角" 的方法,
// 但通过反查 agent.owner_user_id 把 agent 转成 user 再调用(facade 实现里做)。
type TaskFacade interface {
	// ListMyTasksForPrincipal 列 caller 关心的 task。role 取值:
	//   - "" / "either":派给我的 + 我作为 reviewer 的(去重)
	//   - "assignee":只列派给我的
	//   - "reviewer":只列我作为 reviewer 的
	ListMyTasksForPrincipal(ctx context.Context, callerPrincipalID uint64, role, status string, limit, offset int) ([]taskmodel.Task, error)
	GetTaskForPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*tasksvc.TaskDetail, error)
	CreateTaskByPrincipal(ctx context.Context, in CreateTaskByPrincipalInput) (*taskmodel.Task, []uint64, error)
	ClaimTaskByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*taskmodel.Task, error)
	SubmitTaskByPrincipal(ctx context.Context, in SubmitTaskByPrincipalInput) (*taskmodel.Task, *taskmodel.TaskSubmission, error)
	ReviewTaskByPrincipal(ctx context.Context, in ReviewTaskByPrincipalInput) (*taskmodel.Task, *taskmodel.TaskReview, error)
}

// CreateTaskByPrincipalInput 和 tasksvc.CreateInput 类似,但以 principal_id 为主。
type CreateTaskByPrincipalInput struct {
	ChannelID             uint64
	CreatorPrincipalID    uint64
	Title                 string
	Description           string
	OutputSpecKind        string
	IsLightweight         bool
	AssigneePrincipalID   uint64
	ReviewerPrincipalIDs  []uint64
	RequiredApprovals     int
}

// SubmitTaskByPrincipalInput
type SubmitTaskByPrincipalInput struct {
	TaskID               uint64
	SubmitterPrincipalID uint64
	ContentKind          string
	Content              []byte
	InlineSummary        string
}

// ReviewTaskByPrincipalInput
type ReviewTaskByPrincipalInput struct {
	TaskID              uint64
	SubmissionID        uint64
	ReviewerPrincipalID uint64
	Decision            string
	Comment             string
}

// KBFacade MCP tool 用到的 KB 能力。
//
// 当前实现:
//   - list_channel_kb_refs:列 channel 挂的所有 kb_refs(source / document)
//   - list_kb_documents:列 channel 经由 source 挂载范围内的可见 KB 文档,可带 keyword(LIKE)过滤
//   - get_kb_document:按 doc_id 拉文档元数据 + 文本(text 类走 OSS 原文,二进制回退到 chunks 拼接)
//   - search_kb:语义检索(query → embedding → HNSW 近邻),返 top-K chunks
//
// 业务逻辑都在 channelsvc.KBQueryService;Facade 实现(KBAdapter)直接 delegate。
type KBFacade interface {
	// 注:list_channel_kb_refs 老方法已退役 —— channel_kb_refs 表 + per-channel
	// KB 挂载概念整体废弃。LLM 看 KB 走 list_kb_documents / get_kb_document /
	// search_kb,可见集由 channels.project_id JOIN project_kb_refs 计算。

	// ListKBDocumentsByPrincipal 列 channel 经由 source 挂载范围内的 KB 文档元数据(分页)。
	// query 为 keyword(LIKE on title/file_name),空串不过滤;beforeID 为上一页最末 doc.id。
	// 权限:caller 必须是 channel 成员;非成员返 ErrForbidden。
	// 返 Document 元数据列表,不返 chunks。要全文调 GetKBDocumentByPrincipal。
	ListKBDocumentsByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, query string, beforeID uint64, limit int) ([]*docmodel.Document, error)

	// GetKBDocumentByPrincipal 拉单文档元数据 + 文本内容。文本来源:
	//   - text/markdown / text/plain / .md / .txt → 从 OSS 拉原文(无损)
	//   - 其它(pdf/docx/二进制)→ chunks 按 idx 拼接的提取文本
	// 权限:caller 必须是 channel 成员;且 doc 必须命中 channel 可见集
	// (其 source_id 在 channel kb_refs 的 source 集 ∪ 直接挂的 doc_id 集)。
	GetKBDocumentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*KBDocumentContent, error)

	// SearchKBByPrincipal 在 channel 可见 KB 上做语义检索,返 top-K 命中(含 doc 元数据)。
	// query 空 → 返 ErrForbidden;topK ≤ 0 → 走默认值;channel 没挂任何 source / doc → 返空 hits。
	SearchKBByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, query string, topK int) ([]KBSearchHit, error)
}

// DocumentFacade MCP tool 用到的 channel 共享文档能力(PR #9' MCP 化 + PR #15' OSS 直传)。
//
// 暴露动作:创建 / 列 / 拿元数据 / 拿当前内容 / 抢锁 / inline 保存 / 释放 +
// OSS 直传(presign URL + commit)。
// 不暴露:历史版本拉取 / 强制解锁 / 心跳续锁 / 软删 —— 治理动作走 Web。
type DocumentFacade interface {
	ListByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]channelsvc.DocumentWithLock, error)
	GetByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.DocumentDetail, error)
	GetContentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.DocumentContent, error)
	CreateByPrincipal(ctx context.Context, channelID, actorPrincipalID uint64, title, contentKind string) (*channelmodel.ChannelDocument, error)
	AcquireLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) (*channelsvc.LockState, error)
	ReleaseLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) error
	SaveVersionByPrincipal(ctx context.Context, in DocumentSaveByPrincipalInput) (*channelsvc.SaveVersionOutput, error)

	// PresignUploadByPrincipal 拿 OSS 直传预签名 URL + commit token。
	// 客户端 PUT 字节到 OSS 后调 CommitUploadByPrincipal。
	// baseVersion 乐观锁:RMW 模式必传 download 时拿到的 version,空 = 跳过校验(盲写)。
	PresignUploadByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64, baseVersion string) (*channelsvc.PresignedUpload, error)
	// CommitUploadByPrincipal 通知服务端"OSS PUT 已完成,落 version 行"。
	// 服务端验 token + 持锁 + HEAD/算 sha256 + 写 DB + 发事件。
	CommitUploadByPrincipal(ctx context.Context, in DocumentCommitUploadByPrincipalInput) (*channelsvc.SaveVersionOutput, error)
	// PresignDownloadByPrincipal 拿 OSS 直拉预签名 URL,客户端 curl 直接下载到本地。
	// 配合直传形成 read-modify-write 完整闭环(零字节进 LLM context)。
	PresignDownloadByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.PresignedDownload, error)
}

// DocumentCommitUploadByPrincipalInput commit_document_upload tool 入参。
type DocumentCommitUploadByPrincipalInput struct {
	ChannelID        uint64
	DocumentID       uint64
	ActorPrincipalID uint64
	CommitToken      string
	EditSummary      string
}

// AttachmentFacade MCP tool 用到的 channel 附件能力(PR #16')。
//
// 暴露 2 个动作的"agent 上传带图片文档"闭环:
//   - PresignUpload + Commit:agent 调 request → curl PUT → commit 拿 attachment_id +
//     URL,然后把 URL 拼进 markdown body,走现有 doc upload 链路落 doc。
//
// 不暴露 list / delete / get-by-id —— 这些是治理动作,走 Web。
type AttachmentFacade interface {
	PresignUploadByPrincipal(ctx context.Context, in AttachmentPresignUploadByPrincipalInput) (*channelsvc.PresignedAttachmentUpload, error)
	CommitUploadByPrincipal(ctx context.Context, in AttachmentCommitUploadByPrincipalInput) (*channelsvc.CommitAttachmentUploadOutput, error)
}

// AttachmentPresignUploadByPrincipalInput request_channel_attachment_upload_url tool 入参。
type AttachmentPresignUploadByPrincipalInput struct {
	ChannelID        uint64
	ActorPrincipalID uint64
	MimeType         string
	Filename         string
}

// AttachmentCommitUploadByPrincipalInput commit_channel_attachment_upload tool 入参。
type AttachmentCommitUploadByPrincipalInput struct {
	ChannelID        uint64
	ActorPrincipalID uint64
	CommitToken      string
}

// DocumentSaveByPrincipalInput save_channel_document tool 入参 —— 透传到 channel.service.SaveVersionByPrincipalInput。
type DocumentSaveByPrincipalInput struct {
	ChannelID        uint64
	DocumentID       uint64
	ActorPrincipalID uint64
	Content          []byte
	EditSummary      string
}

// keep time package referenced (used by channelsvc.MentionItem.CreatedAt downstream).
var _ = time.Time{}
