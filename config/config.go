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
	OSS          OSSConfig          `yaml:"oss"`
	Document     DocumentConfig     `yaml:"document"`
	Embedding    EmbeddingConfig    `yaml:"embedding"`
	Reranker     RerankerConfig     `yaml:"reranker"`
	Feishu       FeishuConfig       `yaml:"feishu"`
	OAuth        OAuthConfig        `yaml:"oauth"`
}

type ServerConfig struct {
	Port            string        `yaml:"port"`
	Mode            string        `yaml:"mode"` // debug, release, test
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"` // 优雅关闭时等待 in-flight 请求的最长时间
	Host            string        `yaml:"host"`
}

type DatabaseConfig struct {
	MySQL    MySQLConfig    `yaml:"mysql"`
	Postgres PostgresConfig `yaml:"postgres"`
}

type MySQLConfig struct {
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	Database        string `yaml:"database"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`  // 连接最长存活时间,防长连接"过期"(MySQL wait_timeout、LB 切主等)
	ConnMaxIdleTime string `yaml:"conn_max_idle_time"` // 连接最长空闲时间,回收突发后滞留的 idle 连接
}

// PostgresConfig 向量库(pgvector)连接参数。
// Host 为空 = 未配置,启动时自动跳过 pg 连接与向量索引能力,其他模块照常运行。
type PostgresConfig struct {
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	Database        string `yaml:"database"`
	SSLMode         string `yaml:"ssl_mode"` // disable / require / verify-full;本机 dev 用 disable
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime string `yaml:"conn_max_idle_time"`
}

type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	// 连接池大小,默认按 CPU * 10。高并发限流场景下 Redis 命令多,池打满会阻塞 PoolTimeout。
	PoolSize int `yaml:"pool_size"`
	// 单条命令超时(连接 + 读 + 写各自适用)。Redis 抖动时不至于把整条 chat 链路拖死。
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// 池满时等待空闲连接的最长时间。
	PoolTimeout time.Duration `yaml:"pool_timeout"`
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

// OAuthConfig OAuth 2.1 Authorization Server(MCP 远端接入)相关配置。
//
// Issuer:对外 base URL,写入 JWT iss claim + .well-known 发现端点;部署时必须和用户访问的公网
// 域名严格一致(含 scheme + 去尾斜杠)。不设 = 整个 OAuth 模块禁用。
//
// SigningKey:HS256 access token 签名密钥,建议 ≥ 32 字节强随机。和 web 登录 JWT 的 secret_key 分开。
// CookieSecret:/oauth 流程 cookie 的 HMAC-SHA256 密钥;可和 SigningKey 同值也可独立(推荐独立)。
// MCPResourceURL:/.well-known/oauth-protected-resource 指向的 MCP endpoint 绝对 URL。
// CookieSecure:生产必须 true(HTTPS-only);本地 dev 才设 false。
type OAuthConfig struct {
	Issuer         string `yaml:"issuer"`
	SigningKey     string `yaml:"signing_key"`
	CookieSecret   string `yaml:"cookie_secret"`
	MCPResourceURL string `yaml:"mcp_resource_url"`
	CookieSecure   bool   `yaml:"cookie_secure"`
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
// 注:agent 请求超时上下界(MinTimeoutSeconds/MaxTimeoutSeconds)是硬编码常量,
// 在 internal/agent/const.go 里调整,不走 yaml。
type AgentConfig struct {
	DefaultMaxContextRounds int `yaml:"default_max_context_rounds"`
	ChatRateLimitPerMinute  int `yaml:"chat_rate_limit_per_minute"`
	// AllowPrivateEndpoints 是否允许 agent endpoint 指向 RFC1918 / IPv6 ULA 私网地址。
	// 未设置(nil)时默认 true,兼容 Docker / K8s 同网络部署场景。
	// loopback 和 link-local(含云元数据 169.254.169.254)始终拦截,与此开关无关。
	// 详见 internal/agent/endpoint_guard.go。
	AllowPrivateEndpoints *bool `yaml:"allow_private_endpoints"`
}

// OSSConfig 对象存储配置。当前仅支持 aliyun provider。
// 所有上传对象的 key 会带上 PathPrefix + "/" + {org_id} + ...,实现租户隔离。
type OSSConfig struct {
	Provider        string `yaml:"provider"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	Endpoint        string `yaml:"endpoint"`
	Domain          string `yaml:"domain"`
	PathPrefix      string `yaml:"path_prefix"`
}

// DocumentConfig 文档模块配置(仅上传限制)。
type DocumentConfig struct {
	Upload DocumentUploadConfig `yaml:"upload"`
}

type DocumentUploadConfig struct {
	MaxFileSizeBytes int64    `yaml:"max_file_size_bytes"`
	AllowedMIMETypes []string `yaml:"allowed_mime_types"`
}

// EmbeddingConfig 顶层向量化配置。各模态独立一块:text 现在,image/code 未来。
// 每块独立的 provider + 维度,避免不同模态共享配置后维度漂移。
type EmbeddingConfig struct {
	Text EmbeddingProviderConfig `yaml:"text"`
}

// EmbeddingProviderConfig 单一模态的向量化 provider 配置。
// Provider == "fake" 时 Azure 字段可以留空,用于 dev 离线调试。
type EmbeddingProviderConfig struct {
	Provider string               `yaml:"provider"`  // "azure" | "fake"
	ModelDim int                  `yaml:"model_dim"` // text-embedding-3-large = 3072
	Azure    AzureEmbeddingConfig `yaml:"azure"`
}

// AzureEmbeddingConfig Azure OpenAI embedding 接入参数。
// APIKey 支持通过 AZURE_EMBEDDING_API_KEY 环境变量覆盖,便于生产外挂密钥。
type AzureEmbeddingConfig struct {
	Endpoint   string `yaml:"endpoint"`    // 例如 https://{resource}.openai.azure.com/openai/v1/
	Deployment string `yaml:"deployment"`  // 例如 text-embedding-3-large
	APIKey     string `yaml:"api_key"`     // dev 可写 yaml;生产走 env 覆盖
	APIVersion string `yaml:"api_version"` // 默认 2024-10-21
}

// RerankerConfig T1.2 二阶段重排 provider 配置。Provider 空串 = 禁用。
// 当前仅 bge_tei 一家(BGE-reranker 走 HuggingFace TEI 服务),未来可扩 cohere / voyage。
type RerankerConfig struct {
	// Provider "" | "bge_tei";其他值在 main 构造期 warn 然后降级为禁用。
	Provider string `yaml:"provider"`
	BGETEI   BGETEIConfig `yaml:"bge_tei"`
}

// BGETEIConfig BGE-reranker 走 HuggingFace text-embeddings-inference 服务的参数。
// 启动方式(参考 docs/reranker.md):
//   docker run -p 8082:80 ghcr.io/huggingface/text-embeddings-inference:cpu-latest --model-id BAAI/bge-reranker-v2-m3
// 生产按量装 GPU 版本。Timeout 建议 2-5s 覆盖冷启动和批量高位。
type BGETEIConfig struct {
	BaseURL string `yaml:"base_url"` // 例如 http://127.0.0.1:8082
	Timeout string `yaml:"timeout"`  // 解析为 time.Duration,空串 = 5s
}

// FeishuConfig 飞书 OAuth 集成的部署级配置。
// 注意:应用凭证 app_id / app_secret **不在此结构**,改为 per org 存在 org_feishu_configs 表,
// 由 org admin 在前端填入。这里只保留部署级(所有 org 共享)的配置:
//   - BaseURL:飞书区域(国内/海外),部署绑定
//   - RedirectURI:OAuth 回调地址,域名级,所有 org 共享同一回调
//   - FrontendRedirectURL:回调完成后 302 跳回前端的页面(带 ?feishu=success|error)
type FeishuConfig struct {
	BaseURL             string `yaml:"base_url"`     // 空 = 默认中国区 https://open.feishu.cn
	RedirectURI         string `yaml:"redirect_uri"` // 例如 https://synapse.example.com/api/v2/integrations/feishu/callback
	FrontendRedirectURL string `yaml:"frontend_redirect_url"`
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
	if v := os.Getenv("OSS_ACCESS_KEY_ID"); v != "" {
		cfg.OSS.AccessKeyID = v
	}
	if v := os.Getenv("OSS_ACCESS_KEY_SECRET"); v != "" {
		cfg.OSS.AccessKeySecret = v
	}
	if v := os.Getenv("PG_HOST"); v != "" {
		cfg.Database.Postgres.Host = v
	}
	if v := os.Getenv("PG_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Database.Postgres.Port = p
		}
	}
	if v := os.Getenv("PG_USERNAME"); v != "" {
		cfg.Database.Postgres.Username = v
	}
	if v := os.Getenv("PG_PASSWORD"); v != "" {
		cfg.Database.Postgres.Password = v
	}
	if v := os.Getenv("PG_DATABASE"); v != "" {
		cfg.Database.Postgres.Database = v
	}
	if v := os.Getenv("PG_SSL_MODE"); v != "" {
		cfg.Database.Postgres.SSLMode = v
	}
	if v := os.Getenv("AZURE_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.Text.Azure.APIKey = v
	}
	if v := os.Getenv("OAUTH_SIGNING_KEY"); v != "" {
		cfg.OAuth.SigningKey = v
	}
	if v := os.Getenv("OAUTH_COOKIE_SECRET"); v != "" {
		cfg.OAuth.CookieSecret = v
	}
	if v := os.Getenv("OAUTH_ISSUER"); v != "" {
		cfg.OAuth.Issuer = v
	}
	if v := os.Getenv("OAUTH_MCP_RESOURCE_URL"); v != "" {
		cfg.OAuth.MCPResourceURL = v
	}
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Server.ShutdownTimeout == 0 {
		// 默认 30s 覆盖大多数对话完成;ops 想完整等 SSE 流可上调至 agent.MaxTimeoutSeconds(300s)。
		cfg.Server.ShutdownTimeout = 30 * time.Second
	}
	if cfg.Database.MySQL.Port == 0 {
		cfg.Database.MySQL.Port = 3306
	}
	if cfg.Database.MySQL.MaxOpenConns == 0 {
		cfg.Database.MySQL.MaxOpenConns = 50
	}
	if cfg.Database.MySQL.MaxIdleConns == 0 {
		cfg.Database.MySQL.MaxIdleConns = 25
	}
	if cfg.Database.MySQL.ConnMaxLifetime == "" {
		cfg.Database.MySQL.ConnMaxLifetime = "15m"
	}
	if cfg.Database.MySQL.ConnMaxIdleTime == "" {
		// 比 ConnMaxLifetime 短,突发洪峰后的 idle 连接能被及时回收。
		cfg.Database.MySQL.ConnMaxIdleTime = "5m"
	}
	if cfg.Redis.Port == 0 {
		cfg.Redis.Port = 6379
	}
	if cfg.Redis.PoolSize == 0 {
		// go-redis 默认 10 * GOMAXPROCS,这里给个保底,避免极小机器误配。
		cfg.Redis.PoolSize = 50
	}
	if cfg.Redis.DialTimeout == 0 {
		cfg.Redis.DialTimeout = 2 * time.Second
	}
	if cfg.Redis.ReadTimeout == 0 {
		cfg.Redis.ReadTimeout = 500 * time.Millisecond
	}
	if cfg.Redis.WriteTimeout == 0 {
		cfg.Redis.WriteTimeout = 500 * time.Millisecond
	}
	if cfg.Redis.PoolTimeout == 0 {
		// 略大于 ReadTimeout 即可,池满时短等让请求快速失败 → 触发限流的本地降级。
		cfg.Redis.PoolTimeout = 1 * time.Second
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
	if cfg.Agent.AllowPrivateEndpoints == nil {
		t := true
		cfg.Agent.AllowPrivateEndpoints = &t
	}

	// OSS 默认值
	if cfg.OSS.Provider == "" {
		cfg.OSS.Provider = "aliyun"
	}

	// Document 默认值
	if cfg.Document.Upload.MaxFileSizeBytes == 0 {
		cfg.Document.Upload.MaxFileSizeBytes = 10 * 1024 * 1024 // 10MB
	}
	if len(cfg.Document.Upload.AllowedMIMETypes) == 0 {
		cfg.Document.Upload.AllowedMIMETypes = []string{
			"text/markdown",
			"text/plain",
			"text/x-markdown",
		}
	}

	// Postgres 默认值(仅在 Host 已配置时有意义;Host 为空代表"整段不启用")。
	if cfg.Database.Postgres.Port == 0 {
		cfg.Database.Postgres.Port = 5432
	}
	if cfg.Database.Postgres.SSLMode == "" {
		cfg.Database.Postgres.SSLMode = "disable"
	}
	if cfg.Database.Postgres.MaxOpenConns == 0 {
		// 向量库读写远低于主 MySQL,50 足够 PRD agent + 上传索引并发。
		cfg.Database.Postgres.MaxOpenConns = 50
	}
	if cfg.Database.Postgres.MaxIdleConns == 0 {
		cfg.Database.Postgres.MaxIdleConns = 25
	}
	if cfg.Database.Postgres.ConnMaxLifetime == "" {
		cfg.Database.Postgres.ConnMaxLifetime = "1h"
	}
	if cfg.Database.Postgres.ConnMaxIdleTime == "" {
		cfg.Database.Postgres.ConnMaxIdleTime = "5m"
	}

	// Embedding 默认值:未填 provider 时留空(调用方检测并降级);Azure API 版本兜底。
	if cfg.Embedding.Text.Azure.APIVersion == "" {
		cfg.Embedding.Text.Azure.APIVersion = "2024-10-21"
	}

}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
