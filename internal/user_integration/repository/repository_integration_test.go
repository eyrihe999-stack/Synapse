//go:build integration

// 跑法:go test -tags=integration ./internal/user_integration/repository -v
// 前提:synapse-mysql 容器在 127.0.0.1:13306。
package repository_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration/model"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.MySQLConfig{
		Host: "127.0.0.1", Port: 13306,
		Username: "root", Password: "123456", Database: "synapse",
		MaxOpenConns: 5, MaxIdleConns: 2,
	}
	db, err := database.NewGormMySQL(cfg)
	if err != nil {
		t.Skipf("mysql not available: %v", err)
	}
	log := mustLogger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := user_integration.RunMigrations(ctx, db, log, nil); err != nil {
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

func randUserID() uint64 { return uint64(rand.Uint32()) | 1 }

func TestUpsert_Idempotent(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	userID := randUserID()
	in := &model.UserIntegration{
		UserID:            userID,
		Provider:          user_integration.ProviderFeishu,
		ExternalAccountID: "ext-abc",
		AccessToken:       "token-1",
		RefreshToken:      "rtoken-1",
		Status:            user_integration.StatusActive,
	}
	if err := repo.Upsert(ctx, in); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstID := in.ID
	if firstID == 0 {
		t.Fatalf("expected id populated after insert")
	}

	// 第二次 upsert:同 (user_id, provider, external_account_id),ID 应保持;token 更新
	in2 := &model.UserIntegration{
		UserID:            userID,
		Provider:          user_integration.ProviderFeishu,
		ExternalAccountID: "ext-abc",
		AccessToken:       "token-2",
		RefreshToken:      "rtoken-2",
		Status:            user_integration.StatusActive,
	}
	if err := repo.Upsert(ctx, in2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.GetByUserProvider(ctx, userID, user_integration.ProviderFeishu, "ext-abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != firstID {
		t.Fatalf("id changed: before=%d after=%d (should upsert in place)", firstID, got.ID)
	}
	if got.AccessToken != "token-2" {
		t.Fatalf("access_token not updated: %q", got.AccessToken)
	}
}

func TestGet_NotFound(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	_, err := repo.GetByUserProvider(context.Background(), 99999999, "nope", "nope")
	if !errors.Is(err, user_integration.ErrIntegrationNotFound) {
		t.Fatalf("expected ErrIntegrationNotFound, got %v", err)
	}
}

func TestListByUser_Ordered(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	userID := randUserID()
	providers := []string{user_integration.ProviderNotion, user_integration.ProviderFeishu, user_integration.ProviderGitLab}
	for _, p := range providers {
		if err := repo.Upsert(ctx, &model.UserIntegration{
			UserID: userID, Provider: p, ExternalAccountID: "a", AccessToken: "t",
			Status: user_integration.StatusActive,
		}); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	got, err := repo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(providers) {
		t.Fatalf("len=%d want=%d", len(got), len(providers))
	}
	// Order 是 provider ASC,所以 feishu < gitlab < notion
	wantOrder := []string{user_integration.ProviderFeishu, user_integration.ProviderGitLab, user_integration.ProviderNotion}
	for i := range got {
		if got[i].Provider != wantOrder[i] {
			t.Fatalf("order[%d] = %s, want %s", i, got[i].Provider, wantOrder[i])
		}
	}
}

func TestDelete_Idempotent(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	userID := randUserID()
	in := &model.UserIntegration{
		UserID: userID, Provider: "x", ExternalAccountID: "y", AccessToken: "t",
		Status: user_integration.StatusActive,
	}
	if err := repo.Upsert(ctx, in); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.Delete(ctx, userID, in.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 二次删应幂等成功
	if err := repo.Delete(ctx, userID, in.ID); err != nil {
		t.Fatalf("delete idempotent: %v", err)
	}
}
