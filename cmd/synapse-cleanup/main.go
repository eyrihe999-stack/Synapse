// cmd/synapse-cleanup 后台清理 CLI。
//
// 当前职责:
//   - **过期 pending_verify 账号 pseudo 化** —— 防攻击者用 OAuth 注册占位他人邮箱
//
// 未来扩展:其他软清理(session 审计归档、token 反侦察等)都可以接进来。
// 不做硬删(users 行物理抹除),等跨模块级联清理策略统一规划。
//
// 用法:
//
//	# 用 config 配置
//	go run ./cmd/synapse-cleanup
//
//	# 覆盖过期阈值
//	go run ./cmd/synapse-cleanup -stale-days=14 -batch=500
//
//	# 生产 cron,每天跑一次
//	0 4 * * *  /opt/synapse/synapse-cleanup >> /var/log/synapse/cleanup.log 2>&1
//
// 幂等:阈值外的账号扫不出来,反复跑无副作用。单条失败不终止批次,stats.Failed>0 退出码 2 便于告警。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	userrepo "github.com/eyrihe999-stack/Synapse/internal/user/repository"
	usersvc "github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// defaultBatchLimit 一轮最多处理多少条,足够大避免堆积。
const defaultBatchLimit = 1000

func main() {
	batch := flag.Int("batch", defaultBatchLimit, "单轮最多处理多少条过期 pending_verify 账号")
	overrideDays := flag.Int("stale-days", 0, "覆盖 config 的 PendingVerifyExpireDays(0 走 config)")
	flag.Parse()

	cfg, err := config.Load()
	must(err, "load config")

	appLogger, err := logger.GetLogger(&cfg.Log)
	must(err, "init logger")
	defer appLogger.Close()

	db, err := database.NewGormMySQL(&cfg.Database.MySQL)
	must(err, "connect MySQL")

	days := cfg.User.PendingVerifyExpireDays
	if *overrideDays > 0 {
		days = *overrideDays
	}
	if days <= 0 {
		days = 7
	}
	stale := time.Duration(days) * 24 * time.Hour

	repo := userrepo.New(db)
	// cleanup 只调 repo.ListStalePendingVerify + repo.WithTx + repo.DeleteIdentitiesByUserID + repo.MarkUserDeleted,
	// 其他依赖(session/email/policy 等)本流程不可达,传 nil + zero 即可。
	svc := usersvc.NewUserService(repo, nil, nil, 0, appLogger, nil, nil, &cfg.Email, nil, &cfg.User, nil, nil, nil, nil)

	ctx := context.Background()

	stats, err := svc.ExpireStalePendingVerifyAccounts(ctx, stale, *batch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "synapse-cleanup: pending_verify: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "synapse-cleanup: pending_verify stale=%s scanned=%d expired=%d failed=%d\n",
		stale, stats.Scanned, stats.Expired, stats.Failed)

	if stats.Failed > 0 {
		os.Exit(2) // 部分失败走非 0 退出码方便 cron 告警
	}
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "synapse-cleanup: %s: %v\n", what, err)
		os.Exit(1)
	}
}
