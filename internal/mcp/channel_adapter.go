package mcp

import (
	"context"

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
)

// ChannelAdapter 把 channel.service 的 "by principal" 方法包成 ChannelFacade。
// main.go 注入:&mcp.ChannelAdapter{
//     ChannelSvc: channelService.Channel,
//     MessageSvc: channelService.Message,
//     MemberSvc:  channelService.Member,
// }
type ChannelAdapter struct {
	ChannelSvc channelsvc.ChannelService
	MessageSvc channelsvc.MessageService
	MemberSvc  channelsvc.MemberService
}

// ListChannelsByUserPrincipal 直接走 ChannelService.ListByPrincipal。
func (a *ChannelAdapter) ListChannelsByUserPrincipal(ctx context.Context, principalID uint64, limit, offset int) ([]channelmodel.Channel, error) {
	return a.ChannelSvc.ListByPrincipal(ctx, principalID, limit, offset)
}

// GetChannelForPrincipal:拿 channel 基本信息 + 近 N 条消息 + mentions 打包返回。
func (a *ChannelAdapter) GetChannelForPrincipal(ctx context.Context, channelID, principalID uint64, messageLimit int) (*ChannelWithMessages, error) {
	c, err := a.ChannelSvc.Get(ctx, channelID)
	if err != nil {
		return nil, err
	}
	// 用 ListForPrincipal(已做 channel member 校验 + 消息分页 + mentions 批取)
	items, err := a.MessageSvc.ListForPrincipal(ctx, channelID, principalID, 0, messageLimit)
	if err != nil {
		return nil, err
	}
	msgs := make([]channelmodel.ChannelMessage, 0, len(items))
	mentions := make(map[uint64][]uint64, len(items))
	for _, it := range items {
		msgs = append(msgs, it.Message)
		if len(it.Mentions) > 0 {
			mentions[it.Message.ID] = it.Mentions
		}
	}
	return &ChannelWithMessages{Channel: *c, Messages: msgs, Mentions: mentions}, nil
}

// PostMessageAsPrincipal MessageService.PostAsPrincipal 直接包。
func (a *ChannelAdapter) PostMessageAsPrincipal(ctx context.Context, channelID, authorPrincipalID uint64, body string, mentionPrincipalIDs []uint64, replyToMessageID uint64) (*channelmodel.ChannelMessage, []uint64, error) {
	posted, err := a.MessageSvc.PostAsPrincipal(ctx, channelID, authorPrincipalID, body, mentionPrincipalIDs, replyToMessageID)
	if err != nil {
		return nil, nil, err
	}
	return posted.Message, posted.Mentions, nil
}

// AddReactionByPrincipal / RemoveReactionByPrincipal 透传到 MessageService(PR #12')。
// agent 被允许打 reaction,tool 层直接暴露。
func (a *ChannelAdapter) AddReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error {
	return a.MessageSvc.AddReactionByPrincipal(ctx, messageID, callerPrincipalID, emoji)
}
func (a *ChannelAdapter) RemoveReactionByPrincipal(ctx context.Context, messageID, callerPrincipalID uint64, emoji string) error {
	return a.MessageSvc.RemoveReactionByPrincipal(ctx, messageID, callerPrincipalID, emoji)
}

// ListChannelMembersForPrincipal 透传到 MemberService.ListWithProfileByPrincipal。
// caller 必须是 channel 成员;返回成员的 principal_id + display_name + kind +
// is_global_agent 用于 LLM 选派任务 / @ 同事时翻译显示名。
func (a *ChannelAdapter) ListChannelMembersForPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]channelrepo.MemberWithProfile, error) {
	return a.MemberSvc.ListWithProfileByPrincipal(ctx, channelID, callerPrincipalID)
}

// ListMyMentionsForPrincipal 透传到 MessageService.ListMyMentionsByPrincipal —— 跨
// channel 列 caller 被 @ 的消息;list_my_mentions tool 用,inbox 入口必备。
func (a *ChannelAdapter) ListMyMentionsForPrincipal(ctx context.Context, callerPrincipalID, sinceMessageID uint64, limit int) ([]channelsvc.MentionItem, error) {
	return a.MessageSvc.ListMyMentionsByPrincipal(ctx, callerPrincipalID, sinceMessageID, limit)
}
