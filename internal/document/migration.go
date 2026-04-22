// migration.go document 模块的 PG schema 迁移。
//
// 设计要点:
//
//  1. 不走 gorm AutoMigrate —— pgvector 的 `vector` 类型 gorm 不认识,会生成错的 DDL。
//     所有 CREATE TABLE / CREATE INDEX 都用原生 SQL + IF NOT EXISTS 保证幂等。
//  2. embedding 列的维度参数化:CREATE TABLE 时把 cfg.Embedding.Text.ModelDim 插值进去。
//     DDL 建完后做一次 sanity check(实际列维度 vs cfg 维度),不一致 → 返 ErrDimMismatch,
//     装配层 fatal。防止"改 config 不改 schema"静默写脏数据。
//  3. HNSW partial index (`WHERE index_status = 'indexed'`) 是 pgvector 官方推荐姿势,
//     failed 行的 NULL embedding 天然不进索引,两层保险。
package document

import (
	"context"
	"fmt"
	"regexp"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

// RunMigrations 在 PG 上建 documents + document_chunks 两张表及索引。
//
// embeddingDim 取 cfg.Embedding.Text.ModelDim,调用方传入。范围 [1, PGVectorMaxDim] 才合法,
// 超过 2000 返 error(HNSW 上限),≤ 0 返 error(明显误配)。
//
// onReady 迁移成功完成后调用,给 main.go 协调服务就绪用。
//
//nolint:funlen // DDL 集中放在一起可读性更好,不切分
func RunMigrations(ctx context.Context, db *gorm.DB, embeddingDim int, log logger.LoggerInterface, onReady func()) error {
	if db == nil {
		log.ErrorCtx(ctx, "document: pg db is nil", nil, nil)
		return fmt.Errorf("document: pg db is nil")
	}
	if embeddingDim <= 0 {
		log.ErrorCtx(ctx, "document: embedding dim must be > 0", nil, map[string]any{"dim": embeddingDim})
		return fmt.Errorf("document: embedding dim must be > 0, got %d: %w", embeddingDim, ErrDimMismatch)
	}
	if embeddingDim > PGVectorMaxDim {
		log.ErrorCtx(ctx, "document: embedding dim exceeds pgvector HNSW max", nil, map[string]any{
			"dim": embeddingDim, "max": PGVectorMaxDim,
		})
		return fmt.Errorf("document: embedding dim %d exceeds pgvector HNSW max %d: %w",
			embeddingDim, PGVectorMaxDim, ErrDimMismatch)
	}

	// CREATE EXTENSION 已经在 main.go 启动期调过(internal/common/database.EnablePGVectorExtension),
	// 这里再调一次幂等,独立跑 cmd/migrate 时也能兜住。
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		log.ErrorCtx(ctx, "document: enable pgvector extension failed", err, nil)
		return fmt.Errorf("document: enable pgvector: %w: %w", err, ErrDocumentInternal)
	}

	stmts := []string{
		// ─── documents ────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS documents (
			id                  BIGINT PRIMARY KEY,
			org_id              BIGINT NOT NULL,
			source_type         VARCHAR(32) NOT NULL,
			provider            VARCHAR(32) NOT NULL,
			source_id           VARCHAR(255) NOT NULL,
			title               VARCHAR(512) NOT NULL DEFAULT '',
			mime_type           VARCHAR(64)  NOT NULL DEFAULT '',
			file_name           VARCHAR(512) NOT NULL DEFAULT '',
			version             VARCHAR(128) NOT NULL,
			oss_key             TEXT         NOT NULL DEFAULT '',
			external_ref_kind   VARCHAR(32)  NOT NULL DEFAULT '',
			external_ref_uri    TEXT         NOT NULL DEFAULT '',
			external_ref_extra  JSONB,
			uploader_id         BIGINT NOT NULL,
			acl_group_ids       BIGINT[] NOT NULL DEFAULT '{}',
			chunk_count         INT NOT NULL DEFAULT 0,
			content_byte_size   INT NOT NULL DEFAULT 0,
			last_synced_at      TIMESTAMPTZ,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// 老库 ALTER:表已存在但没 oss_key 列时补一下。IF NOT EXISTS 在 PG 9.6+ 支持,幂等安全。
		`ALTER TABLE documents ADD COLUMN IF NOT EXISTS oss_key TEXT NOT NULL DEFAULT ''`,
		// M2.2 加 knowledge_source_id:指向 MySQL sources 表(无 PG FK,跨 DB)。
		// DEFAULT 0 让历史行兼容,backfill 步骤(cmd/migrate)把 0 行回填成对应的 manual_upload source。
		`ALTER TABLE documents ADD COLUMN IF NOT EXISTS knowledge_source_id BIGINT NOT NULL DEFAULT 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_documents_org_source
			ON documents (org_id, source_type, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_org_provider_updated
			ON documents (org_id, provider, updated_at DESC)`,
		// 给 ACL 列表查询用:WHERE knowledge_source_id IN (...) 走索引
		`CREATE INDEX IF NOT EXISTS idx_documents_knowledge_source
			ON documents (knowledge_source_id)`,

		// ─── document_versions ───────────────────────────────────────────
		// 每次 upload 成功写 OSS 后插一行,handler 侧按 MaxVersionsPerDocument 裁剪最老的。
		// doc_id FK + ON DELETE CASCADE:删 doc 自动清版本记录(OSS 对象由 handler 侧显式删)。
		`CREATE TABLE IF NOT EXISTS document_versions (
			id              BIGINT PRIMARY KEY,
			doc_id          BIGINT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			org_id          BIGINT NOT NULL,
			oss_key         TEXT NOT NULL,
			version_hash    VARCHAR(128) NOT NULL,
			file_size       INT NOT NULL DEFAULT 0,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_doc_versions_doc_created
			ON document_versions (doc_id, created_at DESC)`,

		// ─── document_chunks ──────────────────────────────────────────────
		// embedding 维度参数化:%d 插值 cfg.ModelDim
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS document_chunks (
			id                BIGINT PRIMARY KEY,
			doc_id            BIGINT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			org_id            BIGINT NOT NULL,
			chunk_idx         INT NOT NULL,
			content           TEXT NOT NULL,
			content_type      VARCHAR(16) NOT NULL DEFAULT 'text',
			level             SMALLINT NOT NULL DEFAULT 0,
			heading_path      TEXT[] NOT NULL DEFAULT '{}',
			token_count       INT NOT NULL DEFAULT 0,
			embedding         VECTOR(%d),
			chunker_version   VARCHAR(32) NOT NULL DEFAULT '',
			parent_chunk_id   BIGINT,
			index_status      VARCHAR(16) NOT NULL,
			index_error       VARCHAR(255) NOT NULL DEFAULT '',
			metadata          JSONB,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		)`, embeddingDim),
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_document_chunks_doc_idx
			ON document_chunks (doc_id, chunk_idx)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_org_doc
			ON document_chunks (org_id, doc_id)`,
		// HNSW partial index:只收录 indexed 行,failed 行(embedding NULL)不进索引
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_document_chunks_hnsw
			ON document_chunks USING hnsw (embedding vector_cosine_ops)
			WITH (m = %d, ef_construction = %d)
			WHERE index_status = '%s'`,
			HNSWParamM, HNSWParamEFConstruction, ChunkIndexStatusIndexed),
	}

	for _, sql := range stmts {
		if err := db.WithContext(ctx).Exec(sql).Error; err != nil {
			log.ErrorCtx(ctx, "document: DDL exec failed", err, map[string]any{"sql_head": headN(sql, 80)})
			return fmt.Errorf("document: exec DDL: %w: %w", err, ErrDocumentInternal)
		}
	}

	// sanity check:实际列维度 vs cfg.ModelDim
	actual, err := inspectEmbeddingDim(ctx, db, log)
	if err != nil {
		log.ErrorCtx(ctx, "document: inspect embedding dim failed", err, nil)
		return fmt.Errorf("document: inspect embedding dim: %w: %w", err, ErrDocumentInternal)
	}
	if actual != embeddingDim {
		log.ErrorCtx(ctx, "document: embedding dim mismatch", nil, map[string]any{
			"table_dim": actual, "cfg_dim": embeddingDim,
		})
		return fmt.Errorf("document: embedding column is vector(%d) but cfg.ModelDim=%d: %w",
			actual, embeddingDim, ErrDimMismatch)
	}

	log.InfoCtx(ctx, "document: migrations done", map[string]any{"embedding_dim": actual})
	if onReady != nil {
		onReady()
	}
	return nil
}

// inspectEmbeddingDim 读 document_chunks.embedding 列的真实维度。
//
// 用 pg_catalog.format_type 返回形如 "vector(1536)"(pgvector 自定义类型的 typmod 语义由它掌握,
// 直接读 atttypmod 不保证稳定)。再用正则提数字。
//
// 表或列不存在时返回 error(调用方应在 CREATE TABLE 之后才调这个,否则是自己配错顺序)。
func inspectEmbeddingDim(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) (int, error) {
	var fmtType string
	err := db.WithContext(ctx).Raw(`
		SELECT format_type(atttypid, atttypmod)
		FROM pg_attribute
		WHERE attrelid = 'document_chunks'::regclass
		  AND attname  = 'embedding'
	`).Scan(&fmtType).Error
	if err != nil {
		log.ErrorCtx(ctx, "document: read embedding column type failed", err, nil)
		return 0, fmt.Errorf("read embedding column type: %w", err)
	}
	if fmtType == "" {
		log.ErrorCtx(ctx, "document: embedding column not found on document_chunks", nil, nil)
		return 0, fmt.Errorf("embedding column not found on document_chunks")
	}
	// 形如 "vector(1536)" — 提括号里的数字
	m := dimRegexp.FindStringSubmatch(fmtType)
	if len(m) != 2 {
		log.ErrorCtx(ctx, "document: unexpected embedding column type", nil, map[string]any{"fmt_type": fmtType})
		return 0, fmt.Errorf("unexpected column type %q (want vector(N))", fmtType)
	}
	var dim int
	//sayso-lint:ignore err-swallow
	if _, err := fmt.Sscanf(m[1], "%d", &dim); err != nil {
		log.ErrorCtx(ctx, "document: parse embedding dim failed", err, map[string]any{"fmt_type": fmtType})
		return 0, fmt.Errorf("parse dim from %q: %w", fmtType, err)
	}
	return dim, nil
}

var dimRegexp = regexp.MustCompile(`^vector\((\d+)\)$`)

// headN 截 SQL 头部用于错误日志,不塞整段 DDL 到日志里。
func headN(s string, n int) string {
	s = stripSpaces(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripSpaces 压缩连续空白为单空格,让 DDL 的多行字符串在日志里可读。
func stripSpaces(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
			continue
		}
		out = append(out, c)
		prevSpace = false
	}
	return string(out)
}
