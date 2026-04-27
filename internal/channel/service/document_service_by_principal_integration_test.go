//go:build integration

// document_service_by_principal_integration_test.go MCP 路径(by-principal)关键
// 链路 e2e:create → list → acquire → save → release → archived 后只读。
//
// 复用 document_service_integration_test.go 里的 testDB / seedUserWithPrincipal /
// seedChannelWithMembers / newDocService / fakeOSS。
//
// 跑法:
//
//	go test -tags=integration ./internal/channel/service -run ChannelDocumentByPrincipal -v
package service_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
)

// TestChannelDocumentByPrincipal_HappyPath 完整链路:
// A 用 by-principal 接口创建 → list 看到 → acquire → save → 同 hash 幂等 → release。
func TestChannelDocumentByPrincipal_HappyPath(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	_, bobPID := seedUserWithPrincipal(t, db, "bob")
	ch := seedChannelWithMembers(t, db, alicePID, bobPID)

	// A 创建
	doc, err := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "Plan", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("create by principal: %v", err)
	}

	// list 看得到
	list, err := svc.ListByPrincipal(ctx, ch.ID, alicePID)
	if err != nil {
		t.Fatalf("list by principal: %v", err)
	}
	if len(list) != 1 || list[0].Document.ID != doc.ID {
		t.Fatalf("list: want 1 doc id=%d, got %d items", doc.ID, len(list))
	}
	if list[0].Lock != nil {
		t.Fatalf("expected no lock initially, got %+v", list[0].Lock)
	}

	// A 抢锁
	state, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	if err != nil || !state.Acquired {
		t.Fatalf("alice acquire by principal: state=%+v err=%v", state, err)
	}

	// B 抢锁失败,看到 A 持锁
	state2, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, bobPID)
	if !errors.Is(err, chanerr.ErrChannelDocumentLockHeld) {
		t.Fatalf("bob acquire: expected ErrChannelDocumentLockHeld, got %v", err)
	}
	if state2 == nil || state2.HeldByPrincipalID != alicePID {
		t.Fatalf("bob should see alice as holder, got %+v", state2)
	}

	// A save v1
	out1, err := svc.SaveVersionByPrincipal(ctx, service.SaveVersionByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID,
		Content: []byte("# v1"), EditSummary: "first",
	})
	if err != nil {
		t.Fatalf("save v1 by principal: %v", err)
	}
	if !out1.Created {
		t.Fatalf("expected v1 to be created")
	}
	if got, _ := oss.GetObject(ctx, out1.Version.OSSKey); !bytes.Equal(got, []byte("# v1")) {
		t.Fatalf("oss content mismatch")
	}

	// A 同 hash 再 save → 幂等(Created=false)
	out1dup, err := svc.SaveVersionByPrincipal(ctx, service.SaveVersionByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID,
		Content: []byte("# v1"),
	})
	if err != nil {
		t.Fatalf("save v1 dup: %v", err)
	}
	if out1dup.Created {
		t.Fatalf("expected dup save to be no-op")
	}

	// A 读 content
	content, err := svc.GetContentByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	if err != nil {
		t.Fatalf("get content by principal: %v", err)
	}
	if !bytes.Equal(content.Content, []byte("# v1")) {
		t.Fatalf("get content mismatch: got %q", string(content.Content))
	}

	// A 释放
	if err := svc.ReleaseLockByPrincipal(ctx, ch.ID, doc.ID, alicePID); err != nil {
		t.Fatalf("release by principal: %v", err)
	}

	// B 抢锁成功
	stateB, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, bobPID)
	if err != nil || !stateB.Acquired {
		t.Fatalf("bob acquire after release: state=%+v err=%v", stateB, err)
	}
}

// TestChannelDocumentByPrincipal_NonMemberForbidden 非 channel 成员的 principal 调
// 任何 by-principal 方法应得 ErrForbidden,避免 MCP 路径绕过 channel 成员校验。
func TestChannelDocumentByPrincipal_NonMemberForbidden(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	_, eveOnlyPID := seedUserWithPrincipal(t, db, "eve") // 不进 channel
	ch := seedChannelWithMembers(t, db, alicePID)        // 只有 alice

	doc, err := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "Plan", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("alice create: %v", err)
	}

	// eve 各路径都该 forbidden
	if _, err := svc.ListByPrincipal(ctx, ch.ID, eveOnlyPID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("eve list: expect ErrForbidden, got %v", err)
	}
	if _, err := svc.GetByPrincipal(ctx, ch.ID, doc.ID, eveOnlyPID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("eve get: expect ErrForbidden, got %v", err)
	}
	if _, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, eveOnlyPID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("eve acquire: expect ErrForbidden, got %v", err)
	}
	if _, err := svc.SaveVersionByPrincipal(ctx, service.SaveVersionByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: eveOnlyPID,
		Content: []byte("hijack"),
	}); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("eve save: expect ErrForbidden, got %v", err)
	}

	// principalID = 0 也是 ErrForbidden
	if _, err := svc.ListByPrincipal(ctx, ch.ID, 0); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("zero pid list: expect ErrForbidden, got %v", err)
	}
}

// TestChannelDocumentByPrincipal_ArchivedReadOnly 归档 channel:list/get 仍可读;
// 写路径(create / acquire / save / release)拒。
func TestChannelDocumentByPrincipal_ArchivedReadOnly(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	doc, err := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "Plan", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	archiveChannel(t, db, ch.ID)

	// 读路径 OK
	if _, err := svc.ListByPrincipal(ctx, ch.ID, alicePID); err != nil {
		t.Fatalf("list after archive: expect ok, got %v", err)
	}
	if _, err := svc.GetByPrincipal(ctx, ch.ID, doc.ID, alicePID); err != nil {
		t.Fatalf("get after archive: expect ok, got %v", err)
	}

	// 写路径都返 ErrChannelArchived
	if _, err := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "X", chanerr.ChannelDocumentKindText); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("create after archive: expect ErrChannelArchived, got %v", err)
	}
	if _, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("acquire after archive: expect ErrChannelArchived, got %v", err)
	}
	if _, err := svc.SaveVersionByPrincipal(ctx, service.SaveVersionByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID,
		Content: []byte("late"),
	}); !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("save after archive: expect ErrChannelArchived, got %v", err)
	}
}
