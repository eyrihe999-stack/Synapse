//go:build integration

// repository_integration_test.go pm 模块 repository 集成测。
//
// 跑法:
//
//	go test -tags=integration ./internal/pm/repository -v
//
// 前提:docker-compose 中 synapse-mysql 容器已起并暴露 127.0.0.1:13306。
package repository_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	"github.com/eyrihe999-stack/Synapse/internal/pm/model"
	"github.com/eyrihe999-stack/Synapse/internal/pm/repository"
	"github.com/eyrihe999-stack/Synapse/internal/task"
)

// testDB 起一个连到本地 MySQL 的 GORM 句柄,跑完 pm + channel + task 三个模块
// 的 schema 迁移 + pm 的二阶段数据迁移。
//
// 每条用例自己造孤立数据(orgID / projectID 用 randID()),不清表;表之间通过
// 独立 ID 隔离。MySQL 不可用直接 t.Skip(用于本地无 docker 时绕过)。
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 装配次序对齐 main.go:pm → channel → task → pm.RunPostMigrations
	if err := pm.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("pm migration: %v", err)
	}
	if err := channel.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("channel migration: %v", err)
	}
	if err := task.RunMigrations(ctx, db, log, nil); err != nil {
		t.Fatalf("task migration: %v", err)
	}
	if err := pm.RunPostMigrations(ctx, db, log); err != nil {
		t.Fatalf("pm post-migration: %v", err)
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

// randID 给单元测试生成大的随机 ID 避免冲突(同 channel 模块测试约定)。
func randID() uint64 { return uint64(rand.Uint32())<<16 | uint64(rand.Uint32()&0xffff) | 1 }

// seedProject 直接写一行 projects(不走 service,跳过 org membership 校验),
// 然后立即调一次 RunPostMigrations 触发 default initiative + Backlog version
// + console channel 的 seed(因为 testDB 的初始 RunPostMigrations 是在
// seedProject 之前跑的,新 project 还没赶上)。
func seedProject(t *testing.T, db *gorm.DB) *model.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &model.Project{
		OrgID:     randID(),
		Name:      "test-proj-" + time.Now().Format("150405.000000"),
		CreatedBy: randID(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := pm.RunPostMigrations(context.Background(), db, mustLogger(t)); err != nil {
		t.Fatalf("seed project post-migration: %v", err)
	}
	return p
}

// TestProject_CRUD 验证 Project 的基础 CRUD。
func TestProject_CRUD(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	p := &model.Project{OrgID: randID(), Name: "p-" + time.Now().Format("150405.000000"), CreatedBy: randID()}
	if err := repo.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected id assigned")
	}
	got, err := repo.FindProjectByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != p.Name || got.OrgID != p.OrgID {
		t.Fatalf("mismatch got=%+v want=%+v", got, p)
	}
	// 重名守卫:同 org 同 name 第二次插入 ⇒ uk_projects_org_name_active 撞
	dup := &model.Project{OrgID: p.OrgID, Name: p.Name, CreatedBy: p.CreatedBy}
	err = repo.CreateProject(ctx, dup)
	if err == nil {
		t.Fatal("expected duplicate name to fail, got nil")
	}
}

// TestEnsureDefaultInitiative_Idempotent 直接造一个 project,跑两次
// RunPostMigrations,验证 default initiative 只被建一次。
func TestEnsureDefaultInitiative_Idempotent(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	p := seedProject(t, db)

	// testDB 已经跑了一次 RunPostMigrations,所以 default initiative 应该已经在
	got, err := repo.FindDefaultInitiative(ctx, p.ID)
	if err != nil {
		t.Fatalf("find default initiative: %v", err)
	}
	if !got.IsSystem || got.Name != pm.DefaultInitiativeName {
		t.Fatalf("default initiative shape wrong: %+v", got)
	}

	// 再跑一次 post-migration,应该不产生新行
	if err := pm.RunPostMigrations(ctx, db, mustLogger(t)); err != nil {
		t.Fatalf("re-run post-migration: %v", err)
	}
	got2, err := repo.FindDefaultInitiative(ctx, p.ID)
	if err != nil {
		t.Fatalf("find default initiative after re-run: %v", err)
	}
	if got2.ID != got.ID {
		t.Fatalf("re-run produced new row: old=%d new=%d", got.ID, got2.ID)
	}
}

// TestEnsureBacklogVersion_Idempotent 同上,验证 Backlog version。
func TestEnsureBacklogVersion_Idempotent(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	p := seedProject(t, db)

	got, err := repo.FindBacklogVersion(ctx, p.ID)
	if err != nil {
		t.Fatalf("find backlog version: %v", err)
	}
	if !got.IsSystem || got.Name != pm.BacklogVersionName {
		t.Fatalf("backlog version shape wrong: %+v", got)
	}

	if err := pm.RunPostMigrations(ctx, db, mustLogger(t)); err != nil {
		t.Fatalf("re-run post-migration: %v", err)
	}
	got2, err := repo.FindBacklogVersion(ctx, p.ID)
	if err != nil {
		t.Fatalf("find backlog after re-run: %v", err)
	}
	if got2.ID != got.ID {
		t.Fatalf("re-run produced new backlog: old=%d new=%d", got.ID, got2.ID)
	}
}

// TestWorkstream_BackfillNullVersion workstream.version_id 可空 = backlog;
// 验证 model + repo 路径走通,且 list 三个维度都拿到。
func TestWorkstream_BackfillNullVersion(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	p := seedProject(t, db)
	defaultInit, err := repo.FindDefaultInitiative(ctx, p.ID)
	if err != nil {
		t.Fatalf("find default initiative: %v", err)
	}

	w := &model.Workstream{
		InitiativeID: defaultInit.ID,
		ProjectID:    p.ID,
		Name:         "ws-1",
		Status:       pm.WorkstreamStatusActive,
		CreatedBy:    randID(),
	}
	if err := repo.CreateWorkstream(ctx, w); err != nil {
		t.Fatalf("create workstream: %v", err)
	}

	// 三个 list 路径都应能拿到这一行
	byInit, err := repo.ListWorkstreamsByInitiative(ctx, defaultInit.ID, 100, 0)
	if err != nil || len(byInit) == 0 {
		t.Fatalf("list by initiative: err=%v len=%d", err, len(byInit))
	}
	byProj, err := repo.ListWorkstreamsByProject(ctx, p.ID, 100, 0)
	if err != nil || len(byProj) == 0 {
		t.Fatalf("list by project: err=%v len=%d", err, len(byProj))
	}
	// version_id IS NULL,按 version 列表不应包含
	byVer, err := repo.ListWorkstreamsByVersion(ctx, randID(), 100, 0)
	if err != nil {
		t.Fatalf("list by version: %v", err)
	}
	if len(byVer) != 0 {
		t.Fatalf("expected no rows for random version, got %d", len(byVer))
	}
}

// TestProjectKBRef_UniqueViolation 同 (project, source, doc) 第二次插入应撞 UNIQUE。
func TestProjectKBRef_UniqueViolation(t *testing.T) {
	db := testDB(t)
	repo := repository.New(db)
	ctx := context.Background()

	p := seedProject(t, db)
	src := randID()

	ref1 := &model.ProjectKBRef{
		ProjectID: p.ID, KBSourceID: src, KBDocumentID: 0,
		AttachedBy: randID(), AttachedAt: time.Now().UTC(),
	}
	if err := repo.CreateProjectKBRef(ctx, ref1); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	ref2 := &model.ProjectKBRef{
		ProjectID: p.ID, KBSourceID: src, KBDocumentID: 0,
		AttachedBy: randID(), AttachedAt: time.Now().UTC(),
	}
	err := repo.CreateProjectKBRef(ctx, ref2)
	if err == nil {
		t.Fatal("expected unique violation on duplicate attach")
	}

	// FindByTarget 应能拿到第一条
	got, err := repo.FindProjectKBRefByTarget(ctx, p.ID, src, 0)
	if err != nil {
		t.Fatalf("find by target: %v", err)
	}
	if got.ID != ref1.ID {
		t.Fatalf("expected ref1.ID=%d got=%d", ref1.ID, got.ID)
	}
}

// TestVersion_StatusBackfillTolerant 起一个挂老 status 'planned' 的 version,
// 跑 ensureVersionStatusBackfill(包在 RunMigrations 内),验证升级到 planning。
//
// 因为 testDB 已经跑过一次 migration,这里先回填一行老 status,再手动调一次 RunMigrations。
func TestVersion_StatusBackfillTolerant(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	p := seedProject(t, db)
	// 直接 raw INSERT 老 status 值
	if err := db.Exec(
		"INSERT INTO versions (project_id, name, status, is_system, created_at, updated_at) VALUES (?, ?, ?, ?, NOW(), NOW())",
		p.ID, "old-version-"+time.Now().Format("150405.000000"), "planned", false,
	).Error; err != nil {
		t.Fatalf("insert old-status version: %v", err)
	}

	// 重跑 migration,触发 status backfill
	if err := pm.RunMigrations(ctx, db, mustLogger(t), nil); err != nil {
		t.Fatalf("re-run pm migration: %v", err)
	}

	// 校验该 project 下没有 status='planned' 残留
	var count int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM versions WHERE project_id = ? AND status = ?", p.ID, "planned",
	).Scan(&count).Error; err != nil {
		t.Fatalf("count planned: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 'planned' rows after backfill, got %d", count)
	}
}

// 静态校验:确保 errors 包仍被使用(避免 go vet unused import 警告)。
var _ = errors.Is
