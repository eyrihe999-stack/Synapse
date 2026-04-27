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
	Embedding    EmbeddingConfig    `yaml:"embedding"`
	LLM          LLMConfig          `yaml:"llm"`
	Email        EmailConfig        `yaml:"email"`
	User         UserConfig         `yaml:"user"`
	OAuthLogin   OAuthLoginConfig   `yaml:"oauth_login"`
	OSS          OSSConfig          `yaml:"oss"`
	EventBus     EventBusConfig     `yaml:"eventbus"`
	OAuth        OAuthConfig        `yaml:"oauth"`
	AgentSys     AgentSysConfig     `yaml:"agentsys"`
}

// OAuthConfig MCP 专用 OAuth 2.1 AS 配置。
//
// 服务于 Claude Desktop / Cursor 等 remote MCP 接入(详见 docs/collaboration-design.md §3.6.2)。
// **不**用于 user 登录(Google OIDC 是另一条链,在 OAuthLogin 配置)。
//
// 字段语义:
//
//	Issuer              AS 的基 URL,出现在 .well-known metadata 里;必须匹配外网可达域名。
//	                    dev 本地 docker 可填 "http://localhost:8080",生产必须 https
//	AccessTokenTTL      access_token 存活时长;默认 24h
//	RefreshTokenTTL     refresh_token 存活时长;默认 30d
//	AuthorizationCodeTTL  authorize → token exchange 的窗口;默认 10m
//	DCRRateLimitWindow  DCR per-IP 滑动窗口秒数;默认 60
//	DCRRateLimitMax     DCR 单 IP 每窗口最多注册次数;默认 10
type OAuthConfig struct {
	Issuer               string        `yaml:"issuer"`
	AccessTokenTTL       time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL      time.Duration `yaml:"refresh_token_ttl"`
	AuthorizationCodeTTL time.Duration `yaml:"authorization_code_ttl"`
	DCRRateLimitWindow   int           `yaml:"dcr_rate_limit_window"` // seconds
	DCRRateLimitMax      int           `yaml:"dcr_rate_limit_max"`
}

// EventBusConfig 基于 Redis Streams 的事件总线配置。
//
// 职责:asyncjob 完成事件 / channel 消息事件 / task 状态事件等的发布通道。
// 物理后端复用 Config.Redis 已连上的 client(不另建连接),这里只放 stream 相关参数。
//
// 字段语义:
//
//	MaxLen            单 stream 的近似 MAXLEN(XADD 裁剪)。默认 100000,保近 10 万条够审计 + 冷启动重放。
//	AsyncJobStream    asyncjob 完成事件 stream key。默认 "synapse:asyncjob:events"。
//	WorkflowStream    workflow 内部跃迁 stream key。默认 "synapse:workflow:events"(已冻结,保留兼容,PR #5' 之后废弃)。
//	ChannelStream     channel 消息 / @mention / 归档事件 stream key。默认 "synapse:channel:events"。
//	TaskStream        task 状态变更事件 stream key。默认 "synapse:task:events"。
//
// 未来扩展:若换 NATS / Kafka 作为后端,这里加 `Backend string` + 对应 provider 子块。
type EventBusConfig struct {
	MaxLen         int    `yaml:"max_len"`
	AsyncJobStream string `yaml:"asyncjob_stream"`
	WorkflowStream string `yaml:"workflow_stream"`
	ChannelStream  string `yaml:"channel_stream"`
	TaskStream     string `yaml:"task_stream"`

	// ResetGroupsOnStart 启动时是否 destroy + 重建 channel/task stream 上注册的 consumer group
	// (top-orchestrator + channel-event-card-writer)。
	//
	// true:每次重启清光 stale consumer + PEL,group 从干净状态开始
	//   适合:dev / 单实例部署 / 不在乎 in-flight 事件
	// false(默认):保留 group 跨重启,stale consumer 累积(SweepIdleConsumers 保守清理)
	//   适合:生产 / 多实例部署(否则启动时会互相清掉 in-flight)
	ResetGroupsOnStart bool `yaml:"reset_groups_on_start"`
}

// OSSConfig 阿里云 OSS 接入参数 + 文档版本策略。
//
// 字段语义:
//
//	AccessKeyID / AccessKeySecret   访问凭证。本地部署直接写 yaml。
//	Region / Bucket                  bucket 归属 region(cn-hangzhou 等) + bucket 名。
//	Endpoint                         可选;为空时自动拼 oss-<region>.aliyuncs.com。
//	Domain                           可选;为空时走 bucket 的默认域(用于返还给前端的 URL)。
//	PathPrefix                       OSS key 顶层前缀,默认 "synapse"。同一 bucket 多服务共用时用它隔离。
//	MaxVersionsPerDocument           单文档最多保留几个历史版本,超过就删最老的。默认 10。
type OSSConfig struct {
	AccessKeyID            string `yaml:"access_key_id"`
	AccessKeySecret        string `yaml:"access_key_secret"`
	Region                 string `yaml:"region"`
	Bucket                 string `yaml:"bucket"`
	Endpoint               string `yaml:"endpoint"`
	Domain                 string `yaml:"domain"`
	PathPrefix             string `yaml:"path_prefix"`
	MaxVersionsPerDocument int    `yaml:"max_versions_per_document"`
}

type ServerConfig struct {
	Port            string        `yaml:"port"`
	Mode            string        `yaml:"mode"` // debug, release, test
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"` // 优雅关闭时等待 in-flight 请求的最长时间
	// TrustedProxies 可信反代 CIDR / IP 列表,gin 仅在白名单内解析 X-Forwarded-For / X-Real-IP。
	// 空 = 不信任任何代理,直接用 socket IP(安全默认,防伪造)。
	// 生产:填 ingress / LB 的 IP 段,如 ["10.0.0.0/8", "172.16.0.0/12"]。
	// 直接暴露公网时务必保持空。
	TrustedProxies []string `yaml:"trusted_proxies"`
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
	Level      string    `yaml:"level"`
	Format     string    `yaml:"format"` // json, text
	Output     string    `yaml:"output"` // stdout, file
	FilePath   string    `yaml:"file_path"`
	MaxSize    int       `yaml:"max_size"`    // MB
	MaxAge     int       `yaml:"max_age"`     // days
	MaxBackups int       `yaml:"max_backups"` // number of files
	Compress   bool      `yaml:"compress"`
	SLS        SLSConfig `yaml:"sls"` // 阿里云 SLS 日志投递;enabled=false 时不构造 hook。
}

// SLSConfig 与 sayso-server 字段完全对齐,便于以后跨服务查询相同 trace_id。
// 生产环境把 access_key_* 放 env(SLS_ACCESS_KEY_ID / SLS_ACCESS_KEY_SECRET),
// 本地 dev 直接在 yaml 里填明文即可(默认 enabled: false 也不会真的发)。
type SLSConfig struct {
	Enabled             bool              `yaml:"enabled"`
	Endpoint            string            `yaml:"endpoint"`
	AccessKeyID         string            `yaml:"access_key_id"`
	AccessKeySecret     string            `yaml:"access_key_secret"`
	Project             string            `yaml:"project"`
	Logstore            string            `yaml:"logstore"`
	Topic               string            `yaml:"topic"`
	Source              string            `yaml:"source"`
	MaxBatchSize        int               `yaml:"max_batch_size"`        // Max batch size in bytes
	MaxBatchCount       int               `yaml:"max_batch_count"`       // Max logs per batch
	LingerMs            int               `yaml:"linger_ms"`             // Max wait before flushing a batch (ms)
	Retries             int               `yaml:"retries"`               // Retries for failed requests
	MaxReservedAttempts int               `yaml:"max_reserved_attempts"` // Max reserved retry attempts
	Metadata            map[string]string `yaml:"metadata"`              // Extra metadata attached to every log
}

type JWTConfig struct {
	SecretKey            string        `yaml:"secret_key"`
	AccessTokenDuration  time.Duration `yaml:"access_token_duration"`
	RefreshTokenDuration time.Duration `yaml:"refresh_token_duration"`
	Issuer               string        `yaml:"issuer"`
	MaxSessionsPerUser   int           `yaml:"max_sessions_per_user"`
	// AbsoluteSessionTTL session 的绝对过期时间(首次登录起算,不因 refresh 延长)。
	// 超过即便 refresh token 还在有效期也强制重登。0 = 走代码默认 30d;
	// 设 0 意味着长期活跃用户 session 永不过期,被盗 token 如定期 refresh 可持续等同账号寿命。
	AbsoluteSessionTTL time.Duration `yaml:"absolute_session_ttl"`
}

type SnowflakeConfig struct {
	DatacenterID int64 `yaml:"datacenter_id"`
	WorkerID     int64 `yaml:"worker_id"`
}

// OrganizationConfig 组织模块配置。
// 0 值表示走 organization/service.DefaultConfig 的默认值。
type OrganizationConfig struct {
	MaxOwnedOrgs  int `yaml:"max_owned_orgs"`
	MaxJoinedOrgs int `yaml:"max_joined_orgs"`

	// InvitationFrontendBaseURL 邀请邮件里落地页的 URL 前缀,邮件里会拼成
	// "{InvitationFrontendBaseURL}?token={raw_token}"。
	// dev 可留空,service 层退化为直接把 raw token 放进邮件(打日志即可 end-to-end 测)。
	// 例:"https://app.example.com/invite"
	InvitationFrontendBaseURL string `yaml:"invitation_frontend_base_url"`
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

// AgentSysConfig 顶级系统 agent runtime(internal/agentsys)的运行参数。
//
// 字段语义:
//
//	Concurrency 同一进程内并发消费 channel @ 事件的 consumer 数。Redis Streams
//	            consumer group 天然把事件轮派给 N 个 consumer(name=hostname-0/1/...),
//	            实现进程内 N 路并行处理;多 pod 部署时再叠加 pod 级扩展。
//	            0/<=1 视为串行(老路径兼容);缺省 3;上限 32(防配错把 LLM rate limit 打爆)。
//	            注意:不做 per-channel 串行化,同 channel 两条消息**可能**并行,
//	            在活跃场景概率低;真出问题再加 channelID 维度的 mutex。
type AgentSysConfig struct {
	Concurrency int `yaml:"concurrency"`
}

// LLMConfig 顶级 / 专项系统 agent 调用 LLM 的配置(PR #6' 起)。
//
// 与 EmbeddingConfig **独立分离**,因为 LLM 与 embedding 常落在不同的 Azure 资源 /
// 额度池上(生产里也可能是不同的 endpoint 和 key)。这里只支持 Azure provider 一种 ——
// **不提供 fake**,以保证 dev / staging / prod 的行为完全一致:测试替身通过 mock
// llm.Chat 接口实现,不走 factory。
//
// 字段语义:
//
//	Provider             当前只允许 "azure";非法值在 llm.NewFromConfig 构造时 fatal
//	Azure                Azure OpenAI chat completions 接入参数
//	DailyBudgetPerOrgUSD 每 org 每日 LLM 花费上限(美元);0 = 不限
//	                      runtime 每次回复前查 llm_usage 当天 SUM(cost_usd) 比较
//	RequestTimeoutSec    单次 LLM HTTP 请求超时;缺省 60
type LLMConfig struct {
	Provider             string         `yaml:"provider"`
	Azure                AzureLLMConfig `yaml:"azure"`
	DailyBudgetPerOrgUSD float64        `yaml:"daily_budget_per_org_usd"`
	RequestTimeoutSec    int            `yaml:"request_timeout_sec"`
}

// AzureLLMConfig Azure OpenAI chat completions 接入参数。
// APIKey 支持通过 AZURE_LLM_API_KEY 环境变量覆盖。
type AzureLLMConfig struct {
	Endpoint   string `yaml:"endpoint"`    // 例如 https://{resource}.openai.azure.com/openai/v1/
	Deployment string `yaml:"deployment"`  // 例如 gpt-5.4(对应 Azure 部署名)
	APIKey     string `yaml:"api_key"`     // dev 留空 yaml 模板,真值走 config.local.yaml 或 env
	APIVersion string `yaml:"api_version"` // v1 surface 下用不到;传统 surface 默认 2024-10-21
}

// EmailConfig 邮件发送配置(用于邮箱验证码等场景)。
//
// Provider 决定发送通道:
//   - "resend"  走 Resend HTTP API,只需 APIKey
//   - "smtp"    走 SMTP + TLS,需要 SMTPHost/Port/Username/Password
//   - ""        不发送(dev 本地可只写 Redis 然后看日志取码)
//
// 敏感字段(api_key / password / from / smtp_host 等)统一留空 yaml,
// 生产环境通过 EMAIL_* env 注入,和 OSS/OAuth 的做法一致。
//
// DailyVerificationLimit: 每邮箱每天最多允许的发码次数,0 走代码默认值(10)。
// CodeTTL:  单条验证码的有效期,0 走默认 10min。
// MaxAttempts: 单条码最多允许验错几次,超过即作废,0 走默认 5。
// Locale:   模板选择,"zh" 走中文模板,其他值回退英文。
type EmailConfig struct {
	Provider               string `yaml:"provider"`
	SMTPHost               string `yaml:"smtp_host"`
	SMTPPort               int    `yaml:"smtp_port"`
	Username               string `yaml:"username"`
	Password               string `yaml:"password"`
	From                   string `yaml:"from"`
	FromName               string `yaml:"from_name"`
	APIKey                 string `yaml:"api_key"`
	DailyVerificationLimit int    `yaml:"daily_verification_limit"`
	CodeTTL                string `yaml:"code_ttl"`    // 解析为 time.Duration,空=10m
	MaxAttempts            int    `yaml:"max_attempts"`
	Locale                 string `yaml:"locale"`
	// PasswordResetTTL 密码重置 token 有效期,空=15m。
	PasswordResetTTL string `yaml:"password_reset_ttl"`
	// PasswordResetLinkBase 重置邮件里 confirm 链接的前端基础 URL
	// (末尾不带斜杠,service 拼 "/reset-password?token=...")。空 = 用 From 域名猜不出,必须显式配。
	PasswordResetLinkBase string `yaml:"password_reset_link_base"`
	// VerificationTTL M1.1 邮箱激活 token 有效期,空=24h。
	VerificationTTL string `yaml:"verification_ttl"`
	// VerificationLinkBase 激活邮件里的前端基础 URL (末尾不带斜杠),
	// service 会拼 "/auth/email/verify?token=..."。空 = 落回 PasswordResetLinkBase,都为空则激活流程不可用。
	VerificationLinkBase string `yaml:"verification_link_base"`
}

// UserConfig user 模块的安全相关配置。
//
// 所有时长 / 计数的 0 值都走代码默认,便于生产只注入关键字段即可起。
//
//	LoginFail.Max          连续登录失败多少次锁账号,默认 10
//	LoginFail.LockTTL      锁定多久,默认 15m (Go duration string)
//	RegisterRate.Max       每 IP 滑动窗口内允许多少次 /register,默认 5
//	RegisterRate.WindowSec 滑动窗口时长(秒),默认 60
//	Password.MinLen        密码最短长度,默认 10(M1.5)
//	Password.CheckWeakList 是否启用 top-10k 弱密 bloom 校验,默认 true
type UserConfig struct {
	LoginFail    LoginFailConfig      `yaml:"login_fail"`
	RegisterRate RegisterRateConfig   `yaml:"register_rate"`
	Password     PasswordPolicyConfig `yaml:"password"`
	// PendingVerifyExpireDays M1.7 OAuth 未验证账号的过期清理天数,超过此天数且仍 pending_verify
	// 会被 synapse-cleanup CLI pseudo 化(不是硬删),释放原 email 供真用户注册。0=用代码默认 7 天。
	PendingVerifyExpireDays int `yaml:"pending_verify_expire_days"`
}

type LoginFailConfig struct {
	Max     int    `yaml:"max"`
	LockTTL string `yaml:"lock_ttl"`
}

type RegisterRateConfig struct {
	Max       int `yaml:"max"`
	WindowSec int `yaml:"window_sec"`
}

// OAuthLoginConfig M1.6 第三方登录(Synapse 作为 RP,IdP=Google/Feishu/...)。
//
// 敏感字段统一放 env:
//
//	GOOGLE_OAUTH_CLIENT_ID=...
//	GOOGLE_OAUTH_CLIENT_SECRET=...
//
// RedirectURI:必须和 Google 开发者后台登记的一致;公网部署走 https。
// FrontendRedirectBase:前端回调页的 origin,callback 成功后后端 302 到
// "{base}/auth/oauth/callback?exchange={code}" 让前端换取 tokens。
// StateCookieSecret:HMAC 状态 cookie 用;不设时启动 fatal(避免明文 state 被篡改)。
// CookieSecure:HTTPS-only 标志,生产 true,本地 dev false。
type OAuthLoginConfig struct {
	StateCookieSecret string            `yaml:"state_cookie_secret"`
	CookieSecure      bool              `yaml:"cookie_secure"`
	Google            GoogleOAuthConfig `yaml:"google"`
}

// GoogleOAuthConfig Google OIDC 客户端参数。Enabled 为 false 时 /auth/oauth/google/* 全部 404。
type GoogleOAuthConfig struct {
	Enabled              bool   `yaml:"enabled"`
	ClientID             string `yaml:"client_id"`
	ClientSecret         string `yaml:"client_secret"`
	RedirectURI          string `yaml:"redirect_uri"`
	FrontendRedirectBase string `yaml:"frontend_redirect_base"`
}

// PasswordPolicyConfig M1.5 密码策略。
//
//	MinLen        最短长度,0/负值 → 默认 10
//	CheckWeakList 是否启用 top-10k 弱密 bloom 校验;本地调试可临时关
//	              (用 *bool 区分"未设"和"显式 false":nil=走默认 true)
type PasswordPolicyConfig struct {
	MinLen        int   `yaml:"min_len"`
	CheckWeakList *bool `yaml:"check_weak_list"`
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
	if v := os.Getenv("AZURE_LLM_API_KEY"); v != "" {
		cfg.LLM.Azure.APIKey = v
	}
	if v := os.Getenv("EMAIL_PROVIDER"); v != "" {
		cfg.Email.Provider = v
	}
	if v := os.Getenv("EMAIL_SMTP_HOST"); v != "" {
		cfg.Email.SMTPHost = v
	}
	if v := os.Getenv("EMAIL_SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Email.SMTPPort = p
		}
	}
	if v := os.Getenv("EMAIL_USERNAME"); v != "" {
		cfg.Email.Username = v
	}
	if v := os.Getenv("EMAIL_PASSWORD"); v != "" {
		cfg.Email.Password = v
	}
	if v := os.Getenv("EMAIL_FROM"); v != "" {
		cfg.Email.From = v
	}
	if v := os.Getenv("EMAIL_FROM_NAME"); v != "" {
		cfg.Email.FromName = v
	}
	if v := os.Getenv("EMAIL_API_KEY"); v != "" {
		cfg.Email.APIKey = v
	}
	if v := os.Getenv("EMAIL_DAILY_VERIFICATION_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Email.DailyVerificationLimit = n
		}
	}
	if v := os.Getenv("EMAIL_LOCALE"); v != "" {
		cfg.Email.Locale = v
	}

	// ── OAuth login(M1.6)──
	if v := os.Getenv("GOOGLE_OAUTH_CLIENT_ID"); v != "" {
		cfg.OAuthLogin.Google.ClientID = v
	}
	if v := os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"); v != "" {
		cfg.OAuthLogin.Google.ClientSecret = v
	}
	if v := os.Getenv("OAUTH_LOGIN_STATE_COOKIE_SECRET"); v != "" {
		cfg.OAuthLogin.StateCookieSecret = v
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
	if cfg.EventBus.MaxLen == 0 {
		// 10 万条:单条事件 KB 级时占 Redis 约 100MB,够近 1 小时高吞吐 + 冷启动 PEL 重放回看。
		cfg.EventBus.MaxLen = 100000
	}
	if cfg.EventBus.AsyncJobStream == "" {
		cfg.EventBus.AsyncJobStream = "synapse:asyncjob:events"
	}
	if cfg.EventBus.WorkflowStream == "" {
		cfg.EventBus.WorkflowStream = "synapse:workflow:events"
	}
	if cfg.EventBus.ChannelStream == "" {
		cfg.EventBus.ChannelStream = "synapse:channel:events"
	}
	if cfg.EventBus.TaskStream == "" {
		cfg.EventBus.TaskStream = "synapse:task:events"
	}
	if cfg.OAuth.AccessTokenTTL == 0 {
		cfg.OAuth.AccessTokenTTL = 24 * time.Hour
	}
	if cfg.OAuth.RefreshTokenTTL == 0 {
		cfg.OAuth.RefreshTokenTTL = 30 * 24 * time.Hour
	}
	if cfg.OAuth.AuthorizationCodeTTL == 0 {
		cfg.OAuth.AuthorizationCodeTTL = 10 * time.Minute
	}
	if cfg.OAuth.DCRRateLimitWindow == 0 {
		cfg.OAuth.DCRRateLimitWindow = 60
	}
	if cfg.OAuth.DCRRateLimitMax == 0 {
		cfg.OAuth.DCRRateLimitMax = 10
	}
	// AgentSys.Concurrency:0/负数 → 3(默认);>32 夹到 32 避免 LLM rate limit 打爆
	if cfg.AgentSys.Concurrency <= 0 {
		cfg.AgentSys.Concurrency = 3
	}
	if cfg.AgentSys.Concurrency > 32 {
		cfg.AgentSys.Concurrency = 32
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
	if cfg.JWT.AbsoluteSessionTTL <= 0 {
		cfg.JWT.AbsoluteSessionTTL = 30 * 24 * time.Hour
	}
	if cfg.JWT.MaxSessionsPerUser == 0 {
		cfg.JWT.MaxSessionsPerUser = 5
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

	// LLM(PR #6' 起):provider 留空时 factory 会 fatal,这里不替它填默认,
	// 强制用户显式写 "azure" 表达意图。Azure API 版本和 timeout 给兜底值。
	if cfg.LLM.Azure.APIVersion == "" {
		cfg.LLM.Azure.APIVersion = "2024-10-21"
	}
	if cfg.LLM.RequestTimeoutSec == 0 {
		cfg.LLM.RequestTimeoutSec = 60
	}

	// Email 默认值:provider 为空则整个模块 no-op(码只写 Redis,靠日志发给 dev)。
	// 其余给行业常规默认,方便生产侧只注入敏感字段。
	if cfg.Email.DailyVerificationLimit == 0 {
		cfg.Email.DailyVerificationLimit = 10
	}
	if cfg.Email.CodeTTL == "" {
		cfg.Email.CodeTTL = "10m"
	}
	if cfg.Email.MaxAttempts == 0 {
		cfg.Email.MaxAttempts = 5
	}
	if cfg.Email.Locale == "" {
		cfg.Email.Locale = "zh"
	}
	if cfg.Email.SMTPPort == 0 {
		cfg.Email.SMTPPort = 465
	}
	if cfg.Email.PasswordResetTTL == "" {
		cfg.Email.PasswordResetTTL = "15m"
	}
	if cfg.Email.VerificationTTL == "" {
		cfg.Email.VerificationTTL = "24h"
	}
	// VerificationLinkBase 缺省回退到 PasswordResetLinkBase,让本地 dev 只配一个 FE host 就能跑。
	if cfg.Email.VerificationLinkBase == "" {
		cfg.Email.VerificationLinkBase = cfg.Email.PasswordResetLinkBase
	}
	// User 默认值:per-email 登录失败 10 次锁 15min;per-IP 注册 5 次/60s 滑动窗口。
	if cfg.User.LoginFail.Max == 0 {
		cfg.User.LoginFail.Max = 10
	}
	if cfg.User.LoginFail.LockTTL == "" {
		cfg.User.LoginFail.LockTTL = "15m"
	}
	if cfg.User.RegisterRate.Max == 0 {
		cfg.User.RegisterRate.Max = 5
	}
	if cfg.User.RegisterRate.WindowSec == 0 {
		cfg.User.RegisterRate.WindowSec = 60
	}
	// M1.5 密码策略默认:最短 10 位 + 开启弱密名单。
	if cfg.User.Password.MinLen <= 0 {
		cfg.User.Password.MinLen = 10
	}
	// M1.7 Pending_verify 过期清理默认 7 天。
	if cfg.User.PendingVerifyExpireDays <= 0 {
		cfg.User.PendingVerifyExpireDays = 7
	}
	if cfg.User.Password.CheckWeakList == nil {
		t := true
		cfg.User.Password.CheckWeakList = &t
	}
	// M1.6 OAuth login:状态 cookie 密钥缺省给个固定 dev 值,生产必须覆盖。
	// StartupSanityCheck(见 main.go)会在 Google.Enabled=true 且此处仍为默认时 fatal。
	if cfg.OAuthLogin.StateCookieSecret == "" {
		cfg.OAuthLogin.StateCookieSecret = "synapse-dev-oauth-login-state-change-me"
	}

	// OSS:PathPrefix 缺省 "synapse" 供同 bucket 多服务共用;
	// MaxVersionsPerDocument 缺省 10(够用,避免 OSS 空间无节制膨胀)。
	if cfg.OSS.PathPrefix == "" {
		cfg.OSS.PathPrefix = "synapse"
	}
	if cfg.OSS.MaxVersionsPerDocument <= 0 {
		cfg.OSS.MaxVersionsPerDocument = 10
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
