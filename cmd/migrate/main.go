// cmd/migrate 独立触发所有模块的 DB 迁移,不需要启完整服务。
//
// 用途:
//   - CI / ops:在发布新版本前先把 schema 迁移跑完
//   - 本地开发:改了 model 后只想更新 schema 不想起 HTTP
//
// 当前只迁基础设施模块。业务模块(code / document / agent / integration 等)已整体下线,
// 新 flow 实现后在这里补回对应的 RunMigrations 调用。
//
// 用法:
//
//	go run ./cmd/migrate
//	APP_ENV=prod go run ./cmd/migrate
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	srcrepo "github.com/eyrihe999-stack/Synapse/internal/source/repository"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"gorm.io/gorm"
)

func main() {
	cfg, err := config.Load()
	must(err, "load config")

	appLogger, err := logger.GetLogger(&cfg.Log)
	must(err, "init logger")
	defer appLogger.Close()

	db, err := database.NewGormMySQL(&cfg.Database.MySQL)
	must(err, "connect MySQL")

	ctx := context.Background()
	must(user.RunMigrations(ctx, db, appLogger, nil), "user migrations")
	must(organization.RunMigrations(ctx, db, appLogger, nil), "organization migrations")
	must(permission.RunMigrations(ctx, db, appLogger, nil), "permission migrations")
	must(source.RunMigrations(ctx, db, appLogger, nil), "source migrations")
	must(asyncjob.RunMigrations(ctx, db, appLogger, nil), "asyncjob migrations")
	must(user_integration.RunMigrations(ctx, db, appLogger, nil), "user_integration migrations")

	// PG 必须配置才能跑 document migrations(向量库是硬依赖)。
	if cfg.Database.Postgres.Host == "" {
		fmt.Fprintln(os.Stderr, "migrate: PG host empty; skipping document migrations (configure postgres to enable)")
	} else {
		pgDB, err := database.NewGormPostgres(&cfg.Database.Postgres)
		must(err, "connect Postgres")
		must(database.EnablePGVectorExtension(ctx, pgDB), "enable pgvector")
		must(document.RunMigrations(ctx, pgDB, cfg.Embedding.Text.ModelDim, appLogger, nil), "document migrations")

		// M2.2 backfill:把历史 documents 的 knowledge_source_id 回填到对应的 manual_upload source。
		// 跨 DB 操作:从 PG 读 (org_id, uploader_id) 对,在 MySQL 通过 source repo lazy 创建/查 source,
		// 再回写 PG documents。已经回填过的行(knowledge_source_id != 0)被 WHERE 子句过滤,幂等。
		sourceRepository := srcrepo.New(db)
		must(backfillKnowledgeSourceID(ctx, pgDB, sourceRepository, appLogger), "backfill documents.knowledge_source_id")
	}

	fmt.Fprintln(os.Stderr, "migrate: all migrations completed")
}

// backfillKnowledgeSourceID 把所有 documents.knowledge_source_id=0 的行
// 按 (org_id, uploader_id) 分组,确保对应的 manual_upload source 存在,
// 然后批量回写 source.id 到 PG documents。
//
// 幂等:只更新 knowledge_source_id=0 的行;已回填过的不会再动。
//
// 跨 DB 注意事项:
//   - PG 读 + MySQL 写 + PG 写 三段不在一个事务里(无法跨 DB 事务),
//     失败半路重跑该函数会从未完成的对继续 —— 因为 EnsureManualUploadSource 幂等,
//     UPDATE 也只动 knowledge_source_id=0 的行,所以重跑安全。
func backfillKnowledgeSourceID(ctx context.Context, pgDB *gorm.DB, sourceRepo srcrepo.Repository, log logger.LoggerInterface) error {
	type pair struct {
		OrgID      uint64
		UploaderID uint64
	}
	var pairs []pair
	if err := pgDB.WithContext(ctx).Raw(`
		SELECT DISTINCT org_id, uploader_id
		FROM documents
		WHERE knowledge_source_id = 0
	`).Scan(&pairs).Error; err != nil {
		return fmt.Errorf("scan distinct pairs: %w", err)
	}
	if len(pairs) == 0 {
		log.InfoCtx(ctx, "M2.2 backfill: no documents need knowledge_source_id backfill", nil)
		return nil
	}

	var totalUpdated int64
	for _, p := range pairs {
		src, _, err := sourceRepo.EnsureManualUploadSource(ctx, p.OrgID, p.UploaderID)
		if err != nil {
			return fmt.Errorf("ensure manual_upload source for (org=%d, user=%d): %w", p.OrgID, p.UploaderID, err)
		}
		res := pgDB.WithContext(ctx).Exec(`
			UPDATE documents
			SET knowledge_source_id = ?, updated_at = now()
			WHERE org_id = ? AND uploader_id = ? AND knowledge_source_id = 0
		`, src.ID, p.OrgID, p.UploaderID)
		if res.Error != nil {
			return fmt.Errorf("update documents (org=%d, user=%d): %w", p.OrgID, p.UploaderID, res.Error)
		}
		totalUpdated += res.RowsAffected
		log.InfoCtx(ctx, "M2.2 backfill: pair done", map[string]any{
			"org_id":      p.OrgID,
			"uploader_id": p.UploaderID,
			"source_id":   src.ID,
			"docs":        res.RowsAffected,
		})
	}
	log.InfoCtx(ctx, "M2.2 backfill: completed", map[string]any{
		"pairs":         len(pairs),
		"docs_updated":  totalUpdated,
	})
	return nil
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %s: %v\n", what, err)
		os.Exit(1)
	}
}
