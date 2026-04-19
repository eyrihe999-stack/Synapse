// cmd/migrate 独立触发所有模块的 DB 迁移,不需要启完整服务。
//
// 用途:
//   - CI / ops:在发布新版本前先把 schema 迁移跑完
//   - 本地开发:改了 model 后只想更新 schema 不想起 HTTP
//
// 执行顺序和 cmd/synapse 启动逻辑保持一致(user → organization → agent → document),
// 避免子模块的外键/冗余字段出现在尚未建表之前。
//
// 用法:
//   go run ./cmd/migrate
//   APP_ENV=prod go run ./cmd/migrate
package main

import (
	"context"
	"fmt"
	"os"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/agent"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/integration"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/pkg/database"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

func main() {
	cfg, err := config.Load()
	must(err, "load config")

	appLogger, err := logger.GetLogger(&cfg.Log)
	must(err, "init logger")
	defer appLogger.Close()

	db, err := database.NewGormMySQL(&cfg.Database.MySQL)
	must(err, "connect MySQL")

	var pgDB *gorm.DB
	if cfg.Database.Postgres.Host != "" {
		pgDB, err = database.NewGormPostgres(&cfg.Database.Postgres)
		must(err, "connect Postgres")
		must(database.EnablePGVectorExtension(context.Background(), pgDB), "enable pgvector")
	}

	ctx := context.Background()
	must(user.RunMigrations(ctx, db, appLogger, nil), "user migrations")
	must(organization.RunMigrations(ctx, db, appLogger, nil), "organization migrations")
	must(agent.RunMigrations(ctx, db, appLogger, nil), "agent migrations")
	must(document.RunMigrations(ctx, db, pgDB, appLogger, nil), "document migrations")
	must(integration.RunMigrations(ctx, db, appLogger, nil), "integration migrations")

	fmt.Fprintln(os.Stderr, "migrate: all migrations completed")
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %s: %v\n", what, err)
		os.Exit(1)
	}
}
