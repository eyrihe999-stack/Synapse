// file.go code_files + code_file_contents 两张表的 CRUD。
//
// 两表拆分的原因见 model/models.go:内容按 blob_sha CAS 去重,元信息列表查询不载内容。
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/eyrihe999-stack/Synapse/internal/code"
	"github.com/eyrihe999-stack/Synapse/internal/code/model"
)

// CodeFileRepo 访问 code_files + code_file_contents。
//
// 写入语义:Upsert 系列,一次调用搞定"该 blob 没存过就存 + 该文件条目 upsert"。
// 读取语义:默认不带 content(避免全表 BYTEA 扫);需要内容时单独调 GetContent(blob_sha)。
type CodeFileRepo interface {
	// UpsertContent 按 blob_sha 幂等写入内容。同一 blob 已存在时直接 no-op
	// (ON CONFLICT DO NOTHING)—— 不要 UPDATE 覆盖,内容按定义就是不可变的(内容变 = blob_sha 也变)。
	UpsertContent(ctx context.Context, c *model.CodeFileContent) error

	// UpsertFile 按 (repo_id, path) 唯一键 upsert 文件元信息。
	// 文件路径可能换 blob_sha(内容更新),或保持 blob_sha 不变(metadata 变更),都由此一条统一处理。
	// 成功后 ID 字段被 GORM 填回。
	UpsertFile(ctx context.Context, f *model.CodeFile) error

	// GetContent 按 blob_sha 读内容。未命中返 nil, nil(正常场景:blob 刚被 GC 或数据不一致,让调用方决定)。
	GetContent(ctx context.Context, blobSHA string) (*model.CodeFileContent, error)

	// GetByRepoAndPath 按业务唯一键查。未命中返 nil, nil。
	GetByRepoAndPath(ctx context.Context, repoID uint64, path string) (*model.CodeFile, error)

	// ListByRepoID 列 repo 下所有 file 元信息(不带 content)。供 sync 时对比 DB 现有文件 vs source 快照
	// (sync 对比 blob_sha 决定是否 re-chunk/re-embed)。
	ListByRepoID(ctx context.Context, repoID uint64) ([]*model.CodeFile, error)

	// DeleteByID 物理删单行。chunks 级联清由 service 层调 Chunks().DeleteByFileID 完成。
	DeleteByID(ctx context.Context, fileID uint64) error

	// DeleteByRepoID 批量删 repo 下所有 files。chunks 级联在 service 层做。
	// 返删除行数,诊断用。
	DeleteByRepoID(ctx context.Context, repoID uint64) (int64, error)

	// GetByIDs 批量按 id 拿文件元信息(不含 content)。给 search 回填用:
	// 命中多条 chunk 可能分布在多个 file,合并 file_id 去重后一次 SQL 拿齐,避免 N+1。
	// 返的 map 缺项 = 该 id 不存在(不算错,可能文件被删了但 chunk 还没清)。
	GetByIDs(ctx context.Context, ids []uint64) (map[uint64]*model.CodeFile, error)
}

type gormCodeFileRepo struct {
	db *gorm.DB
}

// NewCodeFileRepo 构造。
func NewCodeFileRepo(pgDB *gorm.DB) CodeFileRepo {
	return &gormCodeFileRepo{db: pgDB}
}

func (r *gormCodeFileRepo) UpsertContent(ctx context.Context, c *model.CodeFileContent) error {
	// DoNothing:内容不可变,重复写跳过即可。OnConflict 在 PK (blob_sha) 上命中就什么都不做。
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(c).Error
	if err != nil {
		return fmt.Errorf("upsert code_file_content: %w", err)
	}
	return nil
}

func (r *gormCodeFileRepo) UpsertFile(ctx context.Context, f *model.CodeFile) error {
	// ON CONFLICT (repo_id, path) DO UPDATE:文件元信息可能变
	// (blob_sha 换 = 内容更新;size_bytes 跟着变;last_commit_id 变)。
	// org_id 虽然冗余但不会变(同 repo 永远属于同 org),不写进 UPDATE 列表,避免被误覆盖。
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "repo_id"}, {Name: "path"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"language",
				"size_bytes",
				"blob_sha",
				"last_commit_id",
				"updated_at",
			}),
		}).
		Create(f).Error
	if err != nil {
		return fmt.Errorf("upsert code_file: %w", err)
	}
	return nil
}

func (r *gormCodeFileRepo) GetContent(ctx context.Context, blobSHA string) (*model.CodeFileContent, error) {
	var out model.CodeFileContent
	err := r.db.WithContext(ctx).
		Where("blob_sha = ?", blobSHA).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get code_file_content: %w", err)
	}
	return &out, nil
}

func (r *gormCodeFileRepo) GetByRepoAndPath(ctx context.Context, repoID uint64, path string) (*model.CodeFile, error) {
	var out model.CodeFile
	err := r.db.WithContext(ctx).
		Where("repo_id = ? AND path = ?", repoID, path).
		First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get code_file by path: %w", err)
	}
	return &out, nil
}

func (r *gormCodeFileRepo) ListByRepoID(ctx context.Context, repoID uint64) ([]*model.CodeFile, error) {
	var out []*model.CodeFile
	err := r.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Order("path ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list code_files by repo: %w", err)
	}
	return out, nil
}

func (r *gormCodeFileRepo) DeleteByID(ctx context.Context, fileID uint64) error {
	res := r.db.WithContext(ctx).
		Where("id = ?", fileID).
		Delete(&model.CodeFile{})
	if res.Error != nil {
		return fmt.Errorf("delete code_file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return code.ErrFileNotFound
	}
	return nil
}

func (r *gormCodeFileRepo) DeleteByRepoID(ctx context.Context, repoID uint64) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Delete(&model.CodeFile{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete code_files by repo: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func (r *gormCodeFileRepo) GetByIDs(ctx context.Context, ids []uint64) (map[uint64]*model.CodeFile, error) {
	if len(ids) == 0 {
		return map[uint64]*model.CodeFile{}, nil
	}
	var files []*model.CodeFile
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&files).Error; err != nil {
		return nil, fmt.Errorf("get code_files by ids: %w", err)
	}
	out := make(map[uint64]*model.CodeFile, len(files))
	for _, f := range files {
		out[f.ID] = f
	}
	return out, nil
}
