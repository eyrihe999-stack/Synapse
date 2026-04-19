// repo.go code_repositories 表 CRUD。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/eyrihe999-stack/Synapse/internal/code"
	"github.com/eyrihe999-stack/Synapse/internal/code/model"
)

// RepoSummary 聚合视图 —— 前端"已同步的仓库"卡片用。一次 SQL 带 JOIN 把三张表聚合完。
//
// 为什么不放在 model 包:这不是持久化结构,只是查询投影,放 repository 包就近定义更合适。
type RepoSummary struct {
	ID                uint64     `gorm:"column:id"`
	PathWithNamespace string     `gorm:"column:path_with_namespace"`
	WebURL            string     `gorm:"column:web_url"`
	DefaultBranch     string     `gorm:"column:default_branch"`
	LastSyncedAt      *time.Time `gorm:"column:last_synced_at"`
	Archived          bool       `gorm:"column:archived"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	FileCount         int64      `gorm:"column:file_count"`
	ChunkCount        int64      `gorm:"column:chunk_count"`
	FailedChunkCount  int64      `gorm:"column:failed_chunk_count"`
}

// CodeRepositoryRepo code_repositories 表数据访问。
type CodeRepositoryRepo interface {
	// Upsert 按 (org_id, provider, external_project_id) 唯一键插入或更新。
	// 成功后 id 字段被 GORM 填回;provider 侧 rename 时 path_with_namespace / default_branch 刷新。
	// last_synced_* 字段**不在 Upsert 写入**(单独走 UpdateLastSynced)—— 避免并发 sync 打架。
	Upsert(ctx context.Context, r *model.CodeRepository) error

	// GetByExternalID 按业务唯一键查。未命中返 nil, nil(不算错),让调用方决定是否 create。
	GetByExternalID(ctx context.Context, orgID uint64, provider, externalID string) (*model.CodeRepository, error)

	// ListByOrg 列 org 下所有 repo,按 created_at 升序。给前端 repo 管理页用。
	ListByOrg(ctx context.Context, orgID uint64) ([]*model.CodeRepository, error)

	// ListSummariesByOrg 列 org 下所有 repo 的聚合视图(含文件数 / chunk 数 / 失败数)。
	// 一次 SQL 带 JOIN + 分组 COUNT。按 last_synced_at DESC NULLS LAST 排,未同步过的沉底。
	// 用于前端展示"已同步仓库"概览卡片。
	ListSummariesByOrg(ctx context.Context, orgID uint64) ([]*RepoSummary, error)

	// UpdateLastSynced sync 完成一轮后写回 last_synced_commit + last_synced_at。
	// 单独接口:避免和 Upsert 写同一行竞态。
	UpdateLastSynced(ctx context.Context, id uint64, commit string, at time.Time) error

	// DeleteByID 物理删除 repo 行。级联清 code_files / code_chunks 由 service 层调 Files/Chunks 的批量删完成
	// (不在 DB 建 FK,保持跨模块解耦)。
	DeleteByID(ctx context.Context, id uint64) error

	// GetByIDs 批量按 id 拿 repo 元信息。给 search 回填用(多 chunk 可能跨多 repo)。
	// 未命中的 id 不出现在结果 map 里(不算错)。
	GetByIDs(ctx context.Context, ids []uint64) (map[uint64]*model.CodeRepository, error)
}

type gormCodeRepositoryRepo struct {
	db *gorm.DB
}

// NewCodeRepositoryRepo 构造。
func NewCodeRepositoryRepo(pgDB *gorm.DB) CodeRepositoryRepo {
	return &gormCodeRepositoryRepo{db: pgDB}
}

func (r *gormCodeRepositoryRepo) Upsert(ctx context.Context, repo *model.CodeRepository) error {
	// ON CONFLICT DO UPDATE:仅刷新 provider 侧可能变化的字段。last_synced_* 刻意不在此列。
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "org_id"},
				{Name: "provider"},
				{Name: "external_project_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"path_with_namespace",
				"default_branch",
				"web_url",
				"archived",
				"updated_at",
			}),
		}).
		Create(repo).Error
	if err != nil {
		return fmt.Errorf("upsert code_repository: %w", err)
	}
	return nil
}

func (r *gormCodeRepositoryRepo) GetByExternalID(ctx context.Context, orgID uint64, provider, externalID string) (*model.CodeRepository, error) {
	var out model.CodeRepository
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND provider = ? AND external_project_id = ?", orgID, provider, externalID).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get code_repository by ext id: %w", err)
	}
	return &out, nil
}

func (r *gormCodeRepositoryRepo) ListByOrg(ctx context.Context, orgID uint64) ([]*model.CodeRepository, error) {
	var out []*model.CodeRepository
	err := r.db.WithContext(ctx).
		Where("org_id = ?", orgID).
		Order("created_at ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list code_repositories by org: %w", err)
	}
	return out, nil
}

// ListSummariesByOrg 用 LEFT JOIN 子查询一次拿齐三张表的聚合。
//
// SQL 设计要点:
//   - code_files / code_chunks 都可能没行(新 repo 刚 upsert,还没同步完文件),LEFT JOIN + COALESCE 兜底
//   - chunk 聚合里同时算 total + failed,避免两次扫描 code_chunks
//   - ORDER BY last_synced_at DESC NULLS LAST:刚同步的置顶,"从未同步"的(新 upsert 但 Phase 2 还没跑到)沉底
func (r *gormCodeRepositoryRepo) ListSummariesByOrg(ctx context.Context, orgID uint64) ([]*RepoSummary, error) {
	const query = `
SELECT r.id, r.path_with_namespace, r.web_url, r.default_branch,
       r.last_synced_at, r.archived, r.created_at,
       COALESCE(f.file_count, 0) AS file_count,
       COALESCE(c.chunk_count, 0) AS chunk_count,
       COALESCE(c.failed_count, 0) AS failed_chunk_count
  FROM code_repositories r
  LEFT JOIN (
       SELECT repo_id, COUNT(*) AS file_count
         FROM code_files GROUP BY repo_id
  ) f ON f.repo_id = r.id
  LEFT JOIN (
       SELECT repo_id,
              COUNT(*) AS chunk_count,
              COUNT(*) FILTER (WHERE index_status = 'failed') AS failed_count
         FROM code_chunks GROUP BY repo_id
  ) c ON c.repo_id = r.id
 WHERE r.org_id = ?
 ORDER BY r.last_synced_at DESC NULLS LAST, r.created_at DESC`
	var out []*RepoSummary
	if err := r.db.WithContext(ctx).Raw(query, orgID).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("list code repo summaries: %w", err)
	}
	return out, nil
}

func (r *gormCodeRepositoryRepo) UpdateLastSynced(ctx context.Context, id uint64, commit string, at time.Time) error {
	updates := map[string]any{
		"last_synced_commit": commit,
		"last_synced_at":     at,
	}
	err := r.db.WithContext(ctx).
		Model(&model.CodeRepository{}).
		Where("id = ?", id).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("update last_synced: %w", err)
	}
	return nil
}

func (r *gormCodeRepositoryRepo) GetByIDs(ctx context.Context, ids []uint64) (map[uint64]*model.CodeRepository, error) {
	if len(ids) == 0 {
		return map[uint64]*model.CodeRepository{}, nil
	}
	var rows []*model.CodeRepository
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("get code_repositories by ids: %w", err)
	}
	out := make(map[uint64]*model.CodeRepository, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

func (r *gormCodeRepositoryRepo) DeleteByID(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&model.CodeRepository{})
	if res.Error != nil {
		return fmt.Errorf("delete code_repository: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return code.ErrRepositoryNotFound
	}
	return nil
}
