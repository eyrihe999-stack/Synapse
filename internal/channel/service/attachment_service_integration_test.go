//go:build integration

// attachment_service_integration_test.go channel 附件 service 层 e2e:
// presign → fake PUT → commit + dedup + 鉴权边界。
//
// 跑法:
//
//	go test -tags=integration ./internal/channel/service -run ChannelAttachment -v
//
// 复用 document_service_integration_test.go 的 testDB / seedUserWithPrincipal /
// seedChannelWithMembers / fakeOSS。
package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"gorm.io/gorm"
)

// newAttachmentSvc 装配:复用 newDocService 的依赖配置,但返 Attachment 子服务。
func newAttachmentSvc(t *testing.T, db *gorm.DB) (service.AttachmentService, *fakeOSS) {
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
	return svc.Attachment, oss
}

// TestChannelAttachment_HappyPath 完整链路:presign → fake PUT → commit → GET download URL。
func TestChannelAttachment_HappyPath(t *testing.T) {
	db := testDB(t)
	svc, oss := newAttachmentSvc(t, db)
	ctx := context.Background()

	userID, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	// 1. presign
	presign, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID:        ch.ID,
		ActorPrincipalID: alicePID,
		MimeType:         "image/png",
		Filename:         "diagram.png",
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if presign.UploadURL == "" || presign.CommitToken == "" || presign.OSSKey == "" {
		t.Fatalf("presign returned empty fields: %+v", presign)
	}
	if presign.ContentType != "image/png" {
		t.Fatalf("content type: want image/png got %s", presign.ContentType)
	}
	if !strings.Contains(presign.OSSKey, "channel-attachments") {
		t.Fatalf("oss key should contain channel-attachments: %s", presign.OSSKey)
	}

	// 2. 模拟客户端 PUT 字节
	body := []byte("\x89PNG fake bytes for test")
	if _, err := oss.PutObject(ctx, presign.OSSKey, body, presign.ContentType); err != nil {
		t.Fatalf("fake put: %v", err)
	}

	// 3. commit (by-principal)
	out, err := svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID:        ch.ID,
		ActorPrincipalID: alicePID,
		CommitToken:      presign.CommitToken,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.Reused {
		t.Fatalf("first commit should not be reused")
	}
	if out.Attachment.ChannelID != ch.ID {
		t.Fatalf("channel mismatch: want %d got %d", ch.ID, out.Attachment.ChannelID)
	}
	if out.Attachment.MimeType != "image/png" || out.Attachment.Filename != "diagram.png" {
		t.Fatalf("metadata mismatch: %+v", out.Attachment)
	}
	if out.Attachment.ByteSize != int64(len(body)) {
		t.Fatalf("byte size: want %d got %d", len(body), out.Attachment.ByteSize)
	}
	if out.Attachment.Sha256 == "" {
		t.Fatalf("sha256 should be set")
	}

	// 4. OpenForStream (by-user) — 拿到 server-side stream + 元数据
	stream, err := svc.OpenForStream(ctx, ch.ID, out.Attachment.ID, userID)
	if err != nil {
		t.Fatalf("open for stream: %v", err)
	}
	defer stream.Body.Close()
	if stream.Attachment.ID != out.Attachment.ID {
		t.Fatalf("stream attachment mismatch")
	}
	if stream.MimeType != "image/png" {
		t.Fatalf("stream mime: want image/png got %s", stream.MimeType)
	}
	got, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("stream bytes mismatch: want %q got %q", body, got)
	}
}

// TestChannelAttachment_Dedup 同 channel 重传同字节:第二次 Reused=true,复用同 attachment_id,
// 删第二次的 OSS 对象(避孤儿)。
func TestChannelAttachment_Dedup(t *testing.T) {
	db := testDB(t)
	svc, oss := newAttachmentSvc(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	body := []byte("dedup test bytes")

	// 第一次 upload
	first, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	if err != nil {
		t.Fatalf("first presign: %v", err)
	}
	if _, err := oss.PutObject(ctx, first.OSSKey, body, "image/png"); err != nil {
		t.Fatalf("first put: %v", err)
	}
	firstOut, err := svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, CommitToken: first.CommitToken,
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if firstOut.Reused {
		t.Fatalf("first must not be reused")
	}

	// 第二次 upload — 同字节,新 OSS key(rand 不同)
	second, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	if err != nil {
		t.Fatalf("second presign: %v", err)
	}
	if first.OSSKey == second.OSSKey {
		t.Fatalf("second OSS key should differ from first (rand suffix)")
	}
	if _, err := oss.PutObject(ctx, second.OSSKey, body, "image/png"); err != nil {
		t.Fatalf("second put: %v", err)
	}
	secondOut, err := svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, CommitToken: second.CommitToken,
	})
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if !secondOut.Reused {
		t.Fatalf("second commit should be reused (same sha256 in same channel)")
	}
	if secondOut.Attachment.ID != firstOut.Attachment.ID {
		t.Fatalf("reused attachment id should match first: want %d got %d",
			firstOut.Attachment.ID, secondOut.Attachment.ID)
	}
	// 第二次的 OSS 对象应被服务端删掉
	if _, err := oss.GetObject(ctx, second.OSSKey); err == nil {
		t.Fatalf("second OSS object should have been deleted on dedup")
	}
}

// TestChannelAttachment_RejectInvalidMime SVG / 非白名单 MIME 在 presign 阶段就被拒。
func TestChannelAttachment_RejectInvalidMime(t *testing.T) {
	db := testDB(t)
	svc, _ := newAttachmentSvc(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	for _, mime := range []string{"image/svg+xml", "application/pdf", "text/html", ""} {
		_, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
			ChannelID: ch.ID, ActorPrincipalID: alicePID, MimeType: mime,
		})
		if !errors.Is(err, chanerr.ErrChannelAttachmentMimeInvalid) {
			t.Fatalf("mime %q: want ErrChannelAttachmentMimeInvalid got %v", mime, err)
		}
	}
}

// TestChannelAttachment_NonMemberForbidden 非 channel 成员调 presign / commit 都返 ErrForbidden。
func TestChannelAttachment_NonMemberForbidden(t *testing.T) {
	db := testDB(t)
	svc, _ := newAttachmentSvc(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	_, bobPID := seedUserWithPrincipal(t, db, "bob")
	ch := seedChannelWithMembers(t, db, alicePID) // 只 alice 是成员

	_, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: bobPID, MimeType: "image/png",
	})
	if !errors.Is(err, chanerr.ErrForbidden) {
		t.Fatalf("non-member presign: want ErrForbidden got %v", err)
	}
}

// TestChannelAttachment_ArchivedChannelWriteRejected channel 归档后写路径(presign / commit)拒。
// 读路径(OpenForStream)允许。
func TestChannelAttachment_ArchivedChannelWriteRejected(t *testing.T) {
	db := testDB(t)
	svc, oss := newAttachmentSvc(t, db)
	ctx := context.Background()

	userID, alicePID := seedUserWithPrincipal(t, db, "alice")
	ch := seedChannelWithMembers(t, db, alicePID)

	// 先在未归档时上传一张
	presign, _ := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	body := []byte("png bytes before archive")
	if _, err := oss.PutObject(ctx, presign.OSSKey, body, "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	out, err := svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, CommitToken: presign.CommitToken,
	})
	if err != nil {
		t.Fatalf("commit before archive: %v", err)
	}

	// 归档
	archiveChannel(t, db, ch.ID)

	// 写路径拒
	_, err = svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: ch.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	if !errors.Is(err, chanerr.ErrChannelArchived) {
		t.Fatalf("presign after archive: want ErrChannelArchived got %v", err)
	}

	// 读路径仍可
	stream, err := svc.OpenForStream(ctx, ch.ID, out.Attachment.ID, userID)
	if err != nil {
		t.Fatalf("open stream after archive (should still work): %v", err)
	}
	stream.Body.Close()
}

// TestChannelAttachment_CrossChannelTokenRejected 用 channel A 的 token 在 channel B commit
// 应被拒(防 token 跨 channel 误用 / 攻击)。
func TestChannelAttachment_CrossChannelTokenRejected(t *testing.T) {
	db := testDB(t)
	svc, oss := newAttachmentSvc(t, db)
	ctx := context.Background()

	_, alicePID := seedUserWithPrincipal(t, db, "alice")
	chA := seedChannelWithMembers(t, db, alicePID)
	chB := seedChannelWithMembers(t, db, alicePID)

	presignA, err := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: chA.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	if err != nil {
		t.Fatalf("presign A: %v", err)
	}
	if _, err := oss.PutObject(ctx, presignA.OSSKey, []byte("x"), "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}

	// 用 A 的 token 去 commit B —— service 校验 token.ChannelID == chB.ID 失败 → token invalid
	_, err = svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID: chB.ID, ActorPrincipalID: alicePID, CommitToken: presignA.CommitToken,
	})
	if err == nil {
		t.Fatalf("cross-channel commit should fail")
	}
	if !errors.Is(err, uploadtoken.ErrInvalidToken) {
		t.Fatalf("cross-channel commit: want ErrInvalidToken got %v", err)
	}
}

// TestChannelAttachment_GetByWrongChannelRejected attachment 属 channel A,从 channel B 上下载
// 应返 ErrChannelAttachmentNotFound(避免成员关系泄露 attachment 存在性)。
func TestChannelAttachment_GetByWrongChannelRejected(t *testing.T) {
	db := testDB(t)
	svc, oss := newAttachmentSvc(t, db)
	ctx := context.Background()

	userID, alicePID := seedUserWithPrincipal(t, db, "alice")
	chA := seedChannelWithMembers(t, db, alicePID)
	chB := seedChannelWithMembers(t, db, alicePID)

	presign, _ := svc.PresignUploadByPrincipal(ctx, service.PresignAttachmentUploadByPrincipalInput{
		ChannelID: chA.ID, ActorPrincipalID: alicePID, MimeType: "image/png",
	})
	if _, err := oss.PutObject(ctx, presign.OSSKey, []byte("a"), "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	out, err := svc.CommitUploadByPrincipal(ctx, service.CommitAttachmentUploadByPrincipalInput{
		ChannelID: chA.ID, ActorPrincipalID: alicePID, CommitToken: presign.CommitToken,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 从 chB 取 attachment(实际属 chA)
	_, err = svc.OpenForStream(ctx, chB.ID, out.Attachment.ID, userID)
	if !errors.Is(err, chanerr.ErrChannelAttachmentNotFound) {
		t.Fatalf("cross-channel get: want ErrChannelAttachmentNotFound got %v", err)
	}
}
