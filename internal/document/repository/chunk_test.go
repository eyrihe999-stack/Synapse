// chunk_test.go 是 pgvector repository 层的集成测试,真连 Postgres。
//
// ─── 安全协议:测试库必须和 dev/prod 库隔离 ───────────────────────────────────
//
// 默认跳过(不设 INTEGRATION_PG=1 就不跑)。即使启用,也必须跑在一个**独立**的 PG 数据库里;
// 硬拦:如果 PG_TEST_DATABASE = "synapse"(dev/prod 库名),测试立即 Fatal,绝不 TRUNCATE 那边。
//
// 默认使用 `synapse_test` 库;不存在时自动 CREATE DATABASE(需要连 pg 的 `postgres` 管理库做这一步)。
// 环境变量(都带 PG_TEST_ 前缀,和后端运行时用的 PG_* 一刀两断):
//
//	INTEGRATION_PG        = "1" 才会实际跑,否则全 Skip
//	PG_TEST_HOST          (默认 127.0.0.1)
//	PG_TEST_PORT          (默认 15432)
//	PG_TEST_USERNAME      (默认 postgres —— 必须有 CREATE DATABASE 权限以便首次建库)
//	PG_TEST_PASSWORD      (默认 synapse123)
//	PG_TEST_DATABASE      (默认 synapse_test —— ※ 不能是 "synapse")
//
// 每个测试开始前 TRUNCATE document_chunks,测试间互不污染。
//
// ─── MySQL 侧同类约定(前瞻性规范) ──────────────────────────────────────────
//
// 目前仓库里没有 MySQL 集成测试。如果将来要加(例如 document_test.go 针对 Repository.CreateDocument 等),
// 必须采用同样的隔离策略:
//
//	INTEGRATION_MYSQL     = "1" 才会实际跑
//	MYSQL_TEST_DATABASE   (默认 synapse_test —— 不能是 "synapse")
//
// 并在 setup helper 里做相同的硬拦 + 自动建库。这条注释存在于此就是约定文档,新集成测试的作者必须遵守。
package repository

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
)

// prodPGDatabase 是明令禁止连的 dev/prod 库名;测试不得对这个库 TRUNCATE,否则就是事故。
const prodPGDatabase = "synapse"

func setupChunkRepo(t *testing.T) (ChunkRepository, *gorm.DB) {
	t.Helper()
	if os.Getenv("INTEGRATION_PG") != "1" {
		t.Skip("set INTEGRATION_PG=1 to run pg repository tests (uses synapse_test db by default)")
	}
	host := envOr("PG_TEST_HOST", "127.0.0.1")
	port := envInt("PG_TEST_PORT", 15432)
	user := envOr("PG_TEST_USERNAME", "postgres")
	pass := envOr("PG_TEST_PASSWORD", "synapse123")
	dbn := envOr("PG_TEST_DATABASE", "synapse_test")

	// 硬拦:绝不允许测试指向 dev/prod 库。这是本轮事故(document_chunks 被 TRUNCATE)的根因修复点。
	if dbn == prodPGDatabase {
		t.Fatalf("refusing to run integration tests against PG database %q (= dev/prod db). "+
			"set PG_TEST_DATABASE to something else (recommended: synapse_test)", dbn)
	}
	// CREATE DATABASE 不支持参数化绑定,只能字符串拼接;先收紧 name 到 [a-z0-9_]+,防注入。
	if !isSafePGDBName(dbn) {
		t.Fatalf("invalid test db name %q (allowed chars: [a-z0-9_])", dbn)
	}

	// 不存在就自动建。需要连 pg 内置 `postgres` 管理库做 CREATE DATABASE。
	if err := ensurePGDatabase(host, port, user, pass, dbn); err != nil {
		t.Fatalf("ensure test db %q: %v", dbn, err)
	}

	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable connect_timeout=5 TimeZone=UTC",
		host, port, user, pass, dbn)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connect pg test db %q: %v", dbn, err)
	}

	// 保证扩展 + 表 + HNSW + BM25 content_tsv 都就绪。首次跑(新建的 synapse_test 是空的)全走,后续 no-op。
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		t.Fatalf("enable pgvector: %v", err)
	}
	if err := db.AutoMigrate(&model.DocumentChunk{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_document_chunks_embedding_hnsw
		ON document_chunks USING hnsw (embedding vector_cosine_ops)`).Error; err != nil {
		t.Fatalf("create hnsw: %v", err)
	}
	// T1.1 BM25 通路的 content_tsv(GENERATED)列 + GIN 索引 —— GORM AutoMigrate 不懂 tsvector,
	// 必须走 raw SQL。这段和 internal/document/migration.go ensurePgStructuralIndexes 保持语义一致。
	if err := db.Exec(`DO $$
		BEGIN
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
		END $$`).Error; err != nil {
		t.Fatalf("create content_tsv: %v", err)
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_document_chunks_content_tsv_gin
		ON document_chunks USING GIN (content_tsv)`).Error; err != nil {
		t.Fatalf("create content_tsv GIN: %v", err)
	}

	// 干净起跑:每个 test 独占 document_chunks 表。因为走的是 synapse_test 库,TRUNCATE 不会误伤 dev。
	if err := db.Exec("TRUNCATE TABLE document_chunks RESTART IDENTITY").Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}

	return NewChunkRepository(db), db
}

// isSafePGDBName 限制测试库名只含 [a-z0-9_]:CREATE DATABASE 拼接前必须过这一关,防止恶意/失手注入。
// 常规测试库名如 "synapse_test" / "foo_test_1" 都符合。
func isSafePGDBName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// ensurePGDatabase 连 pg 内置 "postgres" 管理库,查 pg_database;
// 目标库不存在则 CREATE DATABASE,存在则返回 nil。
//
// 连管理库需要 PG_TEST_USERNAME 有对应权限;默认 `postgres` 是超级用户,dev 环境天然满足。
// 生产/CI 里应提前准备好测试库,避免测试运行时动态建库带来的权限复杂度。
func ensurePGDatabase(host string, port int, user, pass, dbn string) error {
	adminDSN := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable connect_timeout=5",
		host, port, user, pass,
	)
	admin, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return fmt.Errorf("connect pg admin db: %w", err)
	}
	defer func() {
		if sqlDB, e := admin.DB(); e == nil {
			//sayso-lint:ignore err-swallow
			_ = sqlDB.Close()
		}
	}()

	var count int64
	if err := admin.Raw("SELECT COUNT(*) FROM pg_database WHERE datname = ?", dbn).Scan(&count).Error; err != nil {
		return fmt.Errorf("check pg_database: %w", err)
	}
	if count > 0 {
		return nil
	}
	// CREATE DATABASE 不接 $1 绑定,只能拼。name 已由 isSafePGDBName 过滤。
	if err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s", dbn)).Error; err != nil {
		return fmt.Errorf("create database %s: %w", dbn, err)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// makeVec 返回一个 1536 维向量,第 i 位为 base+i*step,其余 0,便于构造语义上"相近/相远"的样本。
func makeVec(base, step float32) []float32 {
	v := make([]float32, document.ChunkEmbeddingDim)
	for i := range v {
		v[i] = base + float32(i)*step
	}
	return v
}

// unit 规范化向量到 L2=1,让 cosine distance 与 OpenAI embedding 的空间接近(便于验证语义)。
func unit(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1.0 / sqrtF64(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// sqrtF64 避开 math 导入,本文件只这一处用,节省 import。
func sqrtF64(x float64) float64 {
	if x == 0 {
		return 0
	}
	z := x
	for range 20 {
		z = (z + x/z) / 2
	}
	return z
}

// ─── 测试用例 ────────────────────────────────────────────────────────────────

func TestChunkRepo_InsertAndDelete(t *testing.T) {
	repo, db := setupChunkRepo(t)
	ctx := context.Background()

	chunks := []*model.DocumentChunk{
		{DocID: 100, OrgID: 1, ChunkIdx: 0, Content: "alpha", ContentHash: "h0", IndexStatus: document.ChunkIndexStatusPending},
		{DocID: 100, OrgID: 1, ChunkIdx: 1, Content: "beta", ContentHash: "h1", IndexStatus: document.ChunkIndexStatusPending},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, c := range chunks {
		if c.ID == 0 {
			t.Errorf("expected ID filled after insert, got 0")
		}
	}

	var cnt int64
	db.Model(&model.DocumentChunk{}).Where("doc_id = ?", 100).Count(&cnt)
	if cnt != 2 {
		t.Errorf("count after insert = %d, want 2", cnt)
	}

	if err := repo.DeleteChunksByDocID(ctx, 100); err != nil {
		t.Fatalf("delete: %v", err)
	}
	db.Model(&model.DocumentChunk{}).Where("doc_id = ?", 100).Count(&cnt)
	if cnt != 0 {
		t.Errorf("count after delete = %d, want 0", cnt)
	}
}

func TestChunkRepo_UpdateEmbedding(t *testing.T) {
	repo, db := setupChunkRepo(t)
	ctx := context.Background()

	c := &model.DocumentChunk{DocID: 1, OrgID: 1, ChunkIdx: 0, Content: "x", ContentHash: "h", IndexStatus: document.ChunkIndexStatusPending}
	if err := repo.InsertChunks(ctx, []*model.DocumentChunk{c}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	vec := unit(makeVec(1.0, 0.0)) // 所有维度相同的正向量,单位化
	if err := repo.UpdateChunkEmbedding(ctx, c.ID, vec, "azure-test"); err != nil {
		t.Fatalf("update embedding: %v", err)
	}

	var got model.DocumentChunk
	if err := db.First(&got, c.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.IndexStatus != document.ChunkIndexStatusIndexed {
		t.Errorf("status = %q, want indexed", got.IndexStatus)
	}
	if got.EmbeddingModel != "azure-test" {
		t.Errorf("model = %q, want azure-test", got.EmbeddingModel)
	}
	if got.Embedding == nil {
		t.Fatalf("embedding still nil after update")
	}
	if len(got.Embedding.Slice()) != document.ChunkEmbeddingDim {
		t.Errorf("stored dim = %d, want %d", len(got.Embedding.Slice()), document.ChunkEmbeddingDim)
	}
}

func TestChunkRepo_UpdateEmbedding_DimMismatch(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	// 不必插行,dim 校验在写库前完成。
	err := repo.UpdateChunkEmbedding(context.Background(), 1, []float32{1, 2, 3}, "x")
	if err == nil {
		t.Fatal("expected error for dim mismatch, got nil")
	}
}

func TestChunkRepo_MarkFailed(t *testing.T) {
	repo, db := setupChunkRepo(t)
	ctx := context.Background()

	c := &model.DocumentChunk{DocID: 1, OrgID: 1, ChunkIdx: 0, Content: "x", ContentHash: "h", IndexStatus: document.ChunkIndexStatusPending}
	if err := repo.InsertChunks(ctx, []*model.DocumentChunk{c}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := repo.MarkChunkFailed(ctx, c.ID, "azure 429 quota exceeded"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	var got model.DocumentChunk
	db.First(&got, c.ID)
	if got.IndexStatus != document.ChunkIndexStatusFailed {
		t.Errorf("status = %q, want failed", got.IndexStatus)
	}
	if got.IndexError == "" {
		t.Errorf("index_error empty, want message")
	}
}

func TestChunkRepo_MarkFailed_Truncation(t *testing.T) {
	repo, db := setupChunkRepo(t)
	ctx := context.Background()

	c := &model.DocumentChunk{DocID: 1, OrgID: 1, ChunkIdx: 0, Content: "x", ContentHash: "h", IndexStatus: document.ChunkIndexStatusPending}
	_ = repo.InsertChunks(ctx, []*model.DocumentChunk{c})

	// 构造 2KB 错误消息,应被截断到 1KB。
	longMsg := make([]byte, 2000)
	for i := range longMsg {
		longMsg[i] = 'A'
	}
	if err := repo.MarkChunkFailed(ctx, c.ID, string(longMsg)); err != nil {
		t.Fatalf("mark: %v", err)
	}

	var got model.DocumentChunk
	db.First(&got, c.ID)
	if len(got.IndexError) != maxIndexErrorLen {
		t.Errorf("index_error len = %d, want %d", len(got.IndexError), maxIndexErrorLen)
	}
	if got.IndexError[len(got.IndexError)-3:] != "..." {
		t.Errorf("truncation suffix missing")
	}
}

func TestChunkRepo_SearchByVector(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	ctx := context.Background()

	// 三个 chunk,向量空间上构造"相似度顺序":
	//   near :和 query 同方向 → 距离 ≈ 0
	//   mid  :部分相似
	//   far  :大致正交
	query := unit(makeVec(1, 0))
	nearVec := unit(makeVec(1, 0))           // 和 query 完全重合
	midVec := unit(makeVec(1, 0.001))        // 微偏
	farVec := unit(makeVec(0, 1))            // 完全不同(维度 0 为 0、其余递增)

	chunks := []*model.DocumentChunk{
		{DocID: 1, OrgID: 7, ChunkIdx: 0, Content: "near", ContentHash: "h0", IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t", Embedding: ptrVec(nearVec)},
		{DocID: 1, OrgID: 7, ChunkIdx: 1, Content: "mid", ContentHash: "h1", IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t", Embedding: ptrVec(midVec)},
		{DocID: 1, OrgID: 7, ChunkIdx: 2, Content: "far", ContentHash: "h2", IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t", Embedding: ptrVec(farVec)},
		// 不同 org 的 chunk 不应出现在结果里
		{DocID: 2, OrgID: 8, ChunkIdx: 0, Content: "other-org", ContentHash: "h3", IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t", Embedding: ptrVec(nearVec)},
		// pending 状态的 chunk 不应出现
		{DocID: 1, OrgID: 7, ChunkIdx: 3, Content: "pending", ContentHash: "h4", IndexStatus: document.ChunkIndexStatusPending, Embedding: ptrVec(nearVec)},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("insert: %v", err)
	}

	hits, err := repo.SearchByVector(ctx, 7, query, 3, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3 (only indexed+org=7 rows)", len(hits))
	}
	// 距离升序:near < mid < far
	if !(hits[0].Distance <= hits[1].Distance && hits[1].Distance <= hits[2].Distance) {
		t.Errorf("distances not sorted asc: %v %v %v", hits[0].Distance, hits[1].Distance, hits[2].Distance)
	}
	if hits[0].Chunk.Content != "near" {
		t.Errorf("closest hit = %q, want near", hits[0].Chunk.Content)
	}
	for _, h := range hits {
		if h.Chunk.OrgID != 7 {
			t.Errorf("org leak: got org_id=%d", h.Chunk.OrgID)
		}
		if h.Chunk.IndexStatus != document.ChunkIndexStatusIndexed {
			t.Errorf("unindexed row leaked: status=%s", h.Chunk.IndexStatus)
		}
	}
}

func TestChunkRepo_SearchByVector_EmptyTopK(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	hits, err := repo.SearchByVector(context.Background(), 1, unit(makeVec(1, 0)), 0, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("topK=0 returned %d hits", len(hits))
	}
}

// BM25 通路端到端:验证 ts_rank_cd 对 tsv_tokens 的匹配和排序。
// 三条 chunk 各存不同字符串,用关键词查询应该排出预期的顺序。
func TestChunkRepo_SearchByBM25(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	ctx := context.Background()

	// content_tsv 是 GENERATED column,由 PG 从 tsv_tokens 算出来。
	// 测试里只写 tsv_tokens,PG 自动维护 content_tsv。
	chunks := []*model.DocumentChunk{
		{
			DocID: 1, OrgID: 11, ChunkIdx: 0, Content: "alipay overview",
			ContentHash: "h1", IndexStatus: document.ChunkIndexStatusIndexed,
			TsvTokens: "alipay 模块 架构 tradeprecreate 支付宝",
		},
		{
			DocID: 2, OrgID: 11, ChunkIdx: 0, Content: "stripe overview",
			ContentHash: "h2", IndexStatus: document.ChunkIndexStatusIndexed,
			TsvTokens: "stripe 模块 架构 checkout webhook 支付",
		},
		{
			DocID: 3, OrgID: 11, ChunkIdx: 0, Content: "auth overview",
			ContentHash: "h3", IndexStatus: document.ChunkIndexStatusIndexed,
			TsvTokens: "auth 模块 架构 oauth 登录",
		},
		// 不同 org / 非 indexed:应该都被过滤掉。
		{
			DocID: 4, OrgID: 12, ChunkIdx: 0, Content: "other-org",
			ContentHash: "h4", IndexStatus: document.ChunkIndexStatusIndexed,
			TsvTokens: "tradeprecreate alipay",
		},
		{
			DocID: 5, OrgID: 11, ChunkIdx: 0, Content: "pending",
			ContentHash: "h5", IndexStatus: document.ChunkIndexStatusPending,
			TsvTokens: "tradeprecreate alipay",
		},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Case 1:查 "tradeprecreate" 只应命中 alipay chunk(docID=1),跨 org + pending 都被过滤。
	hits, err := repo.SearchByBM25(ctx, 11, "tradeprecreate", 10, nil)
	if err != nil {
		t.Fatalf("bm25: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (alipay only)", len(hits))
	}
	if hits[0].Chunk.DocID != 1 {
		t.Errorf("wrong hit: doc_id=%d, want 1", hits[0].Chunk.DocID)
	}

	// Case 2:查 "架构 模块" —— 三个都有这两个 token(tsv_tokens 里是精确 token,
	// 不做二次分词,所以 "支付" 和 "支付宝" 是不同的 token;这里挑都确实出现的 token)。
	// ts_rank_cd 会按词频 / 位置给分,返回 3 条。
	hits, err = repo.SearchByBM25(ctx, 11, "架构 模块", 10, nil)
	if err != nil {
		t.Fatalf("bm25: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3 (all three chunks have 架构+模块)", len(hits))
	}

	// Case 3:空 query 返空。
	hits, err = repo.SearchByBM25(ctx, 11, "", 10, nil)
	if err != nil {
		t.Fatalf("bm25 empty: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty query should return 0 hits, got %d", len(hits))
	}

	// Case 4:filter 透传 —— 只搜 docID=2(stripe),查 "架构"(只有 stripe + auth + alipay 都有这个词),
	// 但 doc_id filter 收到 stripe 一个。
	hits, err = repo.SearchByBM25(ctx, 11, "架构", 10, &ChunkSearchFilter{DocIDs: []uint64{2}})
	if err != nil {
		t.Fatalf("bm25 with filter: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.DocID != 2 {
		t.Fatalf("doc_id filter: got %d hits, want 1 stripe", len(hits))
	}
}

// Filter 路径端到端:证明 metadata @> 和 doc_id IN 真的下沉到 SQL,不是在 Go 侧过滤。
// 构造同 org 三篇文档,每篇一个 chunk,各自 heading_path 不同。按 heading_path / doc_id 过滤后
// 应只命中期望的子集。
func TestChunkRepo_SearchByVector_Filter(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	ctx := context.Background()

	query := unit(makeVec(1, 0))
	nearVec := unit(makeVec(1, 0))

	chunks := []*model.DocumentChunk{
		{
			DocID: 101, OrgID: 9, ChunkIdx: 0, Content: "stripe", ContentHash: "h1",
			IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t",
			Embedding: ptrVec(nearVec),
			Metadata:  datatypes.JSON(`{"heading_path":["支付","Stripe"]}`),
		},
		{
			DocID: 102, OrgID: 9, ChunkIdx: 0, Content: "alipay", ContentHash: "h2",
			IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t",
			Embedding: ptrVec(nearVec),
			Metadata:  datatypes.JSON(`{"heading_path":["支付","Alipay"]}`),
		},
		{
			DocID: 103, OrgID: 9, ChunkIdx: 0, Content: "auth", ContentHash: "h3",
			IndexStatus: document.ChunkIndexStatusIndexed, EmbeddingModel: "t",
			Embedding: ptrVec(nearVec),
			Metadata:  datatypes.JSON(`{"heading_path":["认证"]}`),
		},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Case 1:heading_path contains "支付" → 命中 2 篇(stripe, alipay),排除 auth。
	hits, err := repo.SearchByVector(ctx, 9, query, 10, &ChunkSearchFilter{
		HeadingPathContains: []string{"支付"},
	})
	if err != nil {
		t.Fatalf("filter by heading: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("heading filter: got %d hits, want 2 (stripe+alipay)", len(hits))
	}
	for _, h := range hits {
		if h.Chunk.Content == "auth" {
			t.Errorf("auth leaked through heading filter")
		}
	}

	// Case 2:heading_path contains ["支付","Stripe"] → AND 语义,只命中 stripe 一篇。
	hits, err = repo.SearchByVector(ctx, 9, query, 10, &ChunkSearchFilter{
		HeadingPathContains: []string{"支付", "Stripe"},
	})
	if err != nil {
		t.Fatalf("filter by heading AND: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.Content != "stripe" {
		t.Fatalf("heading AND filter: got %d hits first=%q, want 1 stripe",
			len(hits), func() string {
				if len(hits) > 0 {
					return hits[0].Chunk.Content
				}
				return ""
			}())
	}

	// Case 3:doc_id IN [101,103] → 命中 stripe + auth,排除 alipay。
	hits, err = repo.SearchByVector(ctx, 9, query, 10, &ChunkSearchFilter{
		DocIDs: []uint64{101, 103},
	})
	if err != nil {
		t.Fatalf("filter by doc_ids: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("doc_id filter: got %d hits, want 2", len(hits))
	}
	for _, h := range hits {
		if h.Chunk.Content == "alipay" {
			t.Errorf("alipay leaked through doc_id filter")
		}
	}

	// Case 4:两种条件 AND —— heading "支付" + doc_id 101 → 只 stripe。
	hits, err = repo.SearchByVector(ctx, 9, query, 10, &ChunkSearchFilter{
		HeadingPathContains: []string{"支付"},
		DocIDs:              []uint64{101},
	})
	if err != nil {
		t.Fatalf("filter combined: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.Content != "stripe" {
		t.Fatalf("combined filter: got %d hits, want 1 stripe", len(hits))
	}

	// Case 5:空 filter 和 nil filter 等价,都返 3 条。
	hitsNil, _ := repo.SearchByVector(ctx, 9, query, 10, nil)
	hitsEmpty, _ := repo.SearchByVector(ctx, 9, query, 10, &ChunkSearchFilter{})
	if len(hitsNil) != 3 || len(hitsEmpty) != 3 {
		t.Errorf("no-op filter broke: nil=%d empty=%d, want 3/3", len(hitsNil), len(hitsEmpty))
	}
}

func TestChunkRepo_ListPending(t *testing.T) {
	repo, _ := setupChunkRepo(t)
	ctx := context.Background()

	chunks := []*model.DocumentChunk{
		{DocID: 1, OrgID: 1, ChunkIdx: 0, Content: "p", ContentHash: "h0", IndexStatus: document.ChunkIndexStatusPending},
		{DocID: 1, OrgID: 1, ChunkIdx: 1, Content: "f", ContentHash: "h1", IndexStatus: document.ChunkIndexStatusFailed},
		{DocID: 1, OrgID: 1, ChunkIdx: 2, Content: "i", ContentHash: "h2", IndexStatus: document.ChunkIndexStatusIndexed,
			EmbeddingModel: "t", Embedding: ptrVec(unit(makeVec(1, 0)))},
	}
	_ = repo.InsertChunks(ctx, chunks)

	got, err := repo.ListPendingChunks(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (pending+failed, not indexed)", len(got))
	}
	// id 升序 → pending 那条先于 failed(插入顺序即 id 顺序)
	if got[0].IndexStatus != document.ChunkIndexStatusPending {
		t.Errorf("first status = %q, want pending", got[0].IndexStatus)
	}
}

// ptrVec 小工具:pgvector.Vector 取址,省掉每处一行临时变量。
func ptrVec(v []float32) *pgvector.Vector {
	vec := pgvector.NewVector(v)
	return &vec
}
