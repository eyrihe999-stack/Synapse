//go:build integration

// document_service_presign_integration_test.go OSS 直传 (PR #15') service 层闭环测。
//
// 复用 document_service_integration_test.go 的 testDB / seedUserWithPrincipal /
// seedChannelWithMembers / fakeOSS / newDocService。
//
// 测试用 fakeOSS:
//   - PresignUpload 返一个 fake URL
//   - 测试代码手工调 fakeOSS.PutObject 模拟"客户端 PUT 字节到 OSS"
//   - CommitUpload 走 service 真实路径(HEAD + StreamGet 算 sha256 + 写 version 行)
//
// 跑法:go test -tags=integration ./internal/channel/service -run ChannelDocumentPresign -v
package service_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
)

// TestChannelDocumentPresign_HappyPath 完整 OSS 直传链路:
// presign → fake PUT → acquire lock → commit → 期望 Created=true / 新 version 写入。
func TestChannelDocumentPresign_HappyPath(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	doc, err := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "Plan", chanerr.ChannelDocumentKindMarkdown)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 1. presign — 不要求持锁
	presign, err := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if presign.UploadURL == "" || presign.CommitToken == "" || presign.OSSKey == "" {
		t.Fatalf("presign returned empty fields: %+v", presign)
	}

	// 2. 模拟客户端把字节 PUT 到 OSS
	body := []byte("# Direct upload\n\nThis went straight to OSS.")
	if _, err := oss.PutObject(ctx, presign.OSSKey, body, presign.ContentType); err != nil {
		t.Fatalf("fake oss put: %v", err)
	}

	// 3. acquire lock(commit 必持锁)
	if _, err := svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// 4. commit
	out, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID:        ch.ID,
		DocumentID:       doc.ID,
		ActorPrincipalID: alicePID,
		CommitToken:      presign.CommitToken,
		EditSummary:      "first direct upload",
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !out.Created {
		t.Fatalf("expected Created=true on first commit")
	}
	if out.Document.CurrentByteSize != int64(len(body)) {
		t.Fatalf("byte size mismatch: want %d got %d", len(body), out.Document.CurrentByteSize)
	}
	if out.Version.OSSKey != presign.OSSKey {
		t.Fatalf("version oss_key should match presign key: want %s got %s", presign.OSSKey, out.Version.OSSKey)
	}

	// 5. 验证字节在 OSS(读回)
	got, err := svc.GetContentByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	if !bytes.Equal(got.Content, body) {
		t.Fatalf("oss content mismatch: got %q", got.Content)
	}
}

// TestChannelDocumentPresign_CommitWithoutLock commit 必持锁。
func TestChannelDocumentPresign_CommitWithoutLock(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	presign, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	_, _ = oss.PutObject(ctx, presign.OSSKey, []byte("# hello"), presign.ContentType)

	// **不** acquire lock,直接 commit — 应失败
	_, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID:        ch.ID,
		DocumentID:       doc.ID,
		ActorPrincipalID: alicePID,
		CommitToken:      presign.CommitToken,
	})
	if !errors.Is(err, chanerr.ErrChannelDocumentLockNotHeld) {
		t.Fatalf("expect LockNotHeld without lock, got %v", err)
	}
}

// TestChannelDocumentPresign_TokenTamperingRejected commit 时 token 被改 → ErrInvalidToken。
func TestChannelDocumentPresign_TokenTamperingRejected(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	presign, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	_, _ = oss.PutObject(ctx, presign.OSSKey, []byte("# hello"), presign.ContentType)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)

	bad := presign.CommitToken[:len(presign.CommitToken)-1] + "X"
	_, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID:        ch.ID,
		DocumentID:       doc.ID,
		ActorPrincipalID: alicePID,
		CommitToken:      bad,
	})
	if !errors.Is(err, uploadtoken.ErrInvalidToken) {
		t.Fatalf("expect ErrInvalidToken on tampered token, got %v", err)
	}
}

// TestChannelDocumentDownloadURL_HappyPath PresignDownload 拿到 URL + 版本元数据快照。
// 空文档 / 软删 / 非成员 边界用 _Empty / _Forbidden 子测覆盖。
func TestChannelDocumentDownloadURL_HappyPath(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	// 空文档应拒(没 oss_key)
	if _, err := svc.PresignDownloadByPrincipal(ctx, ch.ID, doc.ID, alicePID); !errors.Is(err, chanerr.ErrChannelDocumentContentEmpty) {
		t.Fatalf("expect ErrChannelDocumentContentEmpty for empty doc, got %v", err)
	}

	// 写一版 → 再 presign download 应成功
	body := []byte("# Hello world")
	p, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	_, _ = oss.PutObject(ctx, p.OSSKey, body, p.ContentType)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	out, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID, CommitToken: p.CommitToken,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	dl, err := svc.PresignDownloadByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	if err != nil {
		t.Fatalf("presign download: %v", err)
	}
	if dl.DownloadURL == "" {
		t.Fatalf("download url empty")
	}
	if dl.Version != out.Version.Version {
		t.Fatalf("version mismatch: dl=%s commit=%s", dl.Version, out.Version.Version)
	}
	if dl.ByteSize != int64(len(body)) {
		t.Fatalf("byte size mismatch: dl=%d expected=%d", dl.ByteSize, len(body))
	}
}

// TestChannelDocumentPresign_BaseVersionStale 乐观锁:Alice 拿 v1 的 base 时
// 在她 commit 之前 Bob 已 commit 了 v2 → Alice 的 commit 应被拒(stale base)。
//
// 这测的是"两个 actor 在乐观锁模式下的 lost-update 防御"。
func TestChannelDocumentPresign_BaseVersionStale(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	_, bobPID := seedUserWithPrincipal(t, db, "bob")
	ch := seedChannelWithMembers(t, db, alicePID, bobPID)
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	// 起一个 v1 让 doc 有 base
	v1Body := []byte("# version 1")
	p0, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	_, _ = oss.PutObject(ctx, p0.OSSKey, v1Body, p0.ContentType)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	v1Out, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID, CommitToken: p0.CommitToken,
	})
	if err != nil {
		t.Fatalf("v1 commit: %v", err)
	}
	_ = svc.ReleaseLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	v1Hash := v1Out.Version.Version

	// Alice 走 RMW:拿 base=v1Hash 的 presign(还没 PUT 也还没 commit)
	pAlice, err := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, v1Hash)
	if err != nil {
		t.Fatalf("alice presign with base: %v", err)
	}
	// 模拟 Alice "改了本地" 准备 PUT(但还没 PUT/commit)
	aliceNewBody := []byte("# version 1\n\n## alice's edits")
	_, _ = oss.PutObject(ctx, pAlice.OSSKey, aliceNewBody, pAlice.ContentType)

	// Bob 抢锁 + 直接 inline save 一个 v2(用老的 SaveVersion 路径,绕过 base 校验)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, bobPID)
	_, err = svc.SaveVersionByPrincipal(ctx, service.SaveVersionByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: bobPID,
		Content: []byte("# version 1\n\n## bob's hostile edit"),
	})
	if err != nil {
		t.Fatalf("bob save v2: %v", err)
	}
	_ = svc.ReleaseLockByPrincipal(ctx, ch.ID, doc.ID, bobPID)

	// Alice 此刻试 commit(锁要重抢一次)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	_, err = svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID, CommitToken: pAlice.CommitToken,
	})
	if !errors.Is(err, chanerr.ErrChannelDocumentBaseVersionStale) {
		t.Fatalf("expect ErrChannelDocumentBaseVersionStale, got %v", err)
	}
}

// TestChannelDocumentDownloadURL_NonMemberForbidden 非 channel 成员 download 应 forbidden。
func TestChannelDocumentDownloadURL_NonMemberForbidden(t *testing.T) {
	db := testDB(t)
	svc, _ := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	_, evePID := seedUserWithPrincipal(t, db, "eve")
	ch := seedChannelWithMembers(t, db, alicePID) // eve 不是成员
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	if _, err := svc.PresignDownloadByPrincipal(ctx, ch.ID, doc.ID, evePID); !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("expect ErrForbidden for non-member, got %v", err)
	}
}

// TestChannelDocumentPresign_DuplicateContentIdempotent 同 sha256 commit 第二次:
// Created=false,版本表只 1 行,新上传 OSS 对象被删。
func TestChannelDocumentPresign_DuplicateContentIdempotent(t *testing.T) {
	db := testDB(t)
	svc, oss := newDocService(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)
	doc, _ := svc.CreateByPrincipal(ctx, ch.ID, alicePID, "x", chanerr.ChannelDocumentKindMarkdown)

	body := []byte("# same content twice")

	// 第一次:正常 commit
	p1, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	_, _ = oss.PutObject(ctx, p1.OSSKey, body, p1.ContentType)
	_, _ = svc.AcquireLockByPrincipal(ctx, ch.ID, doc.ID, alicePID)
	out1, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID, CommitToken: p1.CommitToken,
	})
	if err != nil || !out1.Created {
		t.Fatalf("first commit: created=%v err=%v", out1.Created, err)
	}

	// 第二次:新 presign(新 OSS key),同 body
	p2, _ := svc.PresignUploadByPrincipal(ctx, ch.ID, doc.ID, alicePID, "")
	if p2.OSSKey == p1.OSSKey {
		t.Fatalf("expect different oss key on second presign (random)")
	}
	_, _ = oss.PutObject(ctx, p2.OSSKey, body, p2.ContentType)
	out2, err := svc.CommitUploadByPrincipal(ctx, service.CommitUploadByPrincipalInput{
		ChannelID: ch.ID, DocumentID: doc.ID, ActorPrincipalID: alicePID, CommitToken: p2.CommitToken,
	})
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if out2.Created {
		t.Fatalf("expected Created=false on dup hash")
	}
	// p2 上传的 OSS 对象应被服务端删除(避孤儿)
	if _, err := oss.GetObject(ctx, p2.OSSKey); err == nil {
		t.Fatalf("expected p2 OSS object deleted after dup commit, but still exists")
	}
}
