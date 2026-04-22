//go:build integration

// repository_integration_test.go document repository 的集成测试。
//
// 跑法:
//
//	go test -tags=integration ./internal/document/repository -run . -v
//
// 前提:docker-compose 中 synapse-pg 容器已起并暴露 127.0.0.1:15432(与 config.dev.yaml 对齐)。
// 和本地 synapse 应用共用同一个 synapse 库,但测试数据用随机 org_id / snowflake id 隔离。
package repository_test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// testDB 懒连接 PG,失败 t.Skip(让不带 docker 的环境也能跑其他 test)。
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.PostgresConfig{
		Host:         "127.0.0.1",
		Port:         15432,
		Username:     "postgres",
		Password:     "synapse123",
		Database:     "synapse",
		SSLMode:      "disable",
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	}
	db, err := database.NewGormPostgres(cfg)
	if err != nil {
		t.Skipf("pg not available, skip: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 确保 migration 已跑(幂等,多跑一遍无副作用)。
	log := mustLogger(t)
	if err := document.RunMigrations(ctx, db, 1536, log, nil); err != nil {
		t.Fatalf("migration failed: %v", err)
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

// newTestOrg 生成随机 orgID,避免测试之间污染。
func newTestOrg() uint64 {
	return uint64(time.Now().UnixNano()) ^ uint64(rand.Uint32())
}

func newDocID() uint64 { return uint64(rand.Int63()) | 1 } // 保证非 0

// fakeVec 生成 1536 维 float32(和 config.dev.yaml 维度一致)。
func fakeVec(seed int) []float32 {
	v := make([]float32, 1536)
	for i := range v {
		v[i] = float32((seed+i)%100) / 100.0
	}
	return v
}

// ─── tests ───────────────────────────────────────────────────────────────────

func TestGetVersion_NotExists(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)

	v, err := repo.GetVersion(context.Background(), newTestOrg(), "document", "no-such-id")
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if v.Exists {
		t.Fatalf("expected not exists, got %+v", v)
	}
}

// TestUpsertWithChunks_InitialAndReplace 验证核心语义:
//
//  1. 首次 upsert 创建 doc + 插入 chunks
//  2. 相同 (org, source_type, source_id) 再 upsert 时旧 chunks 整体被清除,新 chunks 落库
//  3. 同批允许混存 indexed (Vec!=nil) + failed (Vec==nil) 两种 chunk
func TestUpsertWithChunks_InitialAndReplace(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	org := newTestOrg()
	docID := newDocID()
	sourceID := fmt.Sprintf("upload:%d", docID)

	mkDoc := func(version string) *model.Document {
		return &model.Document{
			ID:          docID,
			OrgID:       org,
			SourceType:  "document",
			Provider:    "upload",
			SourceID:    sourceID,
			Title:       "t1",
			MIMEType:    "text/markdown",
			Version:     version,
			UploaderID:  1,
			ACLGroupIDs: pq.Int64Array{},
		}
	}

	// ─── 首次 upsert ──────────────────────────────────────────────────────
	firstChunks := []repository.ChunkWithVec{
		{
			Chunk: model.DocumentChunk{
				ID:       newDocID(),
				OrgID:    org,
				ChunkIdx: 0,
				Content:  "# heading\nbody 1",
				HeadingPath: pq.StringArray{"heading"},
				ChunkerVersion: "v1",
			},
			Vec: fakeVec(1),
		},
		{
			Chunk: model.DocumentChunk{
				ID:       newDocID(),
				OrgID:    org,
				ChunkIdx: 1,
				Content:  "body 2 that failed embed",
				ChunkerVersion: "v1",
				IndexError:     "azure 400: context_length",
			},
			Vec: nil, // failed
		},
	}
	if _, err := repo.UpsertWithChunks(ctx, mkDoc("v1"), firstChunks); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	indexed, failed, err := repo.CountChunks(ctx, docID)
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if indexed != 1 || failed != 1 {
		t.Fatalf("expected 1 indexed + 1 failed, got %d indexed / %d failed", indexed, failed)
	}

	// version 查得到
	v, err := repo.GetVersion(ctx, org, "document", sourceID)
	if err != nil || !v.Exists || v.DocID != docID || v.Version != "v1" {
		t.Fatalf("get version after first: err=%v v=%+v", err, v)
	}

	// ─── 再 upsert:期望旧 chunks 被替换 ────────────────────────────────────
	secondChunks := []repository.ChunkWithVec{
		{
			Chunk: model.DocumentChunk{
				ID:       newDocID(),
				OrgID:    org,
				ChunkIdx: 0,
				Content:  "v2 only chunk",
				ChunkerVersion: "v1",
			},
			Vec: fakeVec(2),
		},
	}
	if _, err := repo.UpsertWithChunks(ctx, mkDoc("v2"), secondChunks); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	indexed, failed, err = repo.CountChunks(ctx, docID)
	if err != nil {
		t.Fatalf("count chunks v2: %v", err)
	}
	if indexed != 1 || failed != 0 {
		t.Fatalf("expected exactly 1 indexed + 0 failed after replace, got %d / %d", indexed, failed)
	}

	v, _ = repo.GetVersion(ctx, org, "document", sourceID)
	if v.Version != "v2" {
		t.Fatalf("version not updated: %q", v.Version)
	}
}

// TestDeleteByID_Cascade 验证 FK ON DELETE CASCADE:删 doc 自动清 chunks。
func TestDeleteByID_Cascade(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	org := newTestOrg()
	docID := newDocID()
	doc := &model.Document{
		ID: docID, OrgID: org, SourceType: "document", Provider: "upload",
		SourceID: fmt.Sprintf("del:%d", docID), Version: "v1", UploaderID: 1,
		ACLGroupIDs: pq.Int64Array{},
	}
	chunks := []repository.ChunkWithVec{{
		Chunk: model.DocumentChunk{ID: newDocID(), OrgID: org, ChunkIdx: 0, Content: "x", ChunkerVersion: "v1"},
		Vec:   fakeVec(3),
	}}
	if _, err := repo.UpsertWithChunks(ctx, doc, chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.DeleteByID(ctx, org, docID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 元数据没了
	if _, err := repo.GetByID(ctx, org, docID); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found after delete, got %v", err)
	}

	// chunks 也没了(靠 FK CASCADE,不是靠 repo 自己删)
	var cnt int64
	db.WithContext(ctx).Table("document_chunks").Where("doc_id = ?", docID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("cascade failed: %d chunks remain", cnt)
	}
}

// TestDeleteBySourceID_Cascade 同上,但用源端幂等键删。
func TestDeleteBySourceID_Cascade(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	org := newTestOrg()
	docID := newDocID()
	sourceID := fmt.Sprintf("tomb:%d", docID)
	doc := &model.Document{
		ID: docID, OrgID: org, SourceType: "document", Provider: "feishu",
		SourceID: sourceID, Version: "v1", UploaderID: 1,
		ACLGroupIDs: pq.Int64Array{},
	}
	chunks := []repository.ChunkWithVec{{
		Chunk: model.DocumentChunk{ID: newDocID(), OrgID: org, ChunkIdx: 0, Content: "x", ChunkerVersion: "v1"},
		Vec:   fakeVec(4),
	}}
	if _, err := repo.UpsertWithChunks(ctx, doc, chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.DeleteBySourceID(ctx, org, "document", sourceID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	v, _ := repo.GetVersion(ctx, org, "document", sourceID)
	if v.Exists {
		t.Fatalf("expected deleted, but GetVersion still exists")
	}
}

// TestListByOrg_KeysetPaging 快速路径:写几条 → ListByOrg 按 id DESC + cursor 分页。
func TestListByOrg_KeysetPaging(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	org := newTestOrg()
	ids := []uint64{newDocID(), newDocID(), newDocID()}
	// 保证 ids 递增,方便断言
	for i := range ids {
		ids[i] = uint64(time.Now().UnixNano()) + uint64(i*10)
	}
	for i, id := range ids {
		doc := &model.Document{
			ID: id, OrgID: org, SourceType: "document", Provider: "upload",
			SourceID: fmt.Sprintf("page-%d-%d", id, i), Version: "v1", UploaderID: 1,
			ACLGroupIDs: pq.Int64Array{},
		}
		if _, err := repo.UpsertWithChunks(ctx, doc, nil); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// 第一页 limit=2
	page1, err := repo.ListByOrg(ctx, org, repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if page1[0].ID < page1[1].ID {
		t.Fatalf("not desc: %d, %d", page1[0].ID, page1[1].ID)
	}

	// 第二页 BeforeID = 最后一个
	page2, err := repo.ListByOrg(ctx, org, repository.ListOptions{Limit: 2, BeforeID: page1[1].ID})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}
}
