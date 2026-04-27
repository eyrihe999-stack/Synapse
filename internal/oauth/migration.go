// Package oauth OAuth 2.1 AS 模块。详见 internal/oauth/const.go。
package oauth

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/model"
)

// RunMigrations OAuth + PAT 模块数据库迁移。
//
// 5 张表靠 AutoMigrate(struct tag 建列 + 普通索引 + UNIQUE);没有生成列 / 复杂 DDL。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	log.InfoCtx(ctx, "oauth: running MySQL migrations", nil)
	if err := db.WithContext(ctx).AutoMigrate(
		&model.OAuthClient{},
		&model.OAuthAuthorizationCode{},
		&model.OAuthAccessToken{},
		&model.OAuthRefreshToken{},
		&model.UserPAT{},
	); err != nil {
		return fmt.Errorf("oauth auto-migrate: %w: %w", err, ErrOAuthInternal)
	}
	log.InfoCtx(ctx, "oauth: migrations completed", nil)
	if onReady != nil {
		onReady()
	}
	return nil
}
