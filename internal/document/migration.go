// migration.go document 模块数据库迁移。
//
// 拆成两条腿:
//   - MySQL:documents 主元数据表(AutoMigrate)。
//   - Postgres + pgvector(可选):document_chunks 切片 + 向量表(AutoMigrate 列,raw SQL 建 HNSW)。
//
// pgDB 为 nil 表示未启用向量能力,只迁 MySQL,上层后续读/写向量那段自己检查并降级。
package document

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"gorm.io/gorm"
)

// RunMigrations 按当前运行时配置迁 document 模块的两个库。
//
// 失败场景:
//   - MySQL AutoMigrate 失败 → ErrDocumentInternal。
//   - Postgres AutoMigrate 或 HNSW 建索引失败 → ErrDocumentInternal(pgvector 扩展缺失会在 CREATE INDEX 时暴露)。
//
// 成功时调用 onReady(若非 nil)。
func RunMigrations(ctx context.Context, mysqlDB *gorm.DB, pgDB *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.Info("document: running MySQL migrations", nil)
	if err := mysqlDB.WithContext(ctx).AutoMigrate(&model.Document{}); err != nil {
		return fmt.Errorf("document auto-migrate mysql: %w: %w", err, ErrDocumentInternal)
	}

	if pgDB != nil {
		log.Info("document: running Postgres (pgvector) migrations", nil)
		if err := pgDB.WithContext(ctx).AutoMigrate(&model.DocumentChunk{}); err != nil {
			return fmt.Errorf("document auto-migrate postgres: %w: %w", err, ErrDocumentInternal)
		}
		if err := ensureHNSWIndex(ctx, pgDB); err != nil {
			return fmt.Errorf("document create hnsw index: %w: %w", err, ErrDocumentInternal)
		}
		if err := ensurePgStructuralIndexes(ctx, pgDB); err != nil {
			return fmt.Errorf("document ensure structural indexes: %w: %w", err, ErrDocumentInternal)
		}
	} else {
		log.Warn("document: Postgres not configured, skipping chunk table migration", nil)
	}

	log.Info("document: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}

// ensureHNSWIndex 幂等建 HNSW cosine 索引。
//
// pgvector 的 HNSW 只支持 ≤2000 维,schema 里 vector(1536) 已经对齐;改维度前必须先改 schema + 重建索引。
// cosine 距离适配 OpenAI 系 embedding(模型输出已近似单位化,cosine 和 dot product 近似等价,但 cosine 语义更直观)。
// 空表建 HNSW 开销几乎为 0;存量数据上建索引才会慢,此处 IF NOT EXISTS 保证只第一次生效。
func ensureHNSWIndex(ctx context.Context, pgDB *gorm.DB) error {
	const stmt = `CREATE INDEX IF NOT EXISTS idx_document_chunks_embedding_hnsw
		ON document_chunks USING hnsw (embedding vector_cosine_ops)`
	//sayso-lint:ignore log-coverage
	return pgDB.WithContext(ctx).Exec(stmt).Error
}

// ensurePgStructuralIndexes 幂等补齐 GORM AutoMigrate 不覆盖的 Postgres 原生索引与 generated column。
//
// 关注点:
//   - metadata(jsonb) 的 GIN:T1.4 结构化 metadata 过滤用。默认 jsonb_ops 支持 @> / ? / ?& / ?|,
//     比 jsonb_path_ops 稍大但灵活性够用。
//   - T1.1 BM25 所需的两列 + 索引:
//     * tsv_tokens(text,可写)—— GORM AutoMigrate 已建,这里不管
//     * content_tsv(tsvector,GENERATED STORED)—— PG 自动从 tsv_tokens 算,GORM 不支持要 raw SQL
//     * content_tsv 的 GIN 索引
//
// 升级场景兼容:Phase 0 的 content_tsv 是普通 tsvector 列(未填 NULL)。本 migration 检测到它
// 不是 generated 就 drop 重建成 generated。用 DO block 做"存在性 + 类型"判断保证幂等。
func ensurePgStructuralIndexes(ctx context.Context, pgDB *gorm.DB) error {
	stmts := []struct {
		name string
		sql  string
	}{
		{
			name: "metadata GIN",
			sql: `CREATE INDEX IF NOT EXISTS idx_document_chunks_metadata_gin
				ON document_chunks USING GIN (metadata)`,
		},
		{
			// 如果 content_tsv 已存在且 is_generated='NEVER'(Phase 0 状态)→ drop 对应 GIN + 列,
			// 然后按新定义重建。已经是 generated 的情况下这段整体 skip。
			name: "content_tsv → generated",
			sql: `DO $$
				BEGIN
					IF EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_schema = current_schema()
						  AND table_name = 'document_chunks'
						  AND column_name = 'content_tsv'
						  AND is_generated = 'NEVER'
					) THEN
						DROP INDEX IF EXISTS idx_document_chunks_content_tsv_gin;
						ALTER TABLE document_chunks DROP COLUMN content_tsv;
					END IF;
					IF NOT EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_schema = current_schema()
						  AND table_name = 'document_chunks'
						  AND column_name = 'content_tsv'
					) THEN
						ALTER TABLE document_chunks
							ADD COLUMN content_tsv tsvector
							GENERATED ALWAYS AS (to_tsvector('simple', coalesce(tsv_tokens, ''))) STORED;
					END IF;
				END $$`,
		},
		{
			name: "content_tsv GIN",
			sql: `CREATE INDEX IF NOT EXISTS idx_document_chunks_content_tsv_gin
				ON document_chunks USING GIN (content_tsv)`,
		},
	}
	for _, s := range stmts {
		//sayso-lint:ignore log-coverage
		if err := pgDB.WithContext(ctx).Exec(s.sql).Error; err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}
