// repository.go agent 模块统一的 Repository 接口定义与事务封装。
//
// 设计:
//   - 单一 Repository interface,按资源分组(agent/method/secret/publish/invocation)
//   - WithTx 提供事务入口,事务内返回 tx-bound Repository
//   - 方法实现分散在同包的 agent.go / method.go / secret.go / publish.go /
//     invocation.go 里,按 receiver 绑定到 gormRepository
package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/gorm"
)

// AgentWithAuthor 聚合返回值:agent + 作者 user 的关键展示字段。
type AgentWithAuthor struct {
	Agent       *model.Agent
	AuthorName  string
	AuthorAvatar string
}

// Repository agent 模块数据访问入口。
//sayso-lint:ignore interface-pollution
type Repository interface {
	// ─── 事务 ──────────────────────────────────────────────────────────────

	// WithTx 在事务内执行 fn,事务内所有 repo 调用共享同一个 tx。
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── Agent ────────────────────────────────────────────────────────────

	// CreateAgent 创建一条 agent 记录。
	CreateAgent(ctx context.Context, agent *model.Agent) error
	// FindAgentByID 按 ID 查 agent。
	FindAgentByID(ctx context.Context, id uint64) (*model.Agent, error)
	// FindAgentByOwnerSlug 按 (owner_user_id, slug) 查 agent。
	FindAgentByOwnerSlug(ctx context.Context, ownerUserID uint64, slug string) (*model.Agent, error)
	// ListAgentsByOwner 列出某作者的所有 agent。
	ListAgentsByOwner(ctx context.Context, ownerUserID uint64) ([]*model.Agent, error)
	// ListAgentsByIDs 批量按 ID 查 agent。
	ListAgentsByIDs(ctx context.Context, ids []uint64) ([]*model.Agent, error)
	// UpdateAgentFields 部分更新 agent 字段。
	UpdateAgentFields(ctx context.Context, id uint64, updates map[string]any) error
	// DeleteAgent 删除 agent(级联 method/secret/publish 由 service 层显式处理)。
	DeleteAgent(ctx context.Context, id uint64) error
	// ListActiveAgentsForHealthCheck 列出健康检查需要的所有 agent(status=active)。
	ListActiveAgentsForHealthCheck(ctx context.Context, limit int) ([]*model.Agent, error)

	// ─── Method ───────────────────────────────────────────────────────────

	// CreateMethod 创建一条 method。
	CreateMethod(ctx context.Context, m *model.AgentMethod) error
	// CreateMethodsBatch 批量创建(agent 首次注册时用)。
	CreateMethodsBatch(ctx context.Context, methods []*model.AgentMethod) error
	// FindMethodByID 按 ID 查 method。
	FindMethodByID(ctx context.Context, id uint64) (*model.AgentMethod, error)
	// FindMethodByAgentName 按 (agent_id, method_name) 查 method。
	FindMethodByAgentName(ctx context.Context, agentID uint64, methodName string) (*model.AgentMethod, error)
	// ListMethodsByAgent 列出某 agent 的全部 method。
	ListMethodsByAgent(ctx context.Context, agentID uint64) ([]*model.AgentMethod, error)
	// CountMethodsByAgent 统计 method 数量(删除时检查至少保留 1 个)。
	CountMethodsByAgent(ctx context.Context, agentID uint64) (int64, error)
	// UpdateMethodFields 部分更新 method。
	UpdateMethodFields(ctx context.Context, id uint64, updates map[string]any) error
	// DeleteMethod 删除 method。
	DeleteMethod(ctx context.Context, id uint64) error
	// DeleteMethodsByAgent 删除 agent 下所有 method(删除 agent 时级联)。
	DeleteMethodsByAgent(ctx context.Context, agentID uint64) error

	// ─── Secret ───────────────────────────────────────────────────────────

	// CreateSecret 创建 secret 记录。
	CreateSecret(ctx context.Context, s *model.AgentSecret) error
	// FindSecretByAgent 按 agent_id 查 secret。
	FindSecretByAgent(ctx context.Context, agentID uint64) (*model.AgentSecret, error)
	// UpdateSecret 替换 secret(rotate),传入完整字段。
	UpdateSecret(ctx context.Context, agentID uint64, updates map[string]any) error
	// DeleteSecretByAgent 删除 secret(agent 删除时级联)。
	DeleteSecretByAgent(ctx context.Context, agentID uint64) error

	// ─── Publish ──────────────────────────────────────────────────────────

	// CreatePublish 创建发布记录。
	CreatePublish(ctx context.Context, p *model.AgentPublish) error
	// FindPublishByID 按 ID 查 publish。
	FindPublishByID(ctx context.Context, id uint64) (*model.AgentPublish, error)
	// FindActivePublish 查 (agent_id, org_id, status in (pending, approved)) 的唯一记录。
	FindActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error)
	// ListPublishesByOrg 分页列出某 org 的 publish(status 过滤可选)。
	ListPublishesByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]*model.AgentPublish, int64, error)
	// ListActivePublishesByAgent 列出某 agent 所有 active 绑定(status in approved/pending)。
	ListActivePublishesByAgent(ctx context.Context, agentID uint64) ([]*model.AgentPublish, error)
	// ListActivePublishesByAuthorOrg 查 (org_id, submitted_by_user_id, status=approved|pending)。
	// 供成员离开 / org 解散 hook 使用。
	ListActivePublishesByAuthorOrg(ctx context.Context, orgID, authorUserID uint64) ([]*model.AgentPublish, error)
	// UpdatePublishFields 部分更新 publish。
	UpdatePublishFields(ctx context.Context, id uint64, updates map[string]any) error
	// RevokePublishesByAuthorOrg 批量标记 (org_id, author_user_id) 所有 active publish 为 revoked。
	// 成员离开 org 时调用。返回受影响行数。
	RevokePublishesByAuthorOrg(ctx context.Context, orgID, authorUserID uint64, reason string, now time.Time) (int64, error)
	// RevokePublishesByOrg 批量标记 org 下所有 active publish 为 revoked(org 解散时)。
	RevokePublishesByOrg(ctx context.Context, orgID uint64, reason string, now time.Time) (int64, error)
	// ListAgentIDsByOrg 列出某 org 内所有 approved 的 agent_id(用于列 agent 接口)。
	ListAgentIDsByOrg(ctx context.Context, orgID uint64) ([]uint64, error)

	// ─── Invocation(分区表,只读和异步写) ────────────────────────────

	// InsertInvocation 插入 invocation 主表(Begin 阶段同步写)。
	InsertInvocation(ctx context.Context, inv *model.AgentInvocation) error
	// UpdateInvocationByID 按 invocation_id 更新 finished/status/error 等字段。
	UpdateInvocationByID(ctx context.Context, invocationID string, startedAt time.Time, updates map[string]any) error
	// FindInvocationByID 按 invocation_id 查 invocation 主表(取消接口/审计详情用)。
	// 需要同时带 startedAt 做分区裁剪。startedAt 未知时传 zero,走全表查询。
	FindInvocationByID(ctx context.Context, invocationID string) (*model.AgentInvocation, error)
	// ListInvocationsByOrg 分页列出某 org 的 invocation(时间倒序)。
	ListInvocationsByOrg(ctx context.Context, orgID uint64, filter InvocationFilter, page, size int) ([]*model.AgentInvocation, int64, error)
	// InsertInvocationPayload 异步写 payload 表。
	InsertInvocationPayload(ctx context.Context, p *model.AgentInvocationPayload) error
	// FindInvocationPayload 按 invocation_id 查 payload(审计详情用)。
	FindInvocationPayload(ctx context.Context, invocationID string) (*model.AgentInvocationPayload, error)
}

// InvocationFilter 审计查询过滤条件。
type InvocationFilter struct {
	// CallerUserID 仅返回调用者为此用户的 invocation(0 表示不过滤)
	CallerUserID uint64
	// AgentOwnerUserID 仅返回 agent 作者为此用户的 invocation
	AgentOwnerUserID uint64
	// AgentID 仅返回特定 agent
	AgentID uint64
	// StartedAfter 起始时间过滤(用于分区裁剪)
	StartedAfter time.Time
	// StartedBefore 截止时间过滤
	StartedBefore time.Time
}

// gormRepository GORM 实现。具体资源方法在同包其他文件里。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	//sayso-lint:ignore log-coverage
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
