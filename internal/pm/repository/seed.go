package repository

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/pm"
)

// SeedProjectDefaults 给单个 project 触发 default initiative / Backlog version /
// Project Console channel + owner member 的创建。
//
// 用途:ProjectService.Create 后立即调用,保证用户从 HTTP 创建 project 后能马上
// 看到 default 资源(下次启动时 pm.RunPostMigrations 也会再次跑一遍兜底)。
//
// 设计同 pm/migration.go 的 ensureXxx 函数,但所有 SQL 都精确到 projectID,避免
// 全表扫描。SQL 全部幂等(INSERT IGNORE 或 NOT EXISTS 守卫)。
func (r *gormRepository) SeedProjectDefaults(ctx context.Context, projectID uint64) error {
	db := r.db.WithContext(ctx)

	// 1. Default initiative —— UK (project_id, name_active) 兜底,IGNORE 重复
	if err := db.Exec(`
		INSERT IGNORE INTO initiatives
			(project_id, name, description, target_outcome, status, is_system, created_by, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, 0, NOW(), NOW())
	`, projectID, pm.DefaultInitiativeName, pm.DefaultInitiativeDescription,
		pm.InitiativeStatusActive, true).Error; err != nil {
		return fmt.Errorf("seed default initiative: %w", err)
	}

	// 2. Backlog version —— UK (project_id, name) 兜底
	if err := db.Exec(`
		INSERT IGNORE INTO versions
			(project_id, name, status, is_system, created_by, created_at)
		VALUES (?, ?, ?, ?, 0, NOW())
	`, projectID, pm.BacklogVersionName, pm.VersionStatusActive, true).Error; err != nil {
		return fmt.Errorf("seed backlog version: %w", err)
	}

	// 3. Project Console channel(项目级控制台,Architect agent 工作间)。
	//    用 SELECT FROM projects + NOT EXISTS 守卫保证幂等;同 pm/migration.go 中的
	//    ensureProjectConsoleChannel 同 SQL,但限定 p.id = ? 单 project。
	if err := db.Exec(`
		INSERT INTO channels (org_id, project_id, name, purpose, status, kind, created_by, created_at, updated_at)
		SELECT p.org_id, p.id, ?, ?, ?, ?, p.created_by, NOW(), NOW()
		FROM projects p
		WHERE p.id = ?
		  AND p.archived_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM channels c
			WHERE c.project_id = p.id AND c.kind = ?
		)
	`, "Project Console", "Project-level discussion and AI Architect workspace",
		"open", "project_console", projectID, "project_console").Error; err != nil {
		return fmt.Errorf("seed project console channel: %w", err)
	}

	// 4. Console channel 加 owner 成员(creator user 的 principal_id)
	if err := db.Exec(`
		INSERT INTO channel_members (channel_id, principal_id, role, joined_at)
		SELECT c.id, u.principal_id, ?, NOW()
		FROM channels c
		INNER JOIN projects p ON p.id = c.project_id
		INNER JOIN users u ON u.id = p.created_by
		WHERE c.project_id = ?
		  AND c.kind = ?
		  AND u.principal_id <> 0
		  AND NOT EXISTS (
			SELECT 1 FROM channel_members m
			WHERE m.channel_id = c.id AND m.principal_id = u.principal_id
		)
	`, "owner", projectID, "project_console").Error; err != nil {
		return fmt.Errorf("seed project console owner: %w", err)
	}

	return nil
}
