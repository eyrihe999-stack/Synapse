//go:build integration

// document_service_integration_test.go service 层 e2e:lock → save → unlock + archive 后只读。
//
// 跑法:
//
//	go test -tags=integration ./internal/channel/service -run ChannelDocument -v
//
// 前提:同 repository 集成测,docker-compose 中 synapse-mysql 已起 + principal/user/channel
// migration 已跑(channel migration 自带 + 测试代码额外跑 principal+user)。
package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/model"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/principal"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	usermodel "github.com/eyrihe999-stack/Synapse/internal/user/model"
	"gorm.io/gorm"
)

// ── 测试基建 ─────────────────────────────────────────────────────────────────

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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := principal.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("principal migration: %v", err)
	}
	if err := user.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("user migration: %v", err)
	}
	if err := chanerr.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("channel migration: %v", err)
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

func randSuffix() string {
	return fmt.Sprintf("%d_%d", time.Now().UnixNano(), rand.Uint32())
}

// seedUserWithPrincipal 建一条 principal + user,返回 userID。
// User.BeforeCreate hook 会自动建 principal 并回填 principal_id —— 测试用例不必手建 principal。
func seedUserWithPrincipal(t *testing.T, db *gorm.DB, displayName string) (userID, principalID uint64) {
	t.Helper()
	u := &usermodel.User{
		Email:        fmt.Sprintf("test_%s@example.com", randSuffix()),
		PasswordHash: "x",
		DisplayName:  displayName,
		Status:       usermodel.StatusActive,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if u.PrincipalID == 0 {
		t.Fatalf("user PrincipalID not populated by BeforeCreate hook")
	}
	return u.ID, u.PrincipalID
}

// seedChannelWithMembers 建一个未归档 channel + 给定 principal 的成员关系(第一个是 owner,其余 member)。
func seedChannelWithMembers(t *testing.T, db *gorm.DB, principalIDs ...uint64) *model.Channel {
	t.Helper()
	now := time.Now().UTC()
	c := &model.Channel{
		OrgID:     uint64(rand.Uint32()) | 1,
		ProjectID: uint64(rand.Uint32()) | 1,
		Name:      "test-" + randSuffix(),
		Status:    chanerr.ChannelStatusOpen,
		CreatedBy: principalIDs[0],
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	for i, pid := range principalIDs {
		role := chanerr.MemberRoleMember
		if i == 0 {
			role = chanerr.MemberRoleOwner
		}
		mem := &model.ChannelMember{
			ChannelID: c.ID, PrincipalID: pid, Role: role, JoinedAt: now,
		}
		if err := db.Create(mem).Error; err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	return c
}

// archiveChannel 把 channel.status 改 archived,模拟归档(绕过 service 的 owner 校验)。
func archiveChannel(t *testing.T, db *gorm.DB, channelID uint64) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Model(&model.Channel{}).Where("id = ?", channelID).Updates(map[string]any{
		"status": chanerr.ChannelStatusArchived, "archived_at": now,
	}).Error; err != nil {
		t.Fatalf("archive channel: %v", err)
	}
}

// fakeOSS 内存 OSS。线程安全;测试结束随 GC 释放。
type fakeOSS struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeOSS() *fakeOSS { return &fakeOSS{data: make(map[string][]byte)} }

func (f *fakeOSS) PutObject(_ context.Context, key string, data []byte, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.data[key] = cp
	return "fake://" + key, nil
}

func (f *fakeOSS) GetObject(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.data[key]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("fake oss: missing key %s", key)
}

func (f *fakeOSS) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeOSS) URL(key string) string { return "fake://" + key }

func (f *fakeOSS) PresignPutURL(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
	return "fake-presign://" + key, nil
}

func (f *fakeOSS) HeadObject(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.data[key]; ok {
		return int64(len(b)), nil
	}
	return -1, fmt.Errorf("fake oss: missing key %s", key)
}

func (f *fakeOSS) StreamGet(_ context.Context, key string, _ int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.data[key]; ok {
		cp := make([]byte, len(b))
		copy(cp, b)
		return io.NopCloser(bytes.NewReader(cp)), nil
	}
	return nil, fmt.Errorf("fake oss: missing key %s", key)
}

func (f *fakeOSS) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "fake-presign-get://" + key, nil
}

// newDocService 装配:无 publisher,fake OSS,prefix=test。
func newDocService(t *testing.T, db *gorm.DB) (service.DocumentService, *fakeOSS) {
	t.Helper()
	repo := repository.New(db)
	oss := newFakeOSS()
	signer, err := uploadtoken.NewSigner()
	if err != nil {
		t.Fatalf("upload signer: %v", err)
	}
	svc := service.New(
		service.Config{ChannelEventStream: "", OSSPathPrefix: "test"},
		repo,
		service.OrgMembershipCheckerFunc(func(_ context.Context, _, _ uint64) (bool, error) { return true, nil }),
		stubResolver{},
		nil, // no publisher
		oss,
		signer,
		mustLogger(t),
	)
	return svc.Document, oss
}

// stubResolver PrincipalOrgResolver 占位,DocumentService 路径不调它。
type stubResolver struct{}

func (stubResolver) IsPrincipalInOrg(context.Context, uint64, uint64) (bool, error) {
	return true, nil
}

// ── 测试用例 ─────────────────────────────────────────────────────────────────

// TestChannelDocumentService_LockSaveUnlockFlow 完整链路:
// A 创建 → A 抢锁 → B 抢锁失败(409) → A save 新版 → A 释放 → B 抢锁成功 → B save 新版。
func TestChannelDocumentService_LockSaveUnlockFlow(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	aliceUID, alicePID := seedUserWithPrincipal(t, db, "alice")
	bobUID, bobPID := seedUserWithPrincipal(t, db, "bob")
	ch := seedChannelWithMembers(t, db, alicePID, bobPID)

	// A 创建文档
	doc, err := svc.Create(ctx, ch.ID, aliceUID, "PRD", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("alice create: %v", err)
	}

	// A 抢锁
	state, err := svc.AcquireLock(ctx, ch.ID, doc.ID, aliceUID)
	if err != nil || !state.Acquired {
		t.Fatalf("alice acquire: state=%+v err=%v", state, err)
	}

	// B 抢锁失败
	state2, err := svc.AcquireLock(ctx, ch.ID, doc.ID, bobUID)
	if !errors.Is(err, chanerr.ErrChannelDocumentLockHeld) {
		t.Fatalf("bob acquire: expected ErrChannelDocumentLockHeld, got %v", err)
	}
	if state2 == nil || state2.HeldByPrincipalID != alicePID {
		t.Fatalf("bob should see alice as holder, got %+v", state2)
	}

	// A save v1
	out1, err := svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: aliceUID,
		Content: []byte("# Hello"), EditSummary: "first",
	})
	if err != nil {
		t.Fatalf("alice save v1: %v", err)
	}
	if !out1.Created {
		t.Fatalf("expected new version to be created")
	}
	if got, _ := oss.GetObject(ctx, out1.Version.OSSKey); !bytes.Equal(got, []byte("# Hello")) {
		t.Fatalf("OSS content mismatch")
	}

	// A 同 hash 再 save:幂等不写新版
	out1dup, err := svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: aliceUID,
		Content: []byte("# Hello"), EditSummary: "duplicate",
	})
	if err != nil {
		t.Fatalf("alice save dup: %v", err)
	}
	if out1dup.Created {
		t.Fatalf("duplicate hash should not create new version")
	}
	if out1dup.Version.ID != out1.Version.ID {
		t.Fatalf("expected same version row; got id %d vs %d", out1dup.Version.ID, out1.Version.ID)
	}

	// B 没锁不能 save
	_, err = svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: bobUID,
		Content: []byte("hijack"),
	})
	if !errors.Is(err, chanerr.ErrChannelDocumentLockNotHeld) {
		t.Fatalf("bob save without lock: expected ErrChannelDocumentLockNotHeld, got %v", err)
	}

	// A 释放锁
	if err := svc.ReleaseLock(ctx, ch.ID, doc.ID, aliceUID); err != nil {
		t.Fatalf("alice release: %v", err)
	}

	// B 抢锁成功
	state3, err := svc.AcquireLock(ctx, ch.ID, doc.ID, bobUID)
	if err != nil || !state3.Acquired {
		t.Fatalf("bob re-acquire: state=%+v err=%v", state3, err)
	}

	// B save v2
	out2, err := svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: bobUID,
		Content: []byte("# Hello v2"),
	})
	if err != nil {
		t.Fatalf("bob save v2: %v", err)
	}
	if !out2.Created || out2.Document.UpdatedByPrincipalID != bobPID {
		t.Fatalf("expected created+updatedBy=bob, got %+v", out2.Document)
	}

	// 列表能看到 2 个版本
	versions, err := svc.ListVersions(ctx, ch.ID, doc.ID, aliceUID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
}

// TestChannelDocumentService_NonMemberDenied 非 channel member 无法读/写。
func TestChannelDocumentService_NonMemberDenied(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	strangerUID, _ := seedUserWithPrincipal(t, db, "stranger")
	ch := seedChannelWithMembers(t, db, alicePID)

	if _, err := svc.Create(ctx, ch.ID, strangerUID, "X", chanerr.ChannelDocumentKindMarkdown); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("stranger create: expected ErrForbidden, got %v", err)
	}
	if _, err := svc.List(ctx, ch.ID, strangerUID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("stranger list: expected ErrForbidden, got %v", err)
	}
}

// TestChannelDocumentService_ArchivedReadOnly channel archive 后所有写返 ErrChannelArchived,读仍可。
func TestChannelDocumentService_ArchivedReadOnly(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	aliceUID, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)
	doc, err := svc.Create(ctx, ch.ID, aliceUID, "doc", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.AcquireLock(ctx, ch.ID, doc.ID, aliceUID); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: aliceUID, Content: []byte("hi"),
	}); err != nil {
		t.Fatalf("save before archive: %v", err)
	}

	archiveChannel(t, db, ch.ID)

	// 写路径全拒
	if _, err := svc.Create(ctx, ch.ID, aliceUID, "x", chanerr.ChannelDocumentKindMarkdown); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("create after archive: expected ErrChannelArchived, got %v", err)
	}
	if _, err := svc.AcquireLock(ctx, ch.ID, doc.ID, aliceUID); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("acquire after archive: expected ErrChannelArchived, got %v", err)
	}
	if _, err := svc.SaveVersion(ctx, service.SaveVersionInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorUserID: aliceUID, Content: []byte("blocked"),
	}); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("save after archive: expected ErrChannelArchived, got %v", err)
	}

	// 读仍可
	if _, err := svc.List(ctx, ch.ID, aliceUID); err != nil {
		t.Fatalf("list after archive (read should work): %v", err)
	}
	if _, err := svc.Get(ctx, ch.ID, doc.ID, aliceUID); err != nil {
		t.Fatalf("get after archive: %v", err)
	}
}

// TestChannelDocumentService_ForceUnlockByOwner channel owner 强制解锁;非 owner 在锁未过期时不行。
func TestChannelDocumentService_ForceUnlockByOwner(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	ownerUID, ownerPID := seedUserWithPrincipal(t, db, "owner")
	memberUID, memberPID := seedUserWithPrincipal(t, db, "member")
	thirdUID, thirdPID := seedUserWithPrincipal(t, db, "third")
	ch := seedChannelWithMembers(t, db, ownerPID, memberPID, thirdPID)

	doc, err := svc.Create(ctx, ch.ID, ownerUID, "doc", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// member 抢锁
	if _, err := svc.AcquireLock(ctx, ch.ID, doc.ID, memberUID); err != nil {
		t.Fatalf("member acquire: %v", err)
	}

	// third (普通成员) 强制解锁:锁未过期 → 拒
	if err := svc.ForceReleaseLock(ctx, ch.ID, doc.ID, thirdUID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("third force-unlock non-expired: expected ErrForbidden, got %v", err)
	}

	// owner 强制解锁:成功
	if err := svc.ForceReleaseLock(ctx, ch.ID, doc.ID, ownerUID); err != nil {
		t.Fatalf("owner force-unlock: %v", err)
	}

	// third 现在能抢
	state, err := svc.AcquireLock(ctx, ch.ID, doc.ID, thirdUID)
	if err != nil || !state.Acquired {
		t.Fatalf("third acquire after force-unlock: state=%+v err=%v", state, err)
	}
}
