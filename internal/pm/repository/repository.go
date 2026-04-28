// Package repository pm 模块数据访问层。
//
// 设计对齐 channel/repository:一个顶层 Repository interface 汇总所有方法,
// 实现按 entity 拆到多个文件(project.go / initiative.go / version.go /
// workstream.go / project_kb_ref.go)。事务支持通过 WithTx 传入同一个 *gorm.DB 句柄。
//
// 错误处理:repository 层直接返回底层 gorm 错误或 errors.Is(err, gorm.ErrRecordNotFound),
// 由 service 层翻译成模块的哨兵错误。
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
)

// Repository 所有 pm 子实体数据访问的统一入口。
type Repository interface {
	// ── 事务 ────────────────────────────────────────────────────────────────
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ── Project ────────────────────────────────────────────────────────────
	CreateProject(ctx context.Context, p *model.Project) error
	FindProjectByID(ctx context.Context, id uint64) (*model.Project, error)
	ListProjectsByOrg(ctx context.Context, orgID uint64, limit, offset int) ([]model.Project, error)
	UpdateProjectFields(ctx context.Context, id uint64, updates map[string]any) error
	CountActiveProjectByName(ctx context.Context, orgID uint64, name string) (int64, error)
	// ListAllProjects 不限 org,主要供 migration 阶段为存量 project 回填 default
	// initiative / Backlog version 用。limit=0 表示不分页(全量;migration 期间 OK)。
	ListAllProjects(ctx context.Context, limit, offset int) ([]model.Project, error)

	// ── Version ────────────────────────────────────────────────────────────
	CreateVersion(ctx context.Context, v *model.Version) error
	FindVersionByID(ctx context.Context, id uint64) (*model.Version, error)
	ListVersionsByProject(ctx context.Context, projectID uint64) ([]model.Version, error)
	UpdateVersionFields(ctx context.Context, id uint64, updates map[string]any) error
	// FindBacklogVersion 按 (project_id, is_system=true, name=Backlog) 查 system version,
	// migration / service 层判断 default 是否已建用;查无返 gorm.ErrRecordNotFound。
	FindBacklogVersion(ctx context.Context, projectID uint64) (*model.Version, error)
	// CountActiveVersionByName 在 project 下按名字查重(不含已 cancelled 的);DB 层 UNIQUE 兜底,
	// 应用层预检返友好错误码。
	CountActiveVersionByName(ctx context.Context, projectID uint64, name string) (int64, error)

	// ── Initiative ─────────────────────────────────────────────────────────
	CreateInitiative(ctx context.Context, i *model.Initiative) error
	FindInitiativeByID(ctx context.Context, id uint64) (*model.Initiative, error)
	ListInitiativesByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Initiative, error)
	UpdateInitiativeFields(ctx context.Context, id uint64, updates map[string]any) error
	CountActiveInitiativeByName(ctx context.Context, projectID uint64, name string) (int64, error)
	// FindDefaultInitiative 找 project 的 system Default initiative。命中
	// (project_id, is_system=true, name=DefaultInitiativeName);查无返
	// gorm.ErrRecordNotFound。
	FindDefaultInitiative(ctx context.Context, projectID uint64) (*model.Initiative, error)
	// CountActiveWorkstreamsByInitiative 给"删 initiative 前检查是否还有未归档
	// workstream"用;0 表示可以归档/删。
	CountActiveWorkstreamsByInitiative(ctx context.Context, initiativeID uint64) (int64, error)

	// ── Workstream ─────────────────────────────────────────────────────────
	CreateWorkstream(ctx context.Context, w *model.Workstream) error
	FindWorkstreamByID(ctx context.Context, id uint64) (*model.Workstream, error)
	ListWorkstreamsByInitiative(ctx context.Context, initiativeID uint64, limit, offset int) ([]model.Workstream, error)
	ListWorkstreamsByVersion(ctx context.Context, versionID uint64, limit, offset int) ([]model.Workstream, error)
	ListWorkstreamsByProject(ctx context.Context, projectID uint64, limit, offset int) ([]model.Workstream, error)
	UpdateWorkstreamFields(ctx context.Context, id uint64, updates map[string]any) error

	// ── ProjectKBRef ───────────────────────────────────────────────────────
	CreateProjectKBRef(ctx context.Context, r *model.ProjectKBRef) error
	DeleteProjectKBRef(ctx context.Context, id uint64) error
	FindProjectKBRefByID(ctx context.Context, id uint64) (*model.ProjectKBRef, error)
	ListProjectKBRefsByProject(ctx context.Context, projectID uint64) ([]model.ProjectKBRef, error)
	// FindProjectKBRefByTarget 同 (project_id, source_id, doc_id) 查重,二选一
	// 时另一个传 0;查无返 gorm.ErrRecordNotFound。
	FindProjectKBRefByTarget(ctx context.Context, projectID, sourceID, docID uint64) (*model.ProjectKBRef, error)

	// ── 跨模块 helper(轻量,避免引入额外依赖)─────────────────────────────
	// LookupUserPrincipalID 按 users.id 反查 principal_id。手法对齐 channel/repository
	// 同名方法 —— pm 模块 service 内部要 actor user_id 转 principal_id 时用。
	LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error)

	// SeedProjectDefaults 为单个 project 触发 default initiative + Backlog version +
	// Project Console channel(含 owner 成员)的 seed。
	//
	// ProjectService.Create 完成后立即调用 —— 否则用户从 HTTP 创建 project 后
	// 看不到任何 default 资源,要等下次重启 pm.RunPostMigrations 才能补上。
	//
	// 全部步骤幂等(INSERT IGNORE / NOT EXISTS),重试安全。
	SeedProjectDefaults(ctx context.Context, projectID uint64) error

	// AddMembersToWorkstreamChannel 把一组 principal 作为 'member' 角色加入 workstream
	// 对应的 channel(workstream.channel_id 指向的 channel)。invite_to_workstream
	// MCP tool 用。
	//
	// 守卫:workstream 不存在或未归档 / channel 未 lazy-create 都返错。
	// 重复加(同 channel + 同 principal)走 INSERT IGNORE 兜底。
	// 返回实际加入的 principal_id 列表(已经是成员的不重复返)。
	AddMembersToWorkstreamChannel(ctx context.Context, workstreamID uint64, principalIDs []uint64) (added []uint64, channelID uint64, err error)
}

// gormRepository Repository 的 GORM 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务,fn 接到一个事务内的 Repository;返回错误自动回滚。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}

// ─── 跨模块查询 ─────────────────────────────────────────────────────────────

// LookupUserPrincipalID 反查 users.id → principal_id。和 channel/repository 同名方法
// 走的是同一张 users 表,只是为避免 pm 反向依赖 channel/user 模块,这里独立实现一份。
func (r *gormRepository) LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).
		Table("users").
		Select("principal_id").
		Where("id = ?", userID).
		Scan(&pid).Error
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// ─── 公共 helper ───────────────────────────────────────────────────────────

// applyTimeNow 内部 helper:某些字段需要显式 NOW(),GORM autoUpdateTime 不够用时用。
func applyTimeNow(updates map[string]any, key string) {
	updates[key] = time.Now()
}

// _ 防止 applyTimeNow 暂未使用产生编译警告(后续 service 用到会引用)。
var _ = applyTimeNow
