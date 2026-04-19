// migration.go oauth 模块的 MySQL 建表。AutoMigrate 增量添加表/列,索引由 model tag 自动维护。
package oauth

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// RunMigrations AutoMigrate oauth 相关表。和 user / organization 模块同模式。
//
// 参数 _ interface{} 是给未来扩展留位(如 seed / lock 回调),当前只为签名对齐其他模块。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, _ any) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&model.OAuthClient{},
		&model.OAuthAuthorizationCode{},
		&model.OAuthRefreshToken{},
	); err != nil {
		log.ErrorCtx(ctx, "oauth 模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("oauth migrate: %w", err)
	}
	log.InfoCtx(ctx, "oauth 模块 AutoMigrate 完成", nil)
	return nil
}
