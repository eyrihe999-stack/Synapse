// repository.go agent 模块统一的 Repository 接口定义与事务封装。
package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/agent/model"
	"gorm.io/gorm"
)

// Repository agent 模块数据访问入口。
type Repository interface {
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── Agent ────────────────────────────────────────────────────────────
	CreateAgent(ctx context.Context, agent *model.Agent) error
	FindAgentByID(ctx context.Context, id uint64) (*model.Agent, error)
	// LockAgentByID 在事务内对 agent 行加 SELECT ... FOR UPDATE 行锁,
	// 用于串行化同一 agent 的并发状态变更(如发布提交)。必须在 WithTx 里调用。
	LockAgentByID(ctx context.Context, id uint64) (*model.Agent, error)
	FindAgentByOwnerSlug(ctx context.Context, ownerUserID uint64, slug string) (*model.Agent, error)
	ListAgentsByOwner(ctx context.Context, ownerUserID uint64) ([]*model.Agent, error)
	UpdateAgentFields(ctx context.Context, id uint64, updates map[string]any) error
	DeleteAgent(ctx context.Context, id uint64) error

	// ─── Session ──────────────────────────────────────────────────────────
	CreateSession(ctx context.Context, s *model.AgentSession) error
	FindSessionByID(ctx context.Context, sessionID string) (*model.AgentSession, error)
	ListSessionsByUserAgent(ctx context.Context, orgID, userID, agentID uint64, page, size int) ([]*model.AgentSession, int64, error)
	UpdateSessionTitle(ctx context.Context, sessionID string, title string) error
	DeleteSession(ctx context.Context, sessionID string) error

	// ─── Message ──────────────────────────────────────────────────────────
	CreateMessage(ctx context.Context, m *model.AgentMessage) error
	// GetRecentMessages 获取 session 最近 N 轮消息(一轮 = user + assistant),按时间正序返回。
	GetRecentMessages(ctx context.Context, sessionID string, maxRounds int) ([]*model.AgentMessage, error)
	ListMessagesBySession(ctx context.Context, sessionID string, page, size int) ([]*model.AgentMessage, int64, error)
	DeleteMessagesBySession(ctx context.Context, sessionID string) error

	// ─── Cascade Delete ───────────────────────────────────────────────────
	DeletePublishesByAgent(ctx context.Context, agentID uint64) error
	DeleteSessionsByAgent(ctx context.Context, agentID uint64) error
	DeleteMessagesByAgent(ctx context.Context, agentID uint64) error
	DeleteMethodsByAgent(ctx context.Context, agentID uint64) error
	DeleteSecretsByAgent(ctx context.Context, agentID uint64) error
	DeleteInvocationPayloadsByAgent(ctx context.Context, agentID uint64) error
	DeleteInvocationsByAgent(ctx context.Context, agentID uint64) error

	// ─── Publish ──────────────────────────────────────────────────────────
	CreatePublish(ctx context.Context, p *model.AgentPublish) error
	FindPublishByID(ctx context.Context, id uint64) (*model.AgentPublish, error)
	FindActivePublish(ctx context.Context, agentID, orgID uint64) (*model.AgentPublish, error)
	ListPublishesByOrg(ctx context.Context, orgID uint64, status string, page, size int) ([]*model.AgentPublish, int64, error)
	UpdatePublishFields(ctx context.Context, id uint64, updates map[string]any) error
	RevokePublishesByAuthorOrg(ctx context.Context, orgID, authorUserID uint64, reason string, now time.Time) (int64, error)
	RevokePublishesByOrg(ctx context.Context, orgID uint64, reason string, now time.Time) (int64, error)
	ListAgentIDsByOrg(ctx context.Context, orgID uint64) ([]uint64, error)
}

// gormRepository GORM 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
