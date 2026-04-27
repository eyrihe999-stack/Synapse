package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// MessageService channel 消息子领域业务接口。
//
// 权限模型:发消息 / 读消息必须是 channel 成员(role 不限 owner/member/observer)。
// Observer 也能读写 —— 跨团队围观的人如果进来能看不能说反而反常。要禁言的话走
// "主动移除 observer" 或未来加 role=readonly(不在 MVP)。
//
// 系统消息(kind='system_event'):只有 service 层内部产生(channel 创建 /
// 归档 / 成员加入等的自动打点),HTTP / MCP 入口都硬拒 —— 不允许客户端伪造系统
// 事件。
type MessageService interface {
	// Post HTTP 路径:由 user JWT 发消息(反查 user.principal_id)。
	// replyToMessageID=0 表示普通消息;非 0 则本条是对该 id 的回复(前端引用卡片用)。
	Post(ctx context.Context, channelID, authorUserID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*PostedMessage, error)

	// PostAsPrincipal MCP 路径:直接用 principal 发消息(typically agent principal)。
	// 和 Post 的差异:不做 user_id → principal_id 反查;调用方给的 principal 必须是 channel 成员。
	// replyToMessageID 同上。
	PostAsPrincipal(ctx context.Context, channelID, authorPrincipalID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*PostedMessage, error)

	// PostSystemEvent 专为 "channel-event-card-writer" consumer 写一条 kind=system_event
	// 消息。和 Post/PostAsPrincipal 的关键差别:
	//   - **跳过** archived channel guard —— `channel.archived` 事件本身就要作为最后一条
	//     system_event 落在刚刚被归档的 channel 里
	//   - **跳过** channel member guard —— actor 可能已从 channel 被移除(member_removed
	//     事件写入时,actor 仍是"刚才的那一下操作者",哪怕 DB 里已删行)
	//   - **不 publish** message.posted 事件(不进入新的 consumer 循环)
	//   - 用 sourceEventID UNIQUE 做幂等:consumer 重放同一 Redis event 只写一条
	//
	// 入参:body 必须是已序列化的 JSON(event body 约定);sourceEventID 非空。
	// 返回:幂等场景下可能返回历史已写入的那条 message,不视为错误。
	PostSystemEvent(ctx context.Context, channelID, authorPrincipalID uint64, bodyJSON, sourceEventID string) (*PostedMessage, error)

	// List HTTP 路径(反查 user)。
	List(ctx context.Context, channelID, callerUserID uint64, beforeID uint64, limit int) ([]MessageWithMentions, error)

	// ListForPrincipal MCP 路径(直接用 principal 校验成员)。
	ListForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, beforeID uint64, limit int) ([]MessageWithMentions, error)

	// AddReaction 给消息打一个 emoji 反应。HTTP 路径(反查 user → principal)。
	//
	// 校验:caller 必须是 channel 成员;emoji 必须在 AllowedReactionEmojis 白名单;
	// 归档 channel 不允许新增反应(语义和"归档后不写消息"一致)。
	// 幂等:同 (message, principal, emoji) 已存在直接返回成功。
	AddReaction(ctx context.Context, messageID, callerUserID uint64, emoji string) error
	// RemoveReaction 撤销反应。HTTP 路径。不存在视为幂等成功。
	RemoveReaction(ctx context.Context, messageID, callerUserID uint64, emoji string) error
	// AddReactionByPrincipal MCP 路径,agent 允许打反应。
	AddReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error
	// RemoveReactionByPrincipal MCP 路径。
	RemoveReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error

	// ListMyMentionsByPrincipal 跨 channel 列出 caller 被 @ 的消息(按 message_id DESC)。
	//
	// sinceMessageID=0 → 拉最新 limit 个;>0 → 拉 message_id > since 的(增量"自上次后")。
	// 不做 channel 状态过滤(归档 channel 内的 mention 也保留在结果里 —— inbox 回顾语义)。
	// 不做 channel 成员校验:被 @ 的人本来就该在那个 channel(否则 sanitize 阶段就拦了);
	// 退一步即便后来被踢出,这条 mention 仍是历史事实。
	ListMyMentionsByPrincipal(ctx context.Context, callerPrincipalID, sinceMessageID uint64, limit int) ([]MentionItem, error)
}

// MentionItem 跨 channel 列 mention 的一项 —— message + channel 元数据扁平化。
//
// MCP `list_my_mentions` 直接序列化此结构;前端 inbox 列表也复用同 shape。
type MentionItem struct {
	MessageID         uint64
	ChannelID         uint64
	AuthorPrincipalID uint64
	Body              string
	Kind              string
	CreatedAt         time.Time
}

// PostedMessage Post 返回 —— 包含新建的 message 和它的 mentions 列表。
type PostedMessage struct {
	Message  *model.ChannelMessage
	Mentions []uint64 // principal_id 列表;在 DB 里已去重 / 已校验
}

// ReplyPreview 给前端渲染引用卡片用的精简预览:作者 + 正文前若干字。
// 当目标消息查不到(被删除等罕见情况)时,调用方填 Missing=true,前端显示"原消息已不存在"。
type ReplyPreview struct {
	MessageID         uint64 `json:"message_id"`
	AuthorPrincipalID uint64 `json:"author_principal_id"`
	BodySnippet       string `json:"body_snippet"`
	Missing           bool   `json:"missing"`
}

// ReactionEntry 聚合返给前端的反应条目:一个 emoji 对应一组打了它的 principal_id。
// 同 emoji 多人合并,前端按 display_name 渲染"👍 Alice, Bob"。
type ReactionEntry struct {
	Emoji        string   `json:"emoji"`
	PrincipalIDs []uint64 `json:"principal_ids"`
}

// MessageWithMentions List 返回的一项。
// ReplyPreview 可空:只在 message.ReplyToMessageID 非 nil 时填充。
// Reactions 可空:无反应时为 nil。
type MessageWithMentions struct {
	Message      model.ChannelMessage
	Mentions     []uint64
	ReplyPreview *ReplyPreview
	Reactions    []ReactionEntry
}

// replyPreviewSnippetMax reply 预览正文最多保留多少字符(按 rune 截断,保证中文完整)。
const replyPreviewSnippetMax = 160

type messageService struct {
	repo       repository.Repository
	publisher  eventbus.Publisher // 可 nil;nil 时跳过事件发布
	streamKey  string             // synapse:channel:events
	logger     logger.LoggerInterface
}

// newMessageService 构造 MessageService。publisher 可 nil(单测 / 尚未接 eventbus 场景);
// streamKey 非空才会尝试发事件,二者任一缺失都降级为"只写 DB 不广播"。
func newMessageService(
	repo repository.Repository,
	publisher eventbus.Publisher,
	streamKey string,
	log logger.LoggerInterface,
) MessageService {
	return &messageService{
		repo:      repo,
		publisher: publisher,
		streamKey: streamKey,
		logger:    log,
	}
}

// Post HTTP 路径:反查 user → principal,再调 postCore。
func (s *messageService) Post(ctx context.Context, channelID, authorUserID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*PostedMessage, error) {
	authorPrincipalID, err := s.lookupUserPrincipalID(ctx, authorUserID)
	if err != nil {
		return nil, err
	}
	return s.postCore(ctx, channelID, authorPrincipalID, body, mentionPrincipalIDs, replyToMessageID)
}

// PostAsPrincipal MCP 路径:直接用 principal(通常是 agent)。
func (s *messageService) PostAsPrincipal(ctx context.Context, channelID, authorPrincipalID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*PostedMessage, error) {
	if authorPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	return s.postCore(ctx, channelID, authorPrincipalID, body, mentionPrincipalIDs, replyToMessageID)
}

// PostSystemEvent 见接口注释。仅内部 consumer 使用,跳过 archived / member guard。
func (s *messageService) PostSystemEvent(ctx context.Context, channelID, authorPrincipalID uint64, bodyJSON, sourceEventID string) (*PostedMessage, error) {
	if sourceEventID == "" {
		return nil, fmt.Errorf("source_event_id required: %w", chanerr.ErrMessageBodyInvalid)
	}
	if bodyJSON == "" || len(bodyJSON) > chanerr.MessageBodyMaxLen {
		return nil, chanerr.ErrMessageBodyInvalid
	}
	if authorPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}

	// 幂等前检:同一 source_event_id 已写过 → 直接返回历史那条,视作成功
	if existing, err := s.repo.FindMessageBySourceEventID(ctx, sourceEventID); err != nil {
		return nil, fmt.Errorf("lookup source_event: %w: %w", err, chanerr.ErrChannelInternal)
	} else if existing != nil {
		return &PostedMessage{Message: existing}, nil
	}

	// channel 不存在直接失败(消息没地方落);archived 不拦
	c, err := s.repo.FindChannelByID(ctx, channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c == nil {
		return nil, chanerr.ErrChannelNotFound
	}

	src := sourceEventID
	m := &model.ChannelMessage{
		ChannelID:         channelID,
		AuthorPrincipalID: authorPrincipalID,
		Body:              bodyJSON,
		Kind:              chanerr.MessageKindSystemEvent,
		SourceEventID:     &src,
	}
	if err := s.repo.CreateMessage(ctx, m); err != nil {
		// UNIQUE 冲突:consumer 并发写同一 event(不同副本) —— 返回已有那条
		if isDuplicateKeyError(err) {
			if existing, qerr := s.repo.FindMessageBySourceEventID(ctx, sourceEventID); qerr == nil && existing != nil {
				return &PostedMessage{Message: existing}, nil
			}
		}
		return nil, fmt.Errorf("create system_event message: %w: %w", err, chanerr.ErrChannelInternal)
	}
	// 不 publish message.posted —— 避免再次触发本 consumer,产生死循环
	return &PostedMessage{Message: m}, nil
}

// ── Reactions(PR #12')──────────────────────────────────────────────────

// AddReaction HTTP 路径;反查 user → principal 后调 core。
func (s *messageService) AddReaction(ctx context.Context, messageID, callerUserID uint64, emoji string) error {
	pid, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return err
	}
	return s.addReactionCore(ctx, messageID, pid, emoji)
}

// RemoveReaction HTTP 路径。
func (s *messageService) RemoveReaction(ctx context.Context, messageID, callerUserID uint64, emoji string) error {
	pid, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return err
	}
	return s.removeReactionCore(ctx, messageID, pid, emoji)
}

// AddReactionByPrincipal MCP 路径,agent 允许打反应。
func (s *messageService) AddReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error {
	if callerPrincipalID == 0 {
		return chanerr.ErrForbidden
	}
	return s.addReactionCore(ctx, messageID, callerPrincipalID, emoji)
}

// RemoveReactionByPrincipal MCP 路径。
func (s *messageService) RemoveReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error {
	if callerPrincipalID == 0 {
		return chanerr.ErrForbidden
	}
	return s.removeReactionCore(ctx, messageID, callerPrincipalID, emoji)
}

// addReactionCore 共享实现:emoji 白名单 + 消息存在 + channel 未归档 + caller 是 channel 成员。
// 幂等:同 (message, principal, emoji) 已存在返 nil(重复 Add 不报错)。
func (s *messageService) addReactionCore(ctx context.Context, messageID, callerPID uint64, emoji string) error {
	if !chanerr.IsValidReactionEmoji(emoji) {
		return chanerr.ErrReactionEmojiInvalid
	}
	// 查消息拿 channel_id;同时校验存在性
	m, err := s.repo.FindMessageByID(ctx, messageID)
	if err != nil {
		return fmt.Errorf("find message: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if m == nil {
		return chanerr.ErrMessageReplyTargetNotFound // 复用"消息不存在"错误
	}
	// channel 状态:归档拒
	c, err := s.repo.FindChannelByID(ctx, m.ChannelID)
	if err != nil {
		return fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c == nil {
		return chanerr.ErrChannelNotFound
	}
	if c.Status == chanerr.ChannelStatusArchived {
		return chanerr.ErrChannelArchived
	}
	// 权限:caller 必须是 channel 成员
	if err := s.requireChannelMember(ctx, m.ChannelID, callerPID); err != nil {
		return err
	}
	row := &model.ChannelMessageReaction{
		MessageID:   messageID,
		PrincipalID: callerPID,
		Emoji:       emoji,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.repo.AddReaction(ctx, row); err != nil {
		if isDuplicateKeyError(err) {
			return nil // 幂等成功
		}
		return fmt.Errorf("add reaction: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return nil
}

// removeReactionCore 共享实现:消息存在 + channel 成员即可(归档 channel 允许撤销,防数据卡死)。
// 幂等:不存在视为成功。
func (s *messageService) removeReactionCore(ctx context.Context, messageID, callerPID uint64, emoji string) error {
	if !chanerr.IsValidReactionEmoji(emoji) {
		return chanerr.ErrReactionEmojiInvalid
	}
	m, err := s.repo.FindMessageByID(ctx, messageID)
	if err != nil {
		return fmt.Errorf("find message: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if m == nil {
		return chanerr.ErrMessageReplyTargetNotFound
	}
	if err := s.requireChannelMember(ctx, m.ChannelID, callerPID); err != nil {
		return err
	}
	if err := s.repo.RemoveReaction(ctx, messageID, callerPID, emoji); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 幂等成功
		}
		return fmt.Errorf("remove reaction: %w: %w", err, chanerr.ErrChannelInternal)
	}
	return nil
}

// isDuplicateKeyError 粗略判断 MySQL 1062 Duplicate entry。和 task 模块同款,后续 common 化。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") || strings.Contains(msg, "Duplicate entry")
}

// postCore Post / PostAsPrincipal 共享逻辑。
//
// 事务性:INSERT message + INSERT mentions 同成功或同失败;事件 XADD 在事务
// 提交之后 best-effort,失败仅 warn(DB 是真相源)。
//
// replyToMessageID 非 0 时校验:目标消息必须存在且属于同 channel —— 阻断"引用
// 其它 channel 消息"造成的信息泄露。
func (s *messageService) postCore(ctx context.Context, channelID, authorPrincipalID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*PostedMessage, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > chanerr.MessageBodyMaxLen {
		return nil, chanerr.ErrMessageBodyInvalid
	}

	// 拿 channel —— 需要其状态 + org_id(事件 fields 要带 org)
	c, err := s.repo.FindChannelByID(ctx, channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if c == nil {
		return nil, chanerr.ErrChannelNotFound
	}
	if c.Status == chanerr.ChannelStatusArchived {
		return nil, chanerr.ErrChannelArchived
	}

	// 权限:author 必须是 channel 成员
	if err := s.requireChannelMember(ctx, channelID, authorPrincipalID); err != nil {
		return nil, err
	}

	// Mentions 去重 + 校验:每个 mention 必须是本 channel 成员
	sanitized, err := s.sanitizeMentions(ctx, channelID, mentionPrincipalIDs)
	if err != nil {
		return nil, err
	}

	// Reply target 校验(非 0 才查)
	var replyToPtr *uint64
	if replyToMessageID != 0 {
		if _, err := s.repo.FindMessageInChannel(ctx, channelID, replyToMessageID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, chanerr.ErrMessageReplyTargetNotFound
			}
			return nil, fmt.Errorf("find reply target: %w: %w", err, chanerr.ErrChannelInternal)
		}
		rid := replyToMessageID
		replyToPtr = &rid
	}

	var created model.ChannelMessage
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		m := &model.ChannelMessage{
			ChannelID:         channelID,
			AuthorPrincipalID: authorPrincipalID,
			Body:              body,
			Kind:              chanerr.MessageKindText,
			ReplyToMessageID:  replyToPtr,
		}
		if err := tx.CreateMessage(ctx, m); err != nil {
			return err
		}
		if err := tx.AddMessageMentions(ctx, m.ID, sanitized); err != nil {
			return err
		}
		created = *m
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create message tx: %w: %w", err, chanerr.ErrChannelInternal)
	}

	s.publishMessagePosted(ctx, c, &created, sanitized)

	return &PostedMessage{Message: &created, Mentions: sanitized}, nil
}

// List HTTP 路径:反查 user → principal,再调 listCore。
func (s *messageService) List(ctx context.Context, channelID, callerUserID uint64, beforeID uint64, limit int) ([]MessageWithMentions, error) {
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	return s.listCore(ctx, channelID, callerPrincipalID, beforeID, limit)
}

// ListForPrincipal MCP 路径:直接用 principal。
func (s *messageService) ListForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64, beforeID uint64, limit int) ([]MessageWithMentions, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	return s.listCore(ctx, channelID, callerPrincipalID, beforeID, limit)
}

// listCore List / ListForPrincipal 共享逻辑。
func (s *messageService) listCore(ctx context.Context, channelID, callerPrincipalID uint64, beforeID uint64, limit int) ([]MessageWithMentions, error) {
	if limit <= 0 {
		limit = chanerr.MessageListDefaultLimit
	}
	if limit > chanerr.MessageListMaxLimit {
		limit = chanerr.MessageListMaxLimit
	}

	// 校验 channel 存在 + caller 是成员
	if _, err := s.repo.FindChannelByID(ctx, channelID); errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, chanerr.ErrChannelNotFound
	} else if err != nil {
		return nil, fmt.Errorf("find channel: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if err := s.requireChannelMember(ctx, channelID, callerPrincipalID); err != nil {
		return nil, err
	}

	msgs, err := s.repo.ListMessages(ctx, channelID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	ids := make([]uint64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	mentions, err := s.repo.ListMentionsByMessages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list mentions: %w: %w", err, chanerr.ErrChannelInternal)
	}

	// bucket mentions by message_id
	byMsg := make(map[uint64][]uint64, len(msgs))
	for _, mn := range mentions {
		byMsg[mn.MessageID] = append(byMsg[mn.MessageID], mn.PrincipalID)
	}

	// 批取 reply preview —— 先收集非空 reply_to_message_id,再一次 IN 查询。
	replyPreviews, err := s.buildReplyPreviews(ctx, channelID, msgs)
	if err != nil {
		return nil, err
	}

	// 批取 reactions —— 一次 IN 查询,按 (message_id, emoji) 聚合成 ReactionEntry
	reactionRows, err := s.repo.ListReactionsByMessages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list reactions: %w: %w", err, chanerr.ErrChannelInternal)
	}
	reactionsByMsg := aggregateReactions(reactionRows)

	out := make([]MessageWithMentions, len(msgs))
	for i, m := range msgs {
		item := MessageWithMentions{
			Message:   m,
			Mentions:  byMsg[m.ID],
			Reactions: reactionsByMsg[m.ID],
		}
		if m.ReplyToMessageID != nil {
			if prev, ok := replyPreviews[*m.ReplyToMessageID]; ok {
				p := prev
				item.ReplyPreview = &p
			} else {
				// 目标消息在本 channel 查不到 —— 给个 Missing 预览,前端展示"原消息已不存在"
				item.ReplyPreview = &ReplyPreview{MessageID: *m.ReplyToMessageID, Missing: true}
			}
		}
		out[i] = item
	}
	return out, nil
}

// aggregateReactions 把扁平 rows 聚合成 map[message_id] -> []ReactionEntry。
// 同 message + 同 emoji 的 principal_ids 合并。emoji 顺序按首次出现稳定。
func aggregateReactions(rows []model.ChannelMessageReaction) map[uint64][]ReactionEntry {
	if len(rows) == 0 {
		return nil
	}
	// message_id -> emoji 首次出现顺序
	order := make(map[uint64][]string)
	// (message_id, emoji) -> principal_ids
	bucket := make(map[uint64]map[string][]uint64)
	for _, r := range rows {
		if _, ok := bucket[r.MessageID]; !ok {
			bucket[r.MessageID] = make(map[string][]uint64)
		}
		if _, seen := bucket[r.MessageID][r.Emoji]; !seen {
			order[r.MessageID] = append(order[r.MessageID], r.Emoji)
		}
		bucket[r.MessageID][r.Emoji] = append(bucket[r.MessageID][r.Emoji], r.PrincipalID)
	}
	out := make(map[uint64][]ReactionEntry, len(bucket))
	for msgID, emojis := range order {
		entries := make([]ReactionEntry, 0, len(emojis))
		for _, e := range emojis {
			entries = append(entries, ReactionEntry{Emoji: e, PrincipalIDs: bucket[msgID][e]})
		}
		out[msgID] = entries
	}
	return out
}

// buildReplyPreviews 扫一遍 msgs 收 reply_to_message_id,去重后一次 IN 查询回 preview map。
// 注:为什么按 channelID 限定 —— 校验一致,纵然 service 层写入时已经校验同 channel,
// 读侧再加一层屏障(防有人直接写 DB 埋跨 channel 引用)。
func (s *messageService) buildReplyPreviews(ctx context.Context, channelID uint64, msgs []model.ChannelMessage) (map[uint64]ReplyPreview, error) {
	var ids []uint64
	seen := make(map[uint64]struct{})
	for _, m := range msgs {
		if m.ReplyToMessageID == nil {
			continue
		}
		id := *m.ReplyToMessageID
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.repo.FindMessagesByIDsInChannel(ctx, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("find reply preview targets: %w: %w", err, chanerr.ErrChannelInternal)
	}
	out := make(map[uint64]ReplyPreview, len(rows))
	for _, r := range rows {
		out[r.ID] = ReplyPreview{
			MessageID:         r.ID,
			AuthorPrincipalID: r.AuthorPrincipalID,
			BodySnippet:       truncateRunes(r.Body, replyPreviewSnippetMax),
		}
	}
	return out, nil
}

// truncateRunes 按 UTF-8 rune 截断 —— 避免中文被切一半。超限尾加 "…"。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// requireChannelMember 校验 principal 是指定 channel 的成员;否则返 ErrForbidden。
func (s *messageService) requireChannelMember(ctx context.Context, channelID, principalID uint64) error {
	mem, err := s.repo.FindMember(ctx, channelID, principalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return chanerr.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("find channel member: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if mem == nil {
		return chanerr.ErrForbidden
	}
	return nil
}

// sanitizeMentions 去重 + 校验 mentions;每个 id 必须是本 channel 成员。
// 返 principal_id 列表(去重,保留输入顺序中首次出现)。
func (s *messageService) sanitizeMentions(ctx context.Context, channelID uint64, raw []uint64) ([]uint64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[uint64]struct{}, len(raw))
	out := make([]uint64, 0, len(raw))
	for _, pid := range raw {
		if pid == 0 {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		mem, err := s.repo.FindMember(ctx, channelID, pid)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, chanerr.ErrMessageMentionNotInChannel
		}
		if err != nil {
			return nil, fmt.Errorf("find member for mention: %w: %w", err, chanerr.ErrChannelInternal)
		}
		if mem == nil {
			return nil, chanerr.ErrMessageMentionNotInChannel
		}
		out = append(out, pid)
	}
	return out, nil
}

// publishMessagePosted 把 message.posted 事件 XADD 到 synapse:channel:events。
// publisher 为 nil / streamKey 空 / XADD 失败都只 warn,不影响 DB 事务结果。
func (s *messageService) publishMessagePosted(ctx context.Context, c *model.Channel, m *model.ChannelMessage, mentions []uint64) {
	if s.publisher == nil || s.streamKey == "" {
		return
	}
	fields := map[string]any{
		"event_type":          "message.posted",
		"org_id":              strconv.FormatUint(c.OrgID, 10),
		"channel_id":          strconv.FormatUint(c.ID, 10),
		"message_id":          strconv.FormatUint(m.ID, 10),
		"author_principal_id": strconv.FormatUint(m.AuthorPrincipalID, 10),
		"kind":                m.Kind,
		"created_at":          m.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if len(mentions) > 0 {
		// 逗号分隔,消费端 strings.Split 即可。不做 JSON 是因为 stream fields
		// 原生就是 string,平铺小字段比 JSON 好消费。
		ids := make([]string, len(mentions))
		for i, pid := range mentions {
			ids[i] = strconv.FormatUint(pid, 10)
		}
		fields["mentioned_principal_ids"] = strings.Join(ids, ",")
	}
	id, err := s.publisher.Publish(ctx, s.streamKey, fields)
	if err != nil {
		s.logger.WarnCtx(ctx, "channel: publish message.posted event failed", map[string]any{
			"channel_id": c.ID, "message_id": m.ID, "err": err.Error(),
		})
		return
	}
	s.logger.DebugCtx(ctx, "channel: published message.posted", map[string]any{
		"channel_id": c.ID, "message_id": m.ID, "stream_id": id,
	})
}

// ListMyMentionsByPrincipal 见接口注释。
//
// 实现要点:caller 是 user-agent 时,自动把 owner user 的 principal_id 加进
// candidates(类似 task 模块的 expandCallerCandidates)—— 真实场景里别人 @ 的是
// user principal,agent 调本方法应能看到自己的 owner 收到的 @,否则 inbox 永远空。
//
// system agent / user 直接登录(无 owner)走单 principal 查询,无副作用。
func (s *messageService) ListMyMentionsByPrincipal(ctx context.Context, callerPrincipalID, sinceMessageID uint64, limit int) ([]MentionItem, error) {
	if callerPrincipalID == 0 {
		return nil, chanerr.ErrForbidden
	}
	if limit <= 0 {
		limit = chanerr.MessageListDefaultLimit
	}
	if limit > chanerr.MessageListMaxLimit {
		limit = chanerr.MessageListMaxLimit
	}
	candidates := []uint64{callerPrincipalID}
	if ownerPID, err := s.repo.LookupAgentOwnerUserPrincipalID(ctx, callerPrincipalID); err != nil {
		return nil, fmt.Errorf("lookup owner principal: %w: %w", err, chanerr.ErrChannelInternal)
	} else if ownerPID != 0 && ownerPID != callerPrincipalID {
		candidates = append(candidates, ownerPID)
	}
	rows, err := s.repo.ListMentionsByPrincipals(ctx, candidates, sinceMessageID, limit)
	if err != nil {
		return nil, fmt.Errorf("list mentions by principals: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]MentionItem, len(rows))
	for i, r := range rows {
		out[i] = MentionItem{
			MessageID:         r.MessageID,
			ChannelID:         r.ChannelID,
			AuthorPrincipalID: r.AuthorPrincipalID,
			Body:              r.Body,
			Kind:              r.Kind,
			CreatedAt:         r.CreatedAt,
		}
	}
	return out, nil
}

// lookupUserPrincipalID 反查 user 的 principal_id。
// 和 channel_service 里的同名函数职责一样,但我们走 repo 入口避免跨 sub-service 直接引用。
func (s *messageService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	pid, err := s.repo.LookupUserPrincipalID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, chanerr.ErrForbidden
		}
		return 0, fmt.Errorf("lookup user principal: %w: %w", err, chanerr.ErrChannelInternal)
	}
	if pid == 0 {
		return 0, chanerr.ErrForbidden
	}
	return pid, nil
}
