package mcp

import (
	"context"

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
)

// KBAdapter 实现 KBFacade。
//
// 设计原则:**这层是 LLM JSON ↔ Go 类型的薄壳**,不放任何业务规则。
//
// 职责拆分:
//   - 成员校验 / channel 可见集合判定 / OSS-vs-chunks 文本来源 / 检索逻辑
//     → 全部在 channelsvc.KBQueryService(下沉到 channel 模块的 service 层,系统 agent 也用)
//   - list_channel_kb_refs 仍直接 delegate 到 channelsvc.KBRefService.ListForPrincipal
//     (它本身就只做"列挂载关系",和"读 KB 内容"是两件事;不强行融到 KBQueryService)
type KBAdapter struct {
	KBRefSvc   channelsvc.KBRefService
	KBQuerySvc channelsvc.KBQueryService
}

// ListChannelKBRefsForPrincipal 直接 delegate KBRefService。
func (a *KBAdapter) ListChannelKBRefsForPrincipal(ctx context.Context, channelID, principalID uint64) ([]channelmodel.ChannelKBRef, error) {
	return a.KBRefSvc.ListForPrincipal(ctx, channelID, principalID)
}

// ListKBDocumentsByPrincipal delegate 到 KBQueryService。
func (a *KBAdapter) ListKBDocumentsByPrincipal(
	ctx context.Context,
	channelID, callerPrincipalID uint64,
	query string,
	beforeID uint64,
	limit int,
) ([]*docmodel.Document, error) {
	return a.KBQuerySvc.ListDocumentsByPrincipal(ctx, channelID, callerPrincipalID, query, beforeID, limit)
}

// GetKBDocumentByPrincipal delegate 到 KBQueryService。
//
// 返回的 *KBDocumentContent 是 channelsvc.KBDocumentContent 的别名,
// FullTextSource ("oss" | "chunks_join") + Truncated 字段由 service 决定。
func (a *KBAdapter) GetKBDocumentByPrincipal(
	ctx context.Context,
	channelID, docID, callerPrincipalID uint64,
) (*KBDocumentContent, error) {
	return a.KBQuerySvc.GetDocumentByPrincipal(ctx, channelID, docID, callerPrincipalID)
}

// SearchKBByPrincipal delegate 到 KBQueryService。
func (a *KBAdapter) SearchKBByPrincipal(
	ctx context.Context,
	channelID, callerPrincipalID uint64,
	query string,
	topK int,
) ([]KBSearchHit, error) {
	return a.KBQuerySvc.SearchByPrincipal(ctx, channelID, callerPrincipalID, query, topK)
}
