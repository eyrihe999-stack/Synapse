//go:build integration

// persister 集成测试。跑法:
//
//	go test -tags=integration ./internal/ingestion/persister/document -v
//
// 前提:synapse-pg 容器在 127.0.0.1:15432,已经跑过 document.RunMigrations(测试里再跑一次幂等)。
// 不依赖真实 embedding provider,向量手构造。
package document_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	docpersister "github.com/eyrihe999-stack/Synapse/internal/ingestion/persister/document"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/idgen"
	"gorm.io/gorm"
)

const testEmbeddingDim = 1536

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewGormPostgres(&config.PostgresConfig{
		Host: "127.0.0.1", Port: 15432, Username: "postgres", Password: "synapse123",
		Database: "synapse", SSLMode: "disable",
		MaxOpenConns: 5, MaxIdleConns: 2,
	})
	if err != nil {
		t.Skipf("pg not available: %v", err)
	}
	log := mustLogger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := document.RunMigrations(ctx, db, testEmbeddingDim, log, nil); err != nil {
		t.Fatalf("migration: %v", err)
	}
	return db
}

func mustLogger(t *testing.T) logger.LoggerInterface {
	t.Helper()
	l, err := logger.GetLogger(&config.LogConfig{Level: "error", Format: "text", Output: "stdout"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return l
}

// snowflake 在本进程内要 init 一次,多测试共享。
var snowflakeInitOnce bool

func mustInitSnowflake(t *testing.T) {
	t.Helper()
	if snowflakeInitOnce {
		return
	}
	if err := idgen.InitSnowflake(idgen.SnowflakeConfig{}); err != nil {
		t.Fatalf("init snowflake: %v", err)
	}
	snowflakeInitOnce = true
}

// fakeVec 单位化 float32 向量(任意内容,只要维度对就能写 vector(1536))。
func fakeVec(seed int) []float32 {
	v := make([]float32, testEmbeddingDim)
	for i := range v {
		v[i] = float32((seed+i)%97) / 97.0
	}
	return v
}

func newTestOrg() uint64 {
	return uint64(time.Now().UnixNano()) ^ uint64(rand.Uint32())
}

// buildNormalizedDoc 造一个最小的 NormalizedDoc。
func buildNormalizedDoc(org uint64, sourceID string) *ingestion.NormalizedDoc {
	return &ingestion.NormalizedDoc{
		OrgID:      org,
		SourceType: ingestion.SourceTypeDocument,
		SourceID:   sourceID,
		Title:      "test doc",
		MIMEType:   "text/markdown",
		Version:    "v1",
		UploaderID: 100,
		Payload: &docpersister.Payload{
			Provider:        "upload",
			ExternalOwnerID: "user-x",
			ProviderMeta:    map[string]string{"note": "test"},
		},
		Content: []byte("hello world"),
	}
}

// TestPersist_HappyPath_AllIndexed 正常路径:vecs 都给 → chunks 全 indexed。
func TestPersist_HappyPath_AllIndexed(t *testing.T) {
	mustInitSnowflake(t)
	db := testDB(t)
	repo := docrepo.New(db)
	p, err := docpersister.New(repo, mustLogger(t))
	if err != nil {
		t.Fatalf("docpersister.New: %v", err)
	}
	ctx := context.Background()

	org := newTestOrg()
	nd := buildNormalizedDoc(org, fmt.Sprintf("s-%d", time.Now().UnixNano()))

	parentIdx := 0
	chunks := []ingestion.IngestedChunk{
		{Index: 0, Content: "# 架构", ContentType: "heading", Level: 1, HeadingPath: []string{"架构"}, ChunkerVersion: "v1"},
		{Index: 1, Content: "body 1", ContentType: "text", HeadingPath: []string{"架构"}, ParentIndex: &parentIdx, ChunkerVersion: "v1"},
		{Index: 2, Content: "body 2", ContentType: "text", HeadingPath: []string{"架构"}, ParentIndex: &parentIdx, ChunkerVersion: "v1"},
	}
	vecs := [][]float32{fakeVec(1), fakeVec(2), fakeVec(3)}

	if err := p.Persist(ctx, nd, chunks, vecs, nil); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// 验证 documents 行
	v, err := repo.GetVersion(ctx, org, nd.SourceType, nd.SourceID)
	if err != nil || !v.Exists {
		t.Fatalf("get version: err=%v v=%+v", err, v)
	}

	// 验证 chunks 全 indexed,embedding 非 NULL
	indexed, failed, err := repo.CountChunks(ctx, v.DocID)
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if indexed != 3 || failed != 0 {
		t.Fatalf("chunks: indexed=%d failed=%d, want 3/0", indexed, failed)
	}

	// 验证 embedding 列真的被填了(non-NULL)— 取一行看
	var embText string
	err = db.Raw(`SELECT embedding::text FROM document_chunks WHERE doc_id = ? ORDER BY chunk_idx LIMIT 1`, v.DocID).
		Scan(&embText).Error
	if err != nil {
		t.Fatalf("read embedding: %v", err)
	}
	if embText == "" || embText[0] != '[' {
		t.Fatalf("embedding not stored as vector literal: %q", embText)
	}

	// 验证 parent_chunk_id 回填成功(child chunks 应指向 heading chunk 的真实 id)
	type parentRow struct {
		ChunkIdx      int
		ParentChunkID *uint64
	}
	var parents []parentRow
	err = db.Raw(`SELECT chunk_idx, parent_chunk_id FROM document_chunks WHERE doc_id = ? ORDER BY chunk_idx`, v.DocID).
		Scan(&parents).Error
	if err != nil {
		t.Fatalf("read parents: %v", err)
	}
	if len(parents) != 3 {
		t.Fatalf("parents rows = %d, want 3", len(parents))
	}
	if parents[0].ParentChunkID != nil {
		t.Errorf("heading chunk should have nil parent, got %v", parents[0].ParentChunkID)
	}
	if parents[1].ParentChunkID == nil || parents[2].ParentChunkID == nil {
		t.Errorf("child chunks should have parent, got [1]=%v [2]=%v", parents[1].ParentChunkID, parents[2].ParentChunkID)
	}
	if parents[1].ParentChunkID != nil && parents[2].ParentChunkID != nil &&
		*parents[1].ParentChunkID != *parents[2].ParentChunkID {
		t.Errorf("children should share parent, got %v vs %v", *parents[1].ParentChunkID, *parents[2].ParentChunkID)
	}
}

// TestPersist_NonFatalEmbedFailure embedErr != nil 时:chunks 落 failed,embedding NULL,
// index_error 非空,persister 不返 error。
func TestPersist_NonFatalEmbedFailure(t *testing.T) {
	mustInitSnowflake(t)
	db := testDB(t)
	repo := docrepo.New(db)
	p, err := docpersister.New(repo, mustLogger(t))
	if err != nil {
		t.Fatalf("docpersister.New: %v", err)
	}
	ctx := context.Background()

	org := newTestOrg()
	nd := buildNormalizedDoc(org, fmt.Sprintf("fail-%d", time.Now().UnixNano()))
	chunks := []ingestion.IngestedChunk{
		{Index: 0, Content: "a", ContentType: "text", ChunkerVersion: "v1"},
		{Index: 1, Content: "b", ContentType: "text", ChunkerVersion: "v1"},
	}
	azureErr := errors.New("azure 400: context_length_exceeded (8192)")

	// vecs 为 nil,embedErr 非 nil 非 fatal
	if err := p.Persist(ctx, nd, chunks, nil, azureErr); err != nil {
		t.Fatalf("persist should swallow non-fatal err, got %v", err)
	}

	v, _ := repo.GetVersion(ctx, org, nd.SourceType, nd.SourceID)
	if !v.Exists {
		t.Fatal("doc row not written")
	}

	indexed, failed, err := repo.CountChunks(ctx, v.DocID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if indexed != 0 || failed != 2 {
		t.Fatalf("indexed=%d failed=%d, want 0/2", indexed, failed)
	}

	// embedding 列应为 NULL
	type row struct {
		Embedding  *string
		IndexError string
	}
	var rows []row
	err = db.Raw(`SELECT embedding::text AS embedding, index_error FROM document_chunks WHERE doc_id = ?`, v.DocID).
		Scan(&rows).Error
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i, r := range rows {
		if r.Embedding != nil {
			t.Errorf("chunk %d embedding should be NULL, got %q", i, *r.Embedding)
		}
		if r.IndexError == "" {
			t.Errorf("chunk %d index_error should be non-empty", i)
		}
	}
}

// TestPersist_SecondCallReplacesChunks 同 source_id 再 persist,旧 chunks 被清掉,新 chunks 落库。
func TestPersist_SecondCallReplacesChunks(t *testing.T) {
	mustInitSnowflake(t)
	db := testDB(t)
	repo := docrepo.New(db)
	p, err := docpersister.New(repo, mustLogger(t))
	if err != nil {
		t.Fatalf("docpersister.New: %v", err)
	}
	ctx := context.Background()

	org := newTestOrg()
	sid := fmt.Sprintf("rep-%d", time.Now().UnixNano())

	first := buildNormalizedDoc(org, sid)
	firstChunks := []ingestion.IngestedChunk{
		{Index: 0, Content: "a", ContentType: "text", ChunkerVersion: "v1"},
		{Index: 1, Content: "b", ContentType: "text", ChunkerVersion: "v1"},
		{Index: 2, Content: "c", ContentType: "text", ChunkerVersion: "v1"},
	}
	firstVecs := [][]float32{fakeVec(10), fakeVec(11), fakeVec(12)}
	if err := p.Persist(ctx, first, firstChunks, firstVecs, nil); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	v1, _ := repo.GetVersion(ctx, org, first.SourceType, first.SourceID)

	second := buildNormalizedDoc(org, sid)
	second.Version = "v2"
	secondChunks := []ingestion.IngestedChunk{
		{Index: 0, Content: "only one", ContentType: "text", ChunkerVersion: "v1"},
	}
	secondVecs := [][]float32{fakeVec(20)}
	if err := p.Persist(ctx, second, secondChunks, secondVecs, nil); err != nil {
		t.Fatalf("second persist: %v", err)
	}

	// doc id 不变(upsert 复用)
	v2, _ := repo.GetVersion(ctx, org, second.SourceType, second.SourceID)
	if v2.DocID != v1.DocID {
		t.Errorf("doc id changed: %d → %d", v1.DocID, v2.DocID)
	}
	if v2.Version != "v2" {
		t.Errorf("version not updated: %q", v2.Version)
	}

	indexed, failed, _ := repo.CountChunks(ctx, v2.DocID)
	if indexed != 1 || failed != 0 {
		t.Errorf("chunks after replace: indexed=%d failed=%d, want 1/0", indexed, failed)
	}
}
