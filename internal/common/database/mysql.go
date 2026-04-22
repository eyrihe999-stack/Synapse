package database

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	_ "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DatabaseInterface defines the database contract
type DatabaseInterface interface {
	Close() error
	Ping() error
}

// SQLDatabaseInterface extends DatabaseInterface for SQL databases
type SQLDatabaseInterface interface {
	DatabaseInterface
	GetDB() *sql.DB
}

// NoSQLDatabaseInterface for NoSQL databases like Redis
type NoSQLDatabaseInterface interface {
	DatabaseInterface
}

// MySQLDatabase implements MySQL database connection
type MySQLDatabase struct {
	db     *sql.DB
	config *config.MySQLConfig
}

// NewMySQL creates a new MySQL database connection
//sayso-lint:ignore godoc-error-undoc
func NewMySQL(cfg *config.MySQLConfig) (SQLDatabaseInterface, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s&readTimeout=10s&writeTimeout=10s",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
	)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	if err := applyPoolConfig(db, cfg); err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &MySQLDatabase{
		db:     db,
		config: cfg,
	}, nil
}

// applyPoolConfig 统一应用连接池配置,避免两个构造函数重复。
func applyPoolConfig(db *sql.DB, cfg *config.MySQLConfig) error {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)

	if cfg.ConnMaxLifetime != "" {
		lifetime, err := time.ParseDuration(cfg.ConnMaxLifetime)
		if err != nil {
			return fmt.Errorf("invalid conn_max_lifetime %q: %w", cfg.ConnMaxLifetime, err)
		}
		db.SetConnMaxLifetime(lifetime)
	}
	if cfg.ConnMaxIdleTime != "" {
		idleTime, err := time.ParseDuration(cfg.ConnMaxIdleTime)
		if err != nil {
			return fmt.Errorf("invalid conn_max_idle_time %q: %w", cfg.ConnMaxIdleTime, err)
		}
		db.SetConnMaxIdleTime(idleTime)
	}
	return nil
}

func (m *MySQLDatabase) GetDB() *sql.DB { return m.db }

func (m *MySQLDatabase) Close() error {
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

func (m *MySQLDatabase) Ping() error { return m.db.Ping() }

// NewGormMySQL creates a new MySQL database connection using GORM
//sayso-lint:ignore godoc-error-undoc
func NewGormMySQL(cfg *config.MySQLConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=UTC&timeout=5s&readTimeout=10s&writeTimeout=10s",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
	)

	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}

	db, err := gorm.Open(mysql.Open(dsn), gormConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database instance: %w", err)
	}

	if err := applyPoolConfig(sqlDB, cfg); err != nil {
		return nil, err
	}

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
