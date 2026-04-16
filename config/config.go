package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the application
type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Database     DatabaseConfig     `yaml:"database"`
	Redis        RedisConfig        `yaml:"redis"`
	Log          LogConfig          `yaml:"log"`
	JWT          JWTConfig          `yaml:"jwt"`
	Snowflake    SnowflakeConfig    `yaml:"snowflake"`
	Organization OrganizationConfig `yaml:"organization"`
	Agent        AgentConfig        `yaml:"agent"`
}

type ServerConfig struct {
	Port         string        `yaml:"port"`
	Mode         string        `yaml:"mode"` // debug, release, test
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
	Host         string        `yaml:"host"`
}

type DatabaseConfig struct {
	MySQL MySQLConfig `yaml:"mysql"`
}

type MySQLConfig struct {
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	Database        string `yaml:"database"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type LogConfig struct {
	Level      string `yaml:"level"`
	Format     string `yaml:"format"` // json, text
	Output     string `yaml:"output"` // stdout, file
	FilePath   string `yaml:"file_path"`
	MaxSize    int    `yaml:"max_size"`    // MB
	MaxAge     int    `yaml:"max_age"`     // days
	MaxBackups int    `yaml:"max_backups"` // number of files
	Compress   bool   `yaml:"compress"`
}

type JWTConfig struct {
	SecretKey            string        `yaml:"secret_key"`
	AccessTokenDuration  time.Duration `yaml:"access_token_duration"`
	RefreshTokenDuration time.Duration `yaml:"refresh_token_duration"`
	Issuer               string        `yaml:"issuer"`
	MaxSessionsPerUser   int           `yaml:"max_sessions_per_user"`
}

type SnowflakeConfig struct {
	DatacenterID int64 `yaml:"datacenter_id"`
	WorkerID     int64 `yaml:"worker_id"`
}

// OrganizationConfig 组织模块配置。
// 0 值表示走 organization/service.DefaultConfig 的默认值。
type OrganizationConfig struct {
	MaxOwnedOrgs          int `yaml:"max_owned_orgs"`
	MaxJoinedOrgs         int `yaml:"max_joined_orgs"`
	InvitationExpiresDays int `yaml:"invitation_expires_days"`
}

// AgentConfig agent 模块配置。
// 0 值表示走 agent/service.DefaultConfig 的默认值。
// AES-GCM master key 从环境变量 SYNAPSE_AGENT_SECRET_KEY 读取,不放 yaml。
type AgentConfig struct {
	DefaultMaxContextRounds int `yaml:"default_max_context_rounds"`
	ChatRateLimitPerMinute  int `yaml:"chat_rate_limit_per_minute"`
	MaxTimeoutSeconds       int `yaml:"max_timeout_seconds"`
}

// Load loads configuration from YAML file and environment variables
func Load() (*Config, error) {
	env := getEnv("APP_ENV", "dev")

	// Load .env file (ok if missing)
	_ = godotenv.Load()

	cfg := &Config{}
	configPath := fmt.Sprintf("config/config.%s.yaml", env)

	if err := loadYAMLConfig(configPath, cfg); err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
	}

	overrideWithEnvVars(cfg)
	applyDefaults(cfg)

	return cfg, nil
}

func loadYAMLConfig(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	return yaml.Unmarshal(data, cfg)
}

func overrideWithEnvVars(cfg *Config) {
	if v := os.Getenv("DB_HOST"); v != "" {
		cfg.Database.MySQL.Host = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Database.MySQL.Port = p
		}
	}
	if v := os.Getenv("DB_USERNAME"); v != "" {
		cfg.Database.MySQL.Username = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		cfg.Database.MySQL.Password = v
	}
	if v := os.Getenv("DB_DATABASE"); v != "" {
		cfg.Database.MySQL.Database = v
	}
	if v := os.Getenv("REDIS_HOST"); v != "" {
		cfg.Redis.Host = v
	}
	if v := os.Getenv("REDIS_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Redis.Port = p
		}
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("JWT_SECRET_KEY"); v != "" {
		cfg.JWT.SecretKey = v
	}
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}
	if cfg.Database.MySQL.Port == 0 {
		cfg.Database.MySQL.Port = 3306
	}
	if cfg.Database.MySQL.MaxOpenConns == 0 {
		cfg.Database.MySQL.MaxOpenConns = 25
	}
	if cfg.Database.MySQL.MaxIdleConns == 0 {
		cfg.Database.MySQL.MaxIdleConns = 10
	}
	if cfg.Redis.Port == 0 {
		cfg.Redis.Port = 6379
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "text"
	}
	if cfg.Log.Output == "" {
		cfg.Log.Output = "stdout"
	}
	if cfg.JWT.Issuer == "" {
		cfg.JWT.Issuer = "synapse"
	}
	if cfg.JWT.AccessTokenDuration == 0 {
		cfg.JWT.AccessTokenDuration = 2 * time.Hour
	}
	if cfg.JWT.RefreshTokenDuration == 0 {
		cfg.JWT.RefreshTokenDuration = 168 * time.Hour
	}
	if cfg.JWT.MaxSessionsPerUser == 0 {
		cfg.JWT.MaxSessionsPerUser = 5
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
