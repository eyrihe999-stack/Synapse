package user

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/principal"
	principalmodel "github.com/eyrihe999-stack/Synapse/internal/principal/model"
	"github.com/eyrihe999-stack/Synapse/internal/user/model"
)

// RunMigrations 执行 user 模块数据库迁移。
//
// 步骤:
//  1. AutoMigrate(User / UserIdentity / LoginEvent) —— 增量建表 / 列
//  2. backfillUserPrincipals:为 principal_id=0 的存量 user 创建对应 principals 行并回写
//  3. EnsureIndex uk_users_principal —— 在全部非 0 之后建唯一索引
//
// 幂等:
//   - AutoMigrate 本身幂等
//   - backfill 只处理 principal_id=0 的行;重复调用时新行已非 0,跳过
//   - EnsureIndex 按索引名查重,已存在跳过
//
// 必须在 principal.RunMigrations 之后调用(需要 principals 表存在)。
func RunMigrations(ctx context.Context, db *gorm.DB, log logger.LoggerInterface, onReady func()) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.User{}, &model.UserIdentity{}, &model.LoginEvent{}); err != nil {
		log.ErrorCtx(ctx, "user 模块 AutoMigrate 失败", err, nil)
		return fmt.Errorf("auto migrate user: %w: %w", err, ErrUserInternal)
	}
	log.InfoCtx(ctx, "user 模块 AutoMigrate 完成", nil)

	if err := backfillUserPrincipals(ctx, db, log); err != nil {
		return fmt.Errorf("backfill user principals: %w: %w", err, ErrUserInternal)
	}

	if err := database.EnsureIndex(db, database.IndexSpec{
		Table:   "users",
		Name:    "uk_users_principal",
		Columns: []string{"principal_id"},
		Unique:  true,
	}); err != nil {
		log.ErrorCtx(ctx, "user 模块 EnsureIndex uk_users_principal 失败", err, nil)
		return fmt.Errorf("ensure user principal unique index: %w: %w", err, ErrUserInternal)
	}

	if onReady != nil {
		onReady()
	}
	return nil
}

// backfillUserPrincipals 为 principal_id=0 的存量 user 创建对应 principals 行并回写。
//
// 分页查询 + 逐行同事务 INSERT principal + UPDATE user,保证单行一致;一行失败
// 不影响已处理的行(重启后只处理剩余未完成的)。
func backfillUserPrincipals(ctx context.Context, db *gorm.DB, log logger.LoggerInterface) error {
	const batchSize = 500
	backfilled := 0
	for {
		var users []model.User
		if err := db.WithContext(ctx).
			Where("principal_id = ?", 0).
			Order("id ASC").
			Limit(batchSize).
			Find(&users).Error; err != nil {
			return fmt.Errorf("scan users to backfill: %w", err)
		}
		if len(users) == 0 {
			break
		}
		for _, u := range users {
			if err := backfillOneUser(ctx, db, &u); err != nil {
				log.ErrorCtx(ctx, "user principal backfill 单行失败", err, map[string]any{"user_id": u.ID})
				return err
			}
			backfilled++
		}
	}
	if backfilled > 0 {
		log.InfoCtx(ctx, "user principal backfill 完成", map[string]any{"backfilled": backfilled})
	}
	return nil
}

// backfillOneUser 为单个 user 建 principal 行 + 回写 principal_id,整体走事务。
//
// 使用 WHERE principal_id=0 的条件更新,避免并发窗口期的重复回填覆盖(极端场景
// 下若另一进程已写入真实 id,这里 update 影响行数 = 0,不视为错误)。
func backfillOneUser(ctx context.Context, db *gorm.DB, u *model.User) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p := &principalmodel.Principal{
			Kind:        principalmodel.KindUser,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURL,
			Status:      u.Status,
		}
		if err := principal.Create(ctx, tx, p); err != nil {
			return err
		}
		res := tx.Model(&model.User{}).
			Where("id = ? AND principal_id = ?", u.ID, 0).
			Update("principal_id", p.ID)
		if res.Error != nil {
			return fmt.Errorf("update user.principal_id: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// 另一进程已回填,当前 principal 成为孤儿 —— 回滚
			return errors.New("user already backfilled by another worker")
		}
		return nil
	})
}
