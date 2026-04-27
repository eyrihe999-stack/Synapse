//go:build integration

// document_integration_test.go channel 共享文档(PR #9')repository 集成测。
//
// 跑法:
//
//	go test -tags=integration ./internal/channel/repository -run ChannelDocument -v
//
// 前提:docker-compose 中 synapse-mysql 容器已起并暴露 127.0.0.1:13306。
package repository_test

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.MySQLConfig{
		Host: "127.0.0.1", Port: 13306,
		Username: "root", Password: "123456", Database: "synapse",
		MaxOpenConns: 10, MaxIdleConns: 4,
	}
	db, err := database.NewGormMySQL(cfg)
	if err != nil {
		t.Skipf("mysql not available: %v", err)
	}
	log := mustLogger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := channel.RunMigrations(ctx, db, log, nil); err != nil {
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

func randID() uint64 { return uint64(rand.Uint32())<<16 | uint64(rand.Uint32()&0xffff) | 1 }

// seedDocument 直接写一行 channel_documents 用于测试 —— 不走 service,跳过 channel/member 准备。
// 用随机 channelID/orgID 避免污染。
func seedDocument(t *testing.T, db *gorm.DB) *model.ChannelDocument {
	t.Helper()
	now := time.Now().UTC()
	doc := &model.ChannelDocument{
		ChannelID:            randID(),
		OrgID:                randID(),
		Title:                "test doc",
		ContentKind:          channel.ChannelDocumentKindMarkdown,
		CreatedByPrincipalID: randID(),
		UpdatedByPrincipalID: randID(),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return doc
}

// TestChannelDocumentLock_AcquireFresh 抢一个全新文档的锁:成功。
func TestChannelDocumentLock_AcquireFresh(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	caller := randID()

	heldBy, expires, acquired, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, caller, 5*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired || heldBy != caller {
		t.Fatalf("expected acquired by caller=%d, got heldBy=%d acquired=%v", caller, heldBy, acquired)
	}
	if !expires.After(time.Now()) {
		t.Fatalf("expires not in the future: %v", expires)
	}
}

// TestChannelDocumentLock_RaceOnlyOneWins 两个 goroutine 并发抢同一文档锁:只有一个赢。
func TestChannelDocumentLock_RaceOnlyOneWins(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pidA := randID()
	pidB := randID()
	if pidA == pidB {
		pidB++
	}

	var wg sync.WaitGroup
	results := make([]bool, 2)
	holders := make([]uint64, 2)
	wg.Add(2)
	for i, pid := range []uint64{pidA, pidB} {
		i, pid := i, pid
		go func() {
			defer wg.Done()
			heldBy, _, acquired, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pid, time.Minute, time.Now().UTC())
			if err != nil {
				t.Errorf("goroutine %d acquire: %v", i, err)
				return
			}
			results[i] = acquired
			holders[i] = heldBy
		}()
	}
	wg.Wait()

	wins := 0
	for _, ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly 1 winner, got %d (results=%v)", wins, results)
	}
	// 失败那位返回的 holder 必须是赢家
	for i, ok := range results {
		if !ok && holders[i] != pidA && holders[i] != pidB {
			t.Fatalf("loser sees unknown holder %d", holders[i])
		}
	}
}

// TestChannelDocumentLock_ExpiryAllowsSteal 锁过期后另一人能抢。
func TestChannelDocumentLock_ExpiryAllowsSteal(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pidA := randID()
	pidB := pidA + 1

	// A 抢到 1ms TTL 的锁
	if _, _, acquired, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pidA, time.Millisecond, time.Now().UTC()); err != nil || !acquired {
		t.Fatalf("A acquire: acq=%v err=%v", acquired, err)
	}
	time.Sleep(20 * time.Millisecond)

	// B 用 now=time.Now() 抢:expires_at < now,UPDATE 命中 → B 抢到
	heldBy, _, acquired, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pidB, time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("B acquire after expiry: %v", err)
	}
	if !acquired || heldBy != pidB {
		t.Fatalf("expected B to steal expired lock, got heldBy=%d acquired=%v", heldBy, acquired)
	}
}

// TestChannelDocumentLock_SameHolderRenews 同人续锁:expires_at 推后。
func TestChannelDocumentLock_SameHolderRenews(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pid := randID()

	now1 := time.Now().UTC()
	if _, _, acq, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pid, time.Minute, now1); err != nil || !acq {
		t.Fatalf("first acquire: acq=%v err=%v", acq, err)
	}
	now2 := now1.Add(30 * time.Second)
	_, expires2, acq2, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pid, time.Minute, now2)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !acq2 {
		t.Fatalf("same holder should be allowed to renew")
	}
	want := now2.Add(time.Minute).Truncate(time.Second)
	got := expires2.Truncate(time.Second)
	if !got.Equal(want) {
		t.Fatalf("expected renewed expiry %v, got %v", want, got)
	}
}

// TestChannelDocumentLock_OtherHolderRejected 别人持着未过期 → acquired=false 不报错。
func TestChannelDocumentLock_OtherHolderRejected(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pidA := randID()
	pidB := pidA + 1

	if _, _, acq, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pidA, time.Minute, time.Now().UTC()); err != nil || !acq {
		t.Fatalf("A acquire: acq=%v err=%v", acq, err)
	}
	heldBy, _, acquired, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pidB, time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("B acquire: %v", err)
	}
	if acquired {
		t.Fatalf("B should not acquire while A holds non-expired lock")
	}
	if heldBy != pidA {
		t.Fatalf("expected heldBy=A=%d, got %d", pidA, heldBy)
	}
}

// TestChannelDocumentLock_Release 持锁人可释放;别人调释放无效。
func TestChannelDocumentLock_Release(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pidA := randID()
	pidB := pidA + 1

	if _, _, _, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pidA, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("A acquire: %v", err)
	}

	// B 调 release:无效(不是持锁人)
	released, err := repo.ReleaseChannelDocumentLock(ctx, doc.ID, pidB)
	if err != nil {
		t.Fatalf("B release: %v", err)
	}
	if released {
		t.Fatalf("B should not be able to release A's lock")
	}

	// A 调 release:成功
	released, err = repo.ReleaseChannelDocumentLock(ctx, doc.ID, pidA)
	if err != nil {
		t.Fatalf("A release: %v", err)
	}
	if !released {
		t.Fatalf("A should release own lock")
	}

	// 再调一次:幂等 false
	released, err = repo.ReleaseChannelDocumentLock(ctx, doc.ID, pidA)
	if err != nil {
		t.Fatalf("A release again: %v", err)
	}
	if released {
		t.Fatalf("second release should be idempotent no-op")
	}
}

// TestChannelDocumentLock_ForceRelease 强制释放无视持锁人。
func TestChannelDocumentLock_ForceRelease(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	pid := randID()

	if _, _, _, err := repo.AcquireChannelDocumentLock(ctx, doc.ID, pid, time.Hour, time.Now().UTC()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	released, err := repo.ForceReleaseChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		t.Fatalf("force release: %v", err)
	}
	if !released {
		t.Fatalf("force release should remove the lock")
	}
	lock, err := repo.FindChannelDocumentLock(ctx, doc.ID)
	if err != nil {
		t.Fatalf("find lock: %v", err)
	}
	if lock != nil {
		t.Fatalf("lock should be gone, got %+v", lock)
	}
}

// TestChannelDocumentVersion_HashUnique 同 doc + 同 version hash 唯一约束生效。
func TestChannelDocumentVersion_HashUnique(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)
	hash := "abc123"

	v1 := &model.ChannelDocumentVersion{
		DocumentID: doc.ID, Version: hash, OSSKey: "k1", ByteSize: 10,
		EditedByPrincipalID: randID(), CreatedAt: time.Now().UTC(),
	}
	if err := repo.CreateChannelDocumentVersion(ctx, v1); err != nil {
		t.Fatalf("first create: %v", err)
	}

	v2 := &model.ChannelDocumentVersion{
		DocumentID: doc.ID, Version: hash, OSSKey: "k2", ByteSize: 20,
		EditedByPrincipalID: randID(), CreatedAt: time.Now().UTC(),
	}
	err := repo.CreateChannelDocumentVersion(ctx, v2)
	if err == nil {
		t.Fatalf("duplicate (doc, hash) should fail; got nil")
	}

	// 查回首条
	got, err := repo.FindChannelDocumentVersionByHash(ctx, doc.ID, hash)
	if err != nil {
		t.Fatalf("find by hash: %v", err)
	}
	if got == nil || got.OSSKey != "k1" {
		t.Fatalf("expected first version retained, got %+v", got)
	}
}

// TestChannelDocument_SoftDelete 软删后 List 不返,但 Find 仍能拿到带 DeletedAt。
func TestChannelDocument_SoftDelete(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()
	doc := seedDocument(t, db)

	// List 应能看到
	ds, err := repo.ListChannelDocumentsByChannel(ctx, doc.ChannelID)
	if err != nil {
		t.Fatalf("list before delete: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(ds))
	}

	if err := repo.SoftDeleteChannelDocument(ctx, doc.ID, time.Now().UTC()); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	ds, err = repo.ListChannelDocumentsByChannel(ctx, doc.ChannelID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(ds) != 0 {
		t.Fatalf("expected 0 after soft delete, got %d", len(ds))
	}

	// Find 仍能拿到
	got, err := repo.FindChannelDocumentByID(ctx, doc.ID)
	if err != nil {
		t.Fatalf("find after delete: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatalf("expected DeletedAt populated")
	}
}

// TestChannelDocument_NotFound 未存在文档 Find 返 gorm.ErrRecordNotFound。
func TestChannelDocument_NotFound(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	_, err := repo.FindChannelDocumentByID(context.Background(), 99999999999)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
}
