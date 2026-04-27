package mcp

import (
	"context"

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
)

// DocumentAdapter 实现 DocumentFacade,把 channel.service.DocumentService 的 by-principal
// 方法包给 MCP tool 用。
//
// main.go 注入:&mcp.DocumentAdapter{DocSvc: channelService.Document}
type DocumentAdapter struct {
	DocSvc channelsvc.DocumentService
}

func (a *DocumentAdapter) ListByPrincipal(ctx context.Context, channelID, callerPrincipalID uint64) ([]channelsvc.DocumentWithLock, error) {
	return a.DocSvc.ListByPrincipal(ctx, channelID, callerPrincipalID)
}

func (a *DocumentAdapter) GetByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.DocumentDetail, error) {
	return a.DocSvc.GetByPrincipal(ctx, channelID, docID, callerPrincipalID)
}

func (a *DocumentAdapter) GetContentByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.DocumentContent, error) {
	return a.DocSvc.GetContentByPrincipal(ctx, channelID, docID, callerPrincipalID)
}

func (a *DocumentAdapter) CreateByPrincipal(ctx context.Context, channelID, actorPrincipalID uint64, title, contentKind string) (*channelmodel.ChannelDocument, error) {
	return a.DocSvc.CreateByPrincipal(ctx, channelID, actorPrincipalID, title, contentKind)
}

func (a *DocumentAdapter) AcquireLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) (*channelsvc.LockState, error) {
	return a.DocSvc.AcquireLockByPrincipal(ctx, channelID, docID, actorPrincipalID)
}

func (a *DocumentAdapter) ReleaseLockByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64) error {
	return a.DocSvc.ReleaseLockByPrincipal(ctx, channelID, docID, actorPrincipalID)
}

func (a *DocumentAdapter) SaveVersionByPrincipal(ctx context.Context, in DocumentSaveByPrincipalInput) (*channelsvc.SaveVersionOutput, error) {
	return a.DocSvc.SaveVersionByPrincipal(ctx, channelsvc.SaveVersionByPrincipalInput{
		ChannelID:        in.ChannelID,
		DocumentID:       in.DocumentID,
		ActorPrincipalID: in.ActorPrincipalID,
		Content:          in.Content,
		EditSummary:      in.EditSummary,
	})
}

func (a *DocumentAdapter) PresignUploadByPrincipal(ctx context.Context, channelID, docID, actorPrincipalID uint64, baseVersion string) (*channelsvc.PresignedUpload, error) {
	return a.DocSvc.PresignUploadByPrincipal(ctx, channelID, docID, actorPrincipalID, baseVersion)
}

func (a *DocumentAdapter) CommitUploadByPrincipal(ctx context.Context, in DocumentCommitUploadByPrincipalInput) (*channelsvc.SaveVersionOutput, error) {
	return a.DocSvc.CommitUploadByPrincipal(ctx, channelsvc.CommitUploadByPrincipalInput{
		ChannelID:        in.ChannelID,
		DocumentID:       in.DocumentID,
		ActorPrincipalID: in.ActorPrincipalID,
		CommitToken:      in.CommitToken,
		EditSummary:      in.EditSummary,
	})
}

func (a *DocumentAdapter) PresignDownloadByPrincipal(ctx context.Context, channelID, docID, callerPrincipalID uint64) (*channelsvc.PresignedDownload, error) {
	return a.DocSvc.PresignDownloadByPrincipal(ctx, channelID, docID, callerPrincipalID)
}
