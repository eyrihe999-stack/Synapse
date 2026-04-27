// errors.go channel 模块错误码与哨兵错误变量定义。
//
// 错误码格式 HHHSSCCCC:
//   - HHH:HTTP 状态码
//   - SS:模块号 27 = channel(前面已用 01/11/13/15/17/19/21/23/25)
//   - CCCC:业务码
//
// 业务错误统一以 HTTP 200 + body 业务码返回(对齐 user / organization 风格);
// 仅 ErrChannelInternal 返 500。
package channel

import "errors"

// ─── 400 段:请求 / 业务校验 ─────────────────────────────────────────────────

const (
	// CodeChannelInvalidRequest 请求参数无效(泛用)
	CodeChannelInvalidRequest = 400270010

	// CodeProjectNameInvalid project.name 长度不合法
	CodeProjectNameInvalid = 400270020
	// CodeProjectNameDuplicated project.name 在该 org 下已有未归档项目使用
	CodeProjectNameDuplicated = 409270021
	// CodeProjectArchived project 已归档,不允许改 / 加子实体
	CodeProjectArchived = 400270022

	// CodeVersionNameInvalid version.name 长度不合法
	CodeVersionNameInvalid = 400270030
	// CodeVersionNameDuplicated 同 project 下 version.name 重复
	CodeVersionNameDuplicated = 409270031
	// CodeVersionStatusInvalid version.status 枚举非法
	CodeVersionStatusInvalid = 400270032

	// CodeChannelNameInvalid channel.name 长度不合法
	CodeChannelNameInvalid = 400270040
	// CodeChannelArchived channel 已归档,不允许改 / 加成员
	CodeChannelArchived = 400270041

	// CodeMemberRoleInvalid channel_members.role 枚举非法
	CodeMemberRoleInvalid = 400270050
	// CodeMemberLastOwner 当前是 channel 里最后一个 owner,不能被移除 / 降级
	CodeMemberLastOwner = 400270051
	// CodeMemberAlreadyExists principal 已经是该 channel 成员
	CodeMemberAlreadyExists = 409270052
	// CodePrincipalNotInOrg 目标 principal 不在 channel 所属 org 里,不能加入
	CodePrincipalNotInOrg = 400270053

	// CodeMessageBodyInvalid body 空或超长
	CodeMessageBodyInvalid = 400270060
	// CodeMessageKindInvalid kind 字段非法
	CodeMessageKindInvalid = 400270061
	// CodeMessageMentionNotInChannel mention 列表里有 principal 不是本 channel 成员
	CodeMessageMentionNotInChannel = 400270062
	// CodeMessageReplyTargetNotFound reply_to_message_id 指向的消息不存在或不在同 channel
	CodeMessageReplyTargetNotFound = 400270063

	// CodeKBRefInvalid kb_source_id / kb_document_id 二选一约束不满足
	CodeKBRefInvalid = 400270070

	// CodeReactionEmojiInvalid emoji 不在预设白名单 AllowedReactionEmojis 里
	CodeReactionEmojiInvalid = 400270080

	// CodeChannelDocumentTitleInvalid 共享文档标题空或超长
	CodeChannelDocumentTitleInvalid = 400270090
	// CodeChannelDocumentKindInvalid content_kind 不是 md/text
	CodeChannelDocumentKindInvalid = 400270091
	// CodeChannelDocumentContentTooLarge 单版本字节数超过 ChannelDocumentMaxByteSize
	CodeChannelDocumentContentTooLarge = 400270092
	// CodeChannelDocumentContentEmpty 上传 body 空
	CodeChannelDocumentContentEmpty = 400270093
	// CodeChannelDocumentLockHeld 抢锁失败:已被其他 principal 持有
	CodeChannelDocumentLockHeld = 409270094
	// CodeChannelDocumentLockNotHeld 调用方没持有锁却尝试续锁/释放/保存
	CodeChannelDocumentLockNotHeld = 400270095
	// CodeChannelDocumentVersionNotFound 指定 version_id 不存在或不属于该文档
	CodeChannelDocumentVersionNotFound = 404270096
	// CodeChannelDocumentNotFound 共享文档不存在或已软删
	CodeChannelDocumentNotFound = 404270097
	// CodeChannelDocumentUploadTokenInvalid OSS 直传 commit token 签名无效或解析失败
	CodeChannelDocumentUploadTokenInvalid = 400270098
	// CodeChannelDocumentUploadTokenExpired OSS 直传 commit token 已过期(>5min)
	CodeChannelDocumentUploadTokenExpired = 400270099
	// CodeChannelDocumentBaseVersionStale 乐观锁失败:RMW 期间 doc 已被他人提交
	// 新版,client 拿的 base_version 不再是 current_version。client 应 re-download
	// 重做修改后再 commit。
	CodeChannelDocumentBaseVersionStale = 409270100

	// ─── Channel 附件 ───────────────────────────────────────────────────────
	// CodeChannelAttachmentMimeInvalid MIME 不在 AllowedAttachmentMimeTypes 白名单
	CodeChannelAttachmentMimeInvalid = 400270110
	// CodeChannelAttachmentTooLarge 超过 ChannelAttachmentMaxByteSize
	CodeChannelAttachmentTooLarge = 400270111
	// CodeChannelAttachmentEmpty commit 时 OSS 对象 0 字节(client 没真正 PUT)
	CodeChannelAttachmentEmpty = 400270112
	// CodeChannelAttachmentNotFound attachment 不存在 / 已软删 / 不属于 channel
	CodeChannelAttachmentNotFound = 404270113
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

const (
	// CodeForbidden 调用者无权限执行该操作
	CodeForbidden = 403270010
)

// ─── 404 段:资源 ─────────────────────────────────────────────────────────────

const (
	// CodeProjectNotFound
	CodeProjectNotFound = 404270010
	// CodeVersionNotFound
	CodeVersionNotFound = 404270020
	// CodeChannelNotFound
	CodeChannelNotFound = 404270030
	// CodeMemberNotFound 指定的 channel_members 记录不存在
	CodeMemberNotFound = 404270040
	// CodeKBRefNotFound
	CodeKBRefNotFound = 404270050
)

// ─── 500 段 ─────────────────────────────────────────────────────────────────

const (
	// CodeChannelInternal 服务内部错误
	CodeChannelInternal = 500270010
)

// ─── 哨兵错误 ───────────────────────────────────────────────────────────────

var (
	ErrChannelInternal = errors.New("channel: internal error")

	ErrProjectNotFound     = errors.New("channel: project not found")
	ErrProjectNameInvalid  = errors.New("channel: project name invalid")
	ErrProjectNameDup      = errors.New("channel: project name duplicated")
	ErrProjectArchived     = errors.New("channel: project archived")

	ErrVersionNotFound      = errors.New("channel: version not found")
	ErrVersionNameInvalid   = errors.New("channel: version name invalid")
	ErrVersionNameDup       = errors.New("channel: version name duplicated")
	ErrVersionStatusInvalid = errors.New("channel: version status invalid")

	ErrChannelNotFound    = errors.New("channel: channel not found")
	ErrChannelNameInvalid = errors.New("channel: channel name invalid")
	ErrChannelArchived    = errors.New("channel: channel archived")

	ErrMemberRoleInvalid   = errors.New("channel: member role invalid")
	ErrMemberLastOwner     = errors.New("channel: last owner cannot be removed or demoted")
	ErrMemberAlreadyExists = errors.New("channel: member already exists")
	ErrMemberNotFound      = errors.New("channel: member not found")
	ErrPrincipalNotInOrg   = errors.New("channel: principal not in org")

	ErrMessageBodyInvalid         = errors.New("channel: message body invalid")
	ErrMessageKindInvalid         = errors.New("channel: message kind invalid")
	ErrMessageMentionNotInChannel = errors.New("channel: mention principal not in channel")
	ErrMessageReplyTargetNotFound = errors.New("channel: reply target message not found in this channel")

	ErrKBRefInvalid  = errors.New("channel: kb ref invalid")
	ErrKBRefNotFound = errors.New("channel: kb ref not found")

	ErrReactionEmojiInvalid = errors.New("channel: reaction emoji not in allowed set")

	ErrChannelDocumentTitleInvalid     = errors.New("channel: shared document title invalid")
	ErrChannelDocumentKindInvalid      = errors.New("channel: shared document content_kind invalid")
	ErrChannelDocumentContentTooLarge  = errors.New("channel: shared document content too large")
	ErrChannelDocumentContentEmpty     = errors.New("channel: shared document content empty")
	ErrChannelDocumentLockHeld         = errors.New("channel: shared document lock held by another principal")
	ErrChannelDocumentLockNotHeld      = errors.New("channel: shared document lock not held by caller")
	ErrChannelDocumentVersionNotFound  = errors.New("channel: shared document version not found")
	ErrChannelDocumentNotFound         = errors.New("channel: shared document not found")
	ErrChannelDocumentBaseVersionStale = errors.New("channel: document base_version stale, re-download and retry")

	ErrChannelAttachmentMimeInvalid = errors.New("channel: attachment mime type not allowed")
	ErrChannelAttachmentTooLarge    = errors.New("channel: attachment too large")
	ErrChannelAttachmentEmpty       = errors.New("channel: attachment content empty")
	ErrChannelAttachmentNotFound    = errors.New("channel: attachment not found")

	ErrForbidden = errors.New("channel: forbidden")
)
