package repository

import (
	"context"
	"fmt"
)

// kb_visibility.go channel 视角下"我能看到哪些 KB"的查询。
//
// 数据源:**project 级 project_kb_refs 表**(由 pm 模块管;不再读已废弃的
// channel_kb_refs)。语义升级:channel 所属 project 挂载的所有 KB → 该 channel
// 视角下都可见。
//
// 调用方:channel.service.KBQueryService(给 list_kb_documents / get_kb_document /
// search_kb 这三个 MCP tool 计算可见集)。

// ListKBSourceIDsForChannel 返 channel 视角下可见的 KB source_id 集合(非零去重)。
//
// 走 channels.project_id JOIN project_kb_refs.project_id —— 任何 channel(包括
// project_console / workstream / regular)都映射到所属 project 的 KB 挂载范围。
//
// 返空 slice 表示项目下没挂任何 source 级 KB。
func (r *gormRepository) ListKBSourceIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT pk.kb_source_id
		FROM project_kb_refs pk
		INNER JOIN channels c ON c.project_id = pk.project_id
		WHERE c.id = ? AND pk.kb_source_id <> 0
	`, channelID).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list kb source ids for channel via project: %w", err)
	}
	return ids, nil
}

// ListKBDocumentIDsForChannel 返 channel 视角下可见的、直接挂载的 KB document_id 集合。
//
// 与 ListKBSourceIDsForChannel 同义但只列直接挂 doc(精挑路径)。
func (r *gormRepository) ListKBDocumentIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT pk.kb_document_id
		FROM project_kb_refs pk
		INNER JOIN channels c ON c.project_id = pk.project_id
		WHERE c.id = ? AND pk.kb_document_id <> 0
	`, channelID).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list kb document ids for channel via project: %w", err)
	}
	return ids, nil
}
