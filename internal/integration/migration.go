// migration.go integration 模块的 schema 迁移。
// 三张表:
//   - user_integrations: 用户 OAuth / PAT 令牌(refresh_token、access_token 等)
//   - org_feishu_configs: 组织级飞书应用凭证(app_id / app_secret)
//   - org_gitlab_configs: 组织级 GitLab 实例配置(base_url / insecure_skip_verify)
package integration

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/integration/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// RunMigrations AutoMigrate integration 相关表。索引由 model tag 自动维护。
// 失败返原始错误;上层决定是 log 还是 fatal。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.Info("integration: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(
		&model.UserIntegration{},
		&model.OrgFeishuConfig{},
		&model.OrgGitLabConfig{},
	); err != nil {
		return fmt.Errorf("integration auto-migrate: %w", err)
	}
	// 兜底:AutoMigrate 在部分 MySQL 版本不会自动把 NOT NULL 列降级为 NULL。
	// 模型已改为 *time.Time,这里显式 ALTER 确保 GitLab PAT(无过期概念)能写 NULL 而非 '0000-00-00'。
	// MySQL 严格模式下 '0000-00-00' 会被拒,因此必须是可空列。MODIFY 幂等,重复执行是空操作。
	for _, stmt := range []string{
		"ALTER TABLE user_integrations MODIFY refresh_token_expires_at DATETIME NULL",
		"ALTER TABLE user_integrations MODIFY access_token_expires_at DATETIME NULL",
	} {
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("integration alter column nullable: %w", err)
		}
	}
	log.Info("integration: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
