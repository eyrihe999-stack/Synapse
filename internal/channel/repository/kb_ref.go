package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
)

// CreateKBRef 插入一条 channel → KB 资源关联。二选一校验(kb_source_id / kb_document_id
// 恰好一个非零)由 service 层做。
func (r *gormRepository) CreateKBRef(ctx context.Context, row *model.ChannelKBRef) error {
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("create channel kb ref: %w", err)
	}
	return nil
}

// DeleteKBRef 硬删除。生命周期在 service 层 —— channel archive 时由应用层过滤,
// 不在这层做软删除。
func (r *gormRepository) DeleteKBRef(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.ChannelKBRef{})
	if res.Error != nil {
		return fmt.Errorf("delete channel kb ref: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// FindKBRefByID 找不到返 (nil, nil)。
func (r *gormRepository) FindKBRefByID(ctx context.Context, id uint64) (*model.ChannelKBRef, error) {
	var row model.ChannelKBRef
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find channel kb ref: %w", err)
	}
	return &row, nil
}

// ListKBRefsByChannel 按 channel_id 查;简单列表,不分页(一个 channel 挂的 KB
// 数量通常小,几十个以内)。
func (r *gormRepository) ListKBRefsByChannel(ctx context.Context, channelID uint64) ([]model.ChannelKBRef, error) {
	var rows []model.ChannelKBRef
	if err := r.db.WithContext(ctx).
		Where("channel_id = ?", channelID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list channel kb refs: %w", err)
	}
	return rows, nil
}

// ListKBSourceIDsForChannel 拉 channel 当前挂载的 KB source_id 集合(非零去重)。
//
// 用途:MCP `list_kb_documents` / `get_kb_document` 校验"caller 通过 channel 能看
// 到哪些 KB documents"—— 经由 source 下属文档的可见集。直接挂 document 的另走
// ListKBDocumentIDsForChannel。
//
// 返空 slice 表示没挂任何 source 级 KB(只有直接挂 doc 的或空)。
func (r *gormRepository) ListKBSourceIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("channel_kb_refs").
		Where("channel_id = ? AND kb_source_id <> 0", channelID).
		Distinct("kb_source_id").
		Pluck("kb_source_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list channel kb source ids: %w", err)
	}
	return ids, nil
}

// ListKBDocumentIDsForChannel 拉 channel 直接挂载的 KB document_id 集合(非零去重)。
//
// 与 ListKBSourceIDsForChannel 的差别:这是"精挑细选挂某文档"的入口。
// 第一版不做"列出"侧的合并(list_kb_documents 只返 source 范围),只在 get_kb_document
// 校验权限时把直接挂的 doc_id 集纳入可见集。
func (r *gormRepository) ListKBDocumentIDsForChannel(ctx context.Context, channelID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("channel_kb_refs").
		Where("channel_id = ? AND kb_document_id <> 0", channelID).
		Distinct("kb_document_id").
		Pluck("kb_document_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list channel kb document ids: %w", err)
	}
	return ids, nil
}

// LookupAgentOwnerUserPrincipalID 反查 agent.principal_id → owner user 的 principal_id。
//
// 用途:让 caller=agent 的 list_my_mentions / dashboard 能"看到 owner user 收到的 @"。
// 真实场景里 alice 在 web 端 @ 的是 user principal,alice 用 Claude(agent principal)
// 调 MCP 应该能看到 —— 否则 inbox 永远空。
//
// 返 0 的三种情况(都用 (0, nil) 让调用方走单一逻辑):
//   - principal 不是 agent(是 user / 不存在)
//   - agent 是 system kind(owner_user_id NULL)
//   - 历史脏数据(owner_user_id 指向不存在的 user)
//
// 实现对齐 task 模块同名方法(internal/task/repository/repository.go)。
func (r *gormRepository) LookupAgentOwnerUserPrincipalID(ctx context.Context, agentPrincipalID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).Raw(`
		SELECT u.principal_id
		FROM agents a
		JOIN users u ON u.id = a.owner_user_id
		WHERE a.principal_id = ?
		  AND a.kind = 'user'
		  AND a.owner_user_id IS NOT NULL
		LIMIT 1
	`, agentPrincipalID).Scan(&pid).Error
	if err != nil {
		return 0, fmt.Errorf("lookup agent owner principal: %w", err)
	}
	return pid, nil
}

// LookupAutoIncludeAgentPrincipals 跨模块查 agents 表(不引入 agents 模块依赖)。
// 返 principal_id 列表,用于 channel 创建时自动加成员。
func (r *gormRepository) LookupAutoIncludeAgentPrincipals(ctx context.Context, channelOrgID uint64) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Table("agents").
		Where("auto_include_in_new_channels = ? AND enabled = ? AND (org_id = 0 OR org_id = ?)", true, true, channelOrgID).
		Pluck("principal_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("lookup auto-include agent principals: %w", err)
	}
	return ids, nil
}
