package database

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewGormPostgres 打开一条带连接池配置的 GORM Postgres 连接,Ping 通过后返回。
//
// 专门用于向量库(pgvector)场景,和主 MySQL 共存,各自独立连接池。
// 调用方负责判断是否配置(cfg.Host == "" 时应整段跳过,不要进入此函数)。
//
// 失败场景:
//   - DSN 参数无效 / PG 不可达 → 返回 wrap 后的原始错误,调用方通常 fatal。
//   - 连接池参数(conn_max_lifetime/idle_time)解析失败 → 同上。
func NewGormPostgres(cfg *config.PostgresConfig) (*gorm.DB, error) {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5 TimeZone=UTC",
		cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.Database, sslmode,
	)

	gormConfig := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Info),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}

	db, err := gorm.Open(postgres.Open(dsn), gormConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get postgres sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	if cfg.ConnMaxLifetime != "" {
		lifetime, err := time.ParseDuration(cfg.ConnMaxLifetime)
		if err != nil {
			return nil, fmt.Errorf("invalid postgres conn_max_lifetime %q: %w", cfg.ConnMaxLifetime, err)
		}
		sqlDB.SetConnMaxLifetime(lifetime)
	}
	if cfg.ConnMaxIdleTime != "" {
		idleTime, err := time.ParseDuration(cfg.ConnMaxIdleTime)
		if err != nil {
			return nil, fmt.Errorf("invalid postgres conn_max_idle_time %q: %w", cfg.ConnMaxIdleTime, err)
		}
		sqlDB.SetConnMaxIdleTime(idleTime)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}
	return db, nil
}

// EnablePGVectorExtension 在当前 Postgres 上启用 pgvector(幂等)。
//
// 失败说明镜像不是 pgvector/pgvector:* 系或当前角色缺少 CREATE EXTENSION 权限,
// 调用方应视为致命错误 —— 向量能力依赖此扩展。
func EnablePGVectorExtension(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return fmt.Errorf("enable pgvector extension: %w", err)
	}
	return nil
}
