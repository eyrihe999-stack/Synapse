// agent.go Agent 资源的 repository 实现。
package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/gorm/clause"
)

// cascadeDeleteBatchSize 大表级联删除单批行数。
// 单批是 implicit tx,行数过大会持锁久、生成超长 binlog,卡住其他并发写。
const cascadeDeleteBatchSize = 1000

// batchDeleteExec 循环按 LIMIT 执行 DELETE,直到无行匹配。
// 每批一个 implicit tx,不持长锁。sql 必须是单表 DELETE 语句且不带尾部 LIMIT(由本方法附加)。
func (r *gormRepository) batchDeleteExec(ctx context.Context, sql string, args ...any) error {
	stmt := sql + " LIMIT ?"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := r.db.WithContext(ctx).Exec(stmt, append(args, cascadeDeleteBatchSize)...)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
	}
}

// CreateAgent 将新的 agent 记录写入数据库。
func (r *gormRepository) CreateAgent(ctx context.Context, agent *model.Agent) error {
	if err := r.db.WithContext(ctx).Create(agent).Error; err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

// FindAgentByID 根据主键 ID 查找 agent。
func (r *gormRepository) FindAgentByID(ctx context.Context, id uint64) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// LockAgentByID 在事务内对 agent 行加 SELECT ... FOR UPDATE 行锁。
// 用于串行化同一 agent 的并发状态变更(例如两个并发 publish 提交)。
// 必须在 WithTx 内调用,否则行锁会随单句自动释放、起不到串行化作用。
func (r *gormRepository) LockAgentByID(ctx context.Context, id uint64) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// FindAgentByOwnerSlug 根据作者 ID 和 slug 查找 agent。
func (r *gormRepository) FindAgentByOwnerSlug(ctx context.Context, ownerUserID uint64, slug string) (*model.Agent, error) {
	var a model.Agent
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ? AND slug = ?", ownerUserID, slug).
		First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAgentsByOwner 列出指定用户拥有的所有 agent,按创建时间降序排列。
func (r *gormRepository) ListAgentsByOwner(ctx context.Context, ownerUserID uint64) ([]*model.Agent, error) {
	var out []*model.Agent
	if err := r.db.WithContext(ctx).
		Where("owner_user_id = ?", ownerUserID).
		Order("created_at DESC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list agents by owner: %w", err)
	}
	return out, nil
}

// UpdateAgentFields 按字段名批量更新 agent 记录。
func (r *gormRepository) UpdateAgentFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Model(&model.Agent{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update agent fields: %w", err)
	}
	return nil
}

// DeleteAgent 根据 ID 删除 agent 记录。
func (r *gormRepository) DeleteAgent(ctx context.Context, id uint64) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.Agent{}).Error; err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

// DeletePublishesByAgent 删除指定 agent 的所有发布记录。
func (r *gormRepository) DeletePublishesByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.AgentPublish{}).Error; err != nil {
		return fmt.Errorf("delete publishes by agent: %w", err)
	}
	return nil
}

// DeleteSessionsByAgent 删除指定 agent 的所有 session 记录(分批,避免长事务)。
func (r *gormRepository) DeleteSessionsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.batchDeleteExec(ctx,
		"DELETE FROM agent_sessions WHERE agent_id = ?", agentID,
	); err != nil {
		return fmt.Errorf("delete sessions by agent: %w", err)
	}
	return nil
}

// DeleteMessagesByAgent 删除指定 agent 所有 session 下的消息(分批,避免长事务)。
// 注意:必须在 DeleteSessionsByAgent 之前调用,因为子查询依赖 agent_sessions 还存在。
func (r *gormRepository) DeleteMessagesByAgent(ctx context.Context, agentID uint64) error {
	if err := r.batchDeleteExec(ctx,
		"DELETE FROM agent_messages WHERE session_id IN (SELECT session_id FROM agent_sessions WHERE agent_id = ?)",
		agentID,
	); err != nil {
		return fmt.Errorf("delete messages by agent: %w", err)
	}
	return nil
}

// DeleteMethodsByAgent 删除指定 agent 的所有方法定义。
func (r *gormRepository) DeleteMethodsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec("DELETE FROM agent_methods WHERE agent_id = ?", agentID).Error; err != nil {
		return fmt.Errorf("delete methods by agent: %w", err)
	}
	return nil
}

// DeleteSecretsByAgent 删除指定 agent 的所有密钥记录。
func (r *gormRepository) DeleteSecretsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.db.WithContext(ctx).Exec("DELETE FROM agent_secrets WHERE agent_id = ?", agentID).Error; err != nil {
		return fmt.Errorf("delete secrets by agent: %w", err)
	}
	return nil
}

// DeleteInvocationPayloadsByAgent 删除指定 agent 所有调用的 payload 记录(分批,避免长事务)。
// 注意:必须在 DeleteInvocationsByAgent 之前调用,因为子查询依赖 agent_invocations 还存在。
func (r *gormRepository) DeleteInvocationPayloadsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.batchDeleteExec(ctx,
		"DELETE FROM agent_invocation_payloads WHERE invocation_id IN (SELECT invocation_id FROM agent_invocations WHERE agent_id = ?)",
		agentID,
	); err != nil {
		return fmt.Errorf("delete invocation payloads by agent: %w", err)
	}
	return nil
}

// DeleteInvocationsByAgent 删除指定 agent 的所有调用记录(分批,避免长事务)。
func (r *gormRepository) DeleteInvocationsByAgent(ctx context.Context, agentID uint64) error {
	if err := r.batchDeleteExec(ctx,
		"DELETE FROM agent_invocations WHERE agent_id = ?", agentID,
	); err != nil {
		return fmt.Errorf("delete invocations by agent: %w", err)
	}
	return nil
}
