package mcp

import (
	"context"

	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
)

// AttachmentAdapter 实现 AttachmentFacade,把 channel.service.AttachmentService 的
// by-principal 方法包给 MCP tool 用。
//
// main.go 注入:&mcp.AttachmentAdapter{AttachmentSvc: channelService.Attachment}
type AttachmentAdapter struct {
	AttachmentSvc channelsvc.AttachmentService
}

func (a *AttachmentAdapter) PresignUploadByPrincipal(ctx context.Context, in AttachmentPresignUploadByPrincipalInput) (*channelsvc.PresignedAttachmentUpload, error) {
	return a.AttachmentSvc.PresignUploadByPrincipal(ctx, channelsvc.PresignAttachmentUploadByPrincipalInput{
		ChannelID:        in.ChannelID,
		ActorPrincipalID: in.ActorPrincipalID,
		MimeType:         in.MimeType,
		Filename:         in.Filename,
	})
}

func (a *AttachmentAdapter) CommitUploadByPrincipal(ctx context.Context, in AttachmentCommitUploadByPrincipalInput) (*channelsvc.CommitAttachmentUploadOutput, error) {
	return a.AttachmentSvc.CommitUploadByPrincipal(ctx, channelsvc.CommitAttachmentUploadByPrincipalInput{
		ChannelID:        in.ChannelID,
		ActorPrincipalID: in.ActorPrincipalID,
		CommitToken:      in.CommitToken,
	})
}
