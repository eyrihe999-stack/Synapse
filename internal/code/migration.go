// migration.go code 模块 Postgres 迁移。
//
// 四张表全在 PG,和 document 拆 MySQL+PG 不同 —— 代码文件直接 bytea 存 PG,省去跨库一致性。
// pgDB 为 nil 表示未启用 Postgres,整个 code 模块不应该被装配(main 负责判断)。
package code

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/code/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// RunMigrations 跑 code 模块的 PG 迁移 —— 建表 + HNSW 向量索引。
//
// 失败场景:
//   - AutoMigrate 失败 → ErrCodeInternal(pgvector 扩展未启用会在 embedding 列建表时报错)
//   - HNSW 创建失败  → ErrCodeInternal(通常也是 pgvector 未装)
//
// 成功时调用 onReady(非 nil)。
func RunMigrations(ctx context.Context, pgDB *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if pgDB == nil {
		// 调用方应该在 main 就判断过;这里兜底防呆,不让 nil deref。
		log.Warn("code: Postgres not configured, skipping code module migrations", nil)
		return nil
	}
	log.Info("code: running Postgres migrations", nil)
	if err := pgDB.WithContext(ctx).AutoMigrate(
		&model.CodeRepository{},
		&model.CodeFileContent{},
		&model.CodeFile{},
		&model.CodeChunk{},
	); err != nil {
		return fmt.Errorf("code auto-migrate: %w: %w", err, ErrCodeInternal)
	}

	if err := ensureCodeRepoUniqueIndex(ctx, pgDB); err != nil {
		return fmt.Errorf("code ensure unique index: %w: %w", err, ErrCodeInternal)
	}

	if err := ensureCodeHNSWIndex(ctx, pgDB); err != nil {
		return fmt.Errorf("code create hnsw index: %w: %w", err, ErrCodeInternal)
	}

	log.Info("code: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// ensureCodeRepoUniqueIndex 幂等升级 code_repositories 上 (org_id, provider, external_project_id) 的索引。
//
// 背景:早期 model tag 错用了 `index:` 导致索引是普通 btree;Upsert 走 ON CONFLICT 时 PG 要求
// 必须是 UNIQUE 索引或 EXCLUSION 约束(42P10)。GORM AutoMigrate 不升级索引类型 —— 检测到同名索引
// 存在就跳过。这里手动 DROP 旧的 + CREATE UNIQUE,让已部署环境能自愈。
//
// 新部署环境:AutoMigrate 已经按新 tag 建好 uk_code_repos_org_provider_extid,DROP IF EXISTS
// 对 idx_code_repos_org_provider_extid 是 no-op,CREATE IF NOT EXISTS 也是 no-op。幂等。
func ensureCodeRepoUniqueIndex(ctx context.Context, pgDB *gorm.DB) error {
	stmts := []string{
		`DROP INDEX IF EXISTS idx_code_repos_org_provider_extid`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_code_repos_org_provider_extid
			ON code_repositories (org_id, provider, external_project_id)`,
	}
	for _, s := range stmts {
		if err := pgDB.WithContext(ctx).Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}

// ensureCodeHNSWIndex 幂等建 code_chunks.embedding 的 HNSW cosine 索引。
//
// 和 document_chunks 用同一份 pgvector 扩展 —— 只要 document 那边迁移跑过一次,
// CREATE EXTENSION 就已经就位,这里直接 CREATE INDEX 即可。
// cosine 距离适配 OpenAI 系 embedding(输出近似单位化)。改维度要手动 DROP + 重建。
func ensureCodeHNSWIndex(ctx context.Context, pgDB *gorm.DB) error {
	const stmt = `CREATE INDEX IF NOT EXISTS idx_code_chunks_embedding_hnsw
		ON code_chunks USING hnsw (embedding vector_cosine_ops)`
	return pgDB.WithContext(ctx).Exec(stmt).Error
}
