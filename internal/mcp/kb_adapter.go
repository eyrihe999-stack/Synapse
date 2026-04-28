package mcp

import (
	"context"

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
//
// 历史:list_channel_kb_refs 老 tool + KBRefService 已退役(channel_kb_refs 表
// + per-channel KB 挂载概念整体废弃),改由 pm.ProjectKBRefService 在 project 维度管理。
type KBAdapter struct {
	KBQuerySvc channelsvc.KBQueryService
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
