// Package channel 提供 project / version / channel / channel_members / channel_versions
// 五张基础表 + CRUD。所有表都是 collaboration Phase 1 §3.9 定义的"协作工作空间骨架"。
//
// 权限模型:project / channel 的顶层权限靠 org membership(复用 organization 模块的
// IsMember);channel 内部权限靠 channel_members.role(owner / member / observer)。
package channel

import "time"

// Channel 成员角色。
const (
	MemberRoleOwner    = "owner"
	MemberRoleMember   = "member"
	MemberRoleObserver = "observer"
)

// Channel 状态。
const (
	ChannelStatusOpen     = "open"
	ChannelStatusArchived = "archived"
)

// Version 状态。
const (
	VersionStatusPlanned    = "planned"
	VersionStatusInProgress = "in_progress"
	VersionStatusReleased   = "released"
	VersionStatusCancelled  = "cancelled"
)

// 列表默认分页。
const (
	ListDefaultLimit = 50
	ListMaxLimit     = 200
)

// FieldSizes 把和 model tag 对应的长度集中声明,service 层校验输入用。
const (
	NameMaxLen        = 128
	DescriptionMaxLen = 512
	PurposeMaxLen     = 512
	VersionNameMaxLen = 64
	RoleMaxLen        = 16
	MessageBodyMaxLen = 16 * 1024 // 16KB markdown body,一次发太长应拆;大于此值走 KB 上传
)

// Message kinds。
const (
	MessageKindText        = "text"         // 人 / agent 发的正常 markdown 消息
	MessageKindSystemEvent = "system_event" // 系统自动生成(channel 建 / 归档 / 成员加入等)
)

// IsValidMessageKind 校验 message kind 是否合法。
func IsValidMessageKind(k string) bool {
	return k == MessageKindText || k == MessageKindSystemEvent
}

// 消息拉取分页默认上限。
const (
	MessageListDefaultLimit = 50
	MessageListMaxLimit     = 100
)

// AllowedReactionEmojis PR #12' 预设 emoji 白名单。MVP 不支持全 Unicode 也不支持
// 用户自定义 —— 前端 UI 紧凑,后端防垃圾数据。
// 长度上限 16 字符(复合 emoji 如 👨‍👩‍👧 是 4 个 rune 可能占 10+ 字节,但预设集合都 ≤ 6 字节)。
var AllowedReactionEmojis = []string{
	"👍", "👎", "❤️", "🎉", "🚀", "👀",
	"🙏", "😂", "🔥", "✅", "❌", "🤔",
}

// ReactionEmojiMaxLen DB 列长度约束;预设最长 6 字节,留余量。
const ReactionEmojiMaxLen = 16

// IsValidReactionEmoji 校验 emoji 是否在预设白名单内。
func IsValidReactionEmoji(e string) bool {
	for _, allowed := range AllowedReactionEmojis {
		if e == allowed {
			return true
		}
	}
	return false
}

// MinChannelArchiveGrace 归档后自动释放 artifact 的最小宽限时长(Phase 2 用)。
// 此刻没启用 —— 仅作占位避免 magic number。
const MinChannelArchiveGrace = 7 * 24 * time.Hour

// ─── Channel 共享文档(PR #9') ──────────────────────────────────────────────

// ChannelDocument 内容类型。MVP 只支持纯文本族;富文本 / 图片走 message 上传或后续 PR。
const (
	ChannelDocumentKindMarkdown = "md"
	ChannelDocumentKindText     = "text"
)

// IsValidChannelDocumentKind 校验 content_kind 是否合法。
func IsValidChannelDocumentKind(k string) bool {
	return k == ChannelDocumentKindMarkdown || k == ChannelDocumentKindText
}

// ChannelDocumentTitleMaxLen 共享文档标题长度上限,和 channel.name 对齐。
const ChannelDocumentTitleMaxLen = 128

// ChannelDocumentMaxByteSize 单版本字节数上限。md/text 不该超过这个;前端在编辑时
// 应给 warning,后端硬拒。10MB 对齐全局 multipart body 上限。
const ChannelDocumentMaxByteSize = 10 << 20

// ChannelDocumentEditSummaryMaxLen 单次保存的备注字段长度。
const ChannelDocumentEditSummaryMaxLen = 255

// ChannelDocumentLockTTL 编辑锁单次有效时长;心跳每 60s 一次,客户端断网/关页面
// 后最迟 10min 锁过期,其他人能强制抢。
const ChannelDocumentLockTTL = 10 * time.Minute

// ─── Channel 附件(图片等富媒体,Markdown 内嵌引用)──────────────────────────

// ChannelAttachmentMaxByteSize 单个附件字节上限。10MB 与 ChannelDocumentMaxByteSize
// 对齐;前端编辑时应给 warning,后端硬拒。
const ChannelAttachmentMaxByteSize = 10 << 20

// ChannelAttachmentFilenameMaxLen 上传时 filename 字段长度上限(空允许)。
const ChannelAttachmentFilenameMaxLen = 256

// AllowedAttachmentMimeTypes 第一版允许的图片 MIME 白名单。
//
// 不含 image/svg+xml — SVG 内嵌 markdown 渲染走 <img> 在大多浏览器是安全的(脚本被 sandbox),
// 但跨浏览器边界 case 多(老 IE / Safari ImageIO bug 等),且 SVG 文件易被滥作 XSS 载体。
// 默认拒,等真有需求再单独走"沙箱化 SVG renderer"路径。
var AllowedAttachmentMimeTypes = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp",
}

// IsValidAttachmentMimeType 校验 MIME 是否在白名单内。
func IsValidAttachmentMimeType(m string) bool {
	for _, allowed := range AllowedAttachmentMimeTypes {
		if m == allowed {
			return true
		}
	}
	return false
}

// AttachmentDownloadCacheMaxAge 浏览器对 attachment GET 响应的缓存秒数。
//
// 5min 内同一图反复出现(如同一文档反复滚动)直接命中浏览器缓存,不再请求 server。
// 不设太长是因为 attachment 软删后旧缓存仍然能 render(用户体感一致;治理意义不大)。
const AttachmentDownloadCacheMaxAge = 300
