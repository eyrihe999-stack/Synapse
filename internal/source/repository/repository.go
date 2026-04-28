// repository.go source 模块 Repository 接口定义与事务封装。
//
// 设计要点:
//   - 所有 mutation 方法在内部以**同事务**写入 permission_audit_log 行
//     (通过共享的 internal/permission/audit.Write 函数)
//   - actor_user_id 由 audit 写入侧从 ctx 读取
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/source/model"
	"gorm.io/gorm"
)

// Repository source 模块的数据访问入口。
//
//sayso-lint:ignore interface-pollution
type Repository interface {
	// ─── 事务 ──────────────────────────────────────────────────────────────

	// WithTx 在事务内执行 fn,事务内所有 repo 调用共享同一个 tx。
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── Source ─────────────────────────────────────────────────────────────

	// CreateSource 创建一条 source 记录,同事务写一条 source.create audit。
	// (org_id, kind, owner_user_id, external_ref) 唯一冲突由 uk_sources_full 索引保证。
	CreateSource(ctx context.Context, src *model.Source) error

	// FindSourceByID 按主键查找。不存在返回 gorm.ErrRecordNotFound。
	FindSourceByID(ctx context.Context, id uint64) (*model.Source, error)

	// FindManualUploadSource 在某 org 内按 (kind=manual_upload, owner_user_id, external_ref='')
	// 精确查找。不存在返回 gorm.ErrRecordNotFound。给 lazy create 用。
	FindManualUploadSource(ctx context.Context, orgID, ownerUserID uint64) (*model.Source, error)

	// FindSourceByOwnerAndName 在某 (org, owner) 下按 name 精确查找,CreateCustomSource 重名预检用。
	// 不存在返回 gorm.ErrRecordNotFound。
	FindSourceByOwnerAndName(ctx context.Context, orgID, ownerUserID uint64, name string) (*model.Source, error)

	// EnsureManualUploadSource 幂等地确保某用户在某 org 下的 manual_upload source 存在。
	// 已存在 → 返回现有行;不存在 → 创建并写 audit。
	// 并发场景下依赖 uk_sources_full 唯一约束兜底:Create 冲突时回读。
	EnsureManualUploadSource(ctx context.Context, orgID, ownerUserID uint64) (*model.Source, bool, error)

	// UpdateSourceVisibility 更新 visibility,同事务写 source.visibility_change audit。
	// 调用方应先校验 newVisibility 合法 + 与当前值不同(no-op 时本方法直接返回 nil 不写 audit)。
	UpdateSourceVisibility(ctx context.Context, sourceID uint64, newVisibility string) error

	// UpdateGitLabSyncStatus 在 sync runner 终态时回写 last_sync_* 字段。
	// status 见 model.SyncStatus*;commitSHA 仅 status=succeeded 有值(失败时调用方传空串即可,
	// 本方法只覆盖非空 commitSHA,保留上次成功的 commit 作增量起点);errSummary 失败时摘要,
	// 成功时传空串即清空。本方法不写 audit —— sync 终态高频,审计走 async_jobs 表 + eventbus。
	UpdateGitLabSyncStatus(ctx context.Context, sourceID uint64, status, commitSHA, errSummary string) error

	// DeleteSource 按主键删 source,同事务写一条 source.delete audit(before=snapshot, after=null)。
	// 不负责清理 resource_acl 行 —— 调用方(service 层)需先通过 ACLOps.BulkRevokeACLsByResource 清掉。
	// 不存在或已被删 → 返回 gorm.ErrRecordNotFound,由 service 层翻译。
	DeleteSource(ctx context.Context, sourceID uint64) error

	// ListSourcesByOrg 分页列出某 org 的 source(按 created_at DESC)。
	//
	// kindFilter 为空时返全部 kind;非空只返指定 kind。
	// idsFilter 控制 ACL 可见性过滤:
	//   - nil:不过滤(管理视图,scope=all)
	//   - 空 slice:直接返空集合(用户在该 org 看不到任何 source)
	//   - 非空:WHERE id IN (...)
	ListSourcesByOrg(ctx context.Context, orgID uint64, kindFilter string, idsFilter []uint64, page, size int) ([]*model.Source, int64, error)

	// ListSourcesByOwner 列出某 user 在某 org 中作为 owner 的所有 source(不分页)。
	// 单 user 名下 source 数量天然小,不需要分页。
	ListSourcesByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]*model.Source, error)

	// ─── Permission 判定专用轻量查询 ──────────────────────────────────────────
	// 这两个方法供 permission 模块的判定 service 调,只取 id 不取详情,减少传输。

	// ListSourceIDsByOwner 列某 user 持有的 source id(用于"我的所有 source 都隐式可写")。
	ListSourceIDsByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]uint64, error)

	// ListSourceIDsByVisibility 列指定 visibility 的 source id(用于"全 org 可见"过滤)。
	ListSourceIDsByVisibility(ctx context.Context, orgID uint64, visibility string) ([]uint64, error)

	// ListSourceIDsByKindsInIDs 在给定 ID 集合里再按 kind 白名单过滤,只返 ID。
	// 给"知识库文档列表只展示 manual_upload"等场景用 —— documents 在 PG / sources 在
	// MySQL 跨库查不了,handler 拿 visible 集后必须独立来这边求交集。
	// kinds 空 → 直接返 ids;ids 空 → 短路返空。
	ListSourceIDsByKindsInIDs(ctx context.Context, orgID uint64, kinds []string, ids []uint64) ([]uint64, error)
}

// gormRepository 基于 GORM 的统一实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务并在其中执行 fn。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	//sayso-lint:ignore log-coverage
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
