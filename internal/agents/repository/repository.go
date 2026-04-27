// Package repository agents 模块数据访问层。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agents"
	"github.com/eyrihe999-stack/Synapse/internal/agents/model"
)

// Repository agent_registry 表的全部数据访问。
type Repository interface {
	// Create 插入新 agent。UNIQUE(agent_id) 冲突时包装 agents.ErrAgentInvalidRequest 返回。
	Create(ctx context.Context, a *model.Agent) error

	// FindByAgentID 按逻辑 agent_id(X-Agent-ID)查。不存在返 (nil, ErrAgentNotFound)。
	FindByAgentID(ctx context.Context, agentID string) (*model.Agent, error)

	// FindByID 按 PK 查。不存在返 (nil, ErrAgentNotFound)。
	FindByID(ctx context.Context, id uint64) (*model.Agent, error)

	// FindByOwnerAndDisplayName 按 (org_id, owner_user_id, kind='user', display_name) 查 user agent。
	// 给 OAuth consent 查重用:同一 user 通过同一 app(display_name 相同)多次授权,
	// 应该复用已有 agent,不重复创建。
	// 不存在返 (nil, ErrAgentNotFound)。
	FindByOwnerAndDisplayName(ctx context.Context, orgID, ownerUserID uint64, displayName string) (*model.Agent, error)

	// ListByOrg 按 org 列表,分页。按 created_at desc 排序。
	ListByOrg(ctx context.Context, orgID uint64, offset, limit int) ([]*model.Agent, int64, error)

	// Update 按 PK 更新 display_name / enabled / updated_at。
	// 只改 service 允许变更的字段(不改 agent_id / apikey / org_id / created_*)。
	Update(ctx context.Context, id uint64, displayName *string, enabled *bool) error

	// UpdateAPIKey rotate 时调;更新 apikey + rotated_at + updated_at。
	UpdateAPIKey(ctx context.Context, id uint64, newKey string) error

	// UpdateLastSeen handshake 成功后调,仅写 last_seen_at + updated_at。
	// 失败视为非致命(log warn 即可),不影响鉴权结果。
	UpdateLastSeen(ctx context.Context, id uint64, at time.Time) error

	// Delete 按 PK 硬删。找不到返 ErrAgentNotFound。
	Delete(ctx context.Context, id uint64) error

	// ListAutoIncludeVisibleToOrg 列出 auto_include_in_new_channels=TRUE 且对 orgID
	// 可见的所有 agent(含全局 sentinel org_id=0)。用于 channel 创建时自动挂 member
	// (PR #6' hook)。
	//
	// 封装在此是为了绕开 `agents.org_id=0` sentinel 的查询陷阱:
	// 任何"org X 可见的 agent"必须写 `org_id = ? OR org_id = 0`,裸写
	// `WHERE org_id = ?` 会让全局 agent 消失。**禁止**在 repo 外裸查 agents。
	// 详见 agents/const.go 的 GlobalAgentOrgID 注释。
	ListAutoIncludeVisibleToOrg(ctx context.Context, orgID uint64) ([]*model.Agent, error)
}

// gormRepository 基于 GORM 的 Repository 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// Create 插入。UNIQUE 冲突映射为 ErrAgentInvalidRequest(v1 agent_id 系统生成,冲突实际不会发生;留兜底)。
func (r *gormRepository) Create(ctx context.Context, a *model.Agent) error {
	if err := r.db.WithContext(ctx).Create(a).Error; err != nil {
		// 不细分唯一键冲突 —— 系统生成的 agent_id 冲突概率 ≈ 0;真冲突的话当内部错误处理。
		return fmt.Errorf("create agent: %w: %w", err, agents.ErrAgentInternal)
	}
	return nil
}

func (r *gormRepository) FindByAgentID(ctx context.Context, agentID string) (*model.Agent, error) {
	var a model.Agent
	err := r.db.WithContext(ctx).Where("agent_id = ?", agentID).First(&a).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agents.ErrAgentNotFound
		}
		return nil, fmt.Errorf("find by agent_id: %w: %w", err, agents.ErrAgentInternal)
	}
	return &a, nil
}

// FindByOwnerAndDisplayName 见接口注释。只查 kind='user' 的 agent(system agent 不该走此路径)。
func (r *gormRepository) FindByOwnerAndDisplayName(ctx context.Context, orgID, ownerUserID uint64, displayName string) (*model.Agent, error) {
	var a model.Agent
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND owner_user_id = ? AND kind = ? AND display_name = ?",
			orgID, ownerUserID, agents.KindUser, displayName).
		First(&a).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agents.ErrAgentNotFound
		}
		return nil, fmt.Errorf("find by owner+display_name: %w: %w", err, agents.ErrAgentInternal)
	}
	return &a, nil
}

func (r *gormRepository) FindByID(ctx context.Context, id uint64) (*model.Agent, error) {
	var a model.Agent
	err := r.db.WithContext(ctx).First(&a, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agents.ErrAgentNotFound
		}
		return nil, fmt.Errorf("find by id: %w: %w", err, agents.ErrAgentInternal)
	}
	return &a, nil
}

// ListByOrg 列出对该 org 可见的 agents:
//   - 该 org 自己的 agent(org_id = orgID)
//   - 全局 sentinel agent(org_id = 0,如 synapse-top-orchestrator)—— 每个 org 都可见
//
// sentinel 约定:agents.org_id=0 表示"对所有 org 可见"的全局 agent。遗漏 `OR org_id=0`
// 会让前端(@mention picker / 成员显示)把顶级 agent 当成 unknown principal 展示。
// 详见 agents/const.go 里 GlobalAgentOrgID 的护栏注释。
func (r *gormRepository) ListByOrg(ctx context.Context, orgID uint64, offset, limit int) ([]*model.Agent, int64, error) {
	var (
		rows  []*model.Agent
		total int64
	)
	q := r.db.WithContext(ctx).Model(&model.Agent{}).
		Where("(org_id = ? OR org_id = ?)", orgID, agents.GlobalAgentOrgID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count by org: %w: %w", err, agents.ErrAgentInternal)
	}
	if err := q.Order("created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list by org: %w: %w", err, agents.ErrAgentInternal)
	}
	return rows, total, nil
}

func (r *gormRepository) Update(ctx context.Context, id uint64, displayName *string, enabled *bool) error {
	updates := map[string]any{"updated_at": time.Now()}
	if displayName != nil {
		updates["display_name"] = *displayName
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if len(updates) == 1 {
		return nil // 只有 updated_at,业务上相当于 no-op
	}
	res := r.db.WithContext(ctx).Model(&model.Agent{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update: %w: %w", res.Error, agents.ErrAgentInternal)
	}
	if res.RowsAffected == 0 {
		return agents.ErrAgentNotFound
	}
	return nil
}

func (r *gormRepository) UpdateAPIKey(ctx context.Context, id uint64, newKey string) error {
	now := time.Now()
	// GORM 把 APIKey 字段映射成 column api_key(snake_case)。
	// 早期这里写成 "apikey" 导致 Updates 更新一个不存在的列 → 500。
	res := r.db.WithContext(ctx).Model(&model.Agent{}).Where("id = ?", id).Updates(map[string]any{
		"api_key":    newKey,
		"rotated_at": now,
		"updated_at": now,
	})
	if res.Error != nil {
		return fmt.Errorf("update apikey: %w: %w", res.Error, agents.ErrAgentInternal)
	}
	if res.RowsAffected == 0 {
		return agents.ErrAgentNotFound
	}
	return nil
}

func (r *gormRepository) UpdateLastSeen(ctx context.Context, id uint64, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&model.Agent{}).Where("id = ?", id).Updates(map[string]any{
		"last_seen_at": at,
		"updated_at":   at,
	})
	if res.Error != nil {
		return fmt.Errorf("update last_seen: %w: %w", res.Error, agents.ErrAgentInternal)
	}
	return nil // RowsAffected=0 不算错(agent 可能被删了,但这不是 handshake 的致命问题)
}

// Delete 硬删 agent + 级联清理其 principal_id 在其它模块留下的引用:
//   - oauth_access_tokens / oauth_refresh_tokens —— 清了 Claude Desktop 等客户端的 token 立即失效
//   - channel_members —— 否则 UI 会显示"principal#N 未知身份"
//   - task_reviewers —— 曾被任命为审批人的记录删(task_reviews 历史保留)
//   - tasks.assignee_principal_id —— 把活跃任务的 assignee 清零(task 本身不删),让创建人看到并重新指派
//
// 跨模块做 raw SQL 清理 —— 未引入跨模块 Go 依赖。一个事务保证原子。
//
// 终态任务(approved/rejected/cancelled)的 assignee 不动 —— 保留审计完整性。
func (r *gormRepository) Delete(ctx context.Context, id uint64) error {
	// 先拿 principal_id,给级联清理用
	var a model.Agent
	if err := r.db.WithContext(ctx).Select("id", "principal_id").First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return agents.ErrAgentNotFound
		}
		return fmt.Errorf("load agent for delete: %w: %w", err, agents.ErrAgentInternal)
	}
	pid := a.PrincipalID

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if pid > 0 {
			// 清 OAuth tokens —— Claude Desktop / Cursor 等客户端下次调用会拿到 401
			if err := tx.Exec("DELETE FROM oauth_access_tokens WHERE agent_id = ?", pid).Error; err != nil {
				return fmt.Errorf("cleanup oauth_access_tokens: %w", err)
			}
			if err := tx.Exec("DELETE FROM oauth_refresh_tokens WHERE agent_id = ?", pid).Error; err != nil {
				return fmt.Errorf("cleanup oauth_refresh_tokens: %w", err)
			}
			// 清 user_pats —— 这个 agent 关联的所有 PAT 一并删,避免悬挂引用
			// (否则 PAT 仍能通过 BearerAuth 但 inject 出的 agent_principal_id 指向已删 agent,
			// 下游 channel/task 操作会因 member 校验失败而拒,呈"假死"状态)。
			if err := tx.Exec("DELETE FROM user_pats WHERE agent_id = ?", pid).Error; err != nil {
				return fmt.Errorf("cleanup user_pats: %w", err)
			}
			// 清 channel_members —— 让 UI 不再显示 principal#N 未知身份
			if err := tx.Exec("DELETE FROM channel_members WHERE principal_id = ?", pid).Error; err != nil {
				return fmt.Errorf("cleanup channel_members: %w", err)
			}
			// 清 task_reviewers —— 审批人不再存在,需要 creator 重配
			if err := tx.Exec("DELETE FROM task_reviewers WHERE principal_id = ?", pid).Error; err != nil {
				return fmt.Errorf("cleanup task_reviewers: %w", err)
			}
			// 活跃任务的 assignee 清零(status 降回 open)。终态任务不动。
			if err := tx.Exec(`
				UPDATE tasks SET assignee_principal_id = 0, status = 'open'
				WHERE assignee_principal_id = ?
				  AND status NOT IN ('approved','rejected','cancelled')
			`, pid).Error; err != nil {
				return fmt.Errorf("cleanup tasks.assignee: %w", err)
			}
		}
		// 最后删 agent 行
		res := tx.Delete(&model.Agent{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete agent row: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return agents.ErrAgentNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			return err
		}
		return fmt.Errorf("delete with cleanup: %w: %w", err, agents.ErrAgentInternal)
	}
	return nil
}

// ListAutoIncludeVisibleToOrg 实现接口的同名方法。
//
// 只取 enabled=TRUE(被 disable 的不再自动入新 channel),按 created_at 升序 ——
// 老的 agent(含全局 orchestrator)先加入,保证稳定排序。
func (r *gormRepository) ListAutoIncludeVisibleToOrg(ctx context.Context, orgID uint64) ([]*model.Agent, error) {
	var rows []*model.Agent
	err := r.db.WithContext(ctx).
		Where("auto_include_in_new_channels = ? AND enabled = ? AND (org_id = ? OR org_id = ?)",
			true, true, orgID, agents.GlobalAgentOrgID).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list auto-include agents visible to org %d: %w: %w", orgID, err, agents.ErrAgentInternal)
	}
	return rows, nil
}
