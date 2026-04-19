// Package model integration 模块的数据模型。
package model

import (
	"time"

	"gorm.io/datatypes"
)

// UserIntegration 单个 (user_id, provider) 对的 OAuth token 持久化记录。
//
// 安全注意:RefreshToken 是长期凭证(飞书 30 天、Google 无限期),应该在生产
// 环境用 KMS / envelope encryption 加密列。MVP 先明文存,T4(合规)再升级。
//
// 生命周期:
//   - 用户 OAuth 成功 → service.ExchangeCode 插入此行
//   - 后台 worker Sync 前调 service.GetValidAccessToken → 走 Adapter
//   - Adapter 内部 refresh access_token 时若 refresh_token 轮换,写回此行
//   - 用户撤销 / 管理员关停 → service.Delete 删此行(Adapter 下次调用拿不到 token 自然失败)
//
// 索引:
//   - (user_id, provider) 唯一:一个 user 对一个 provider 只能一条授权
//   - org_id 单列索引:给"列出本 org 所有已授权用户" 的管理端用
//   - provider 单列索引:sync worker / 管理端 按 provider 扫全表用
type UserIntegration struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	UserID   uint64 `gorm:"not null;uniqueIndex:uk_integration_user_provider,priority:1"`
	OrgID    uint64 `gorm:"not null;index:idx_integration_org"`
	Provider string `gorm:"size:32;not null;uniqueIndex:uk_integration_user_provider,priority:2;index:idx_integration_provider"`

	// RefreshToken 长期凭证。空串 = 授权失效(撤销 / 过期 / 未走过 OAuth)。
	// MySQL TEXT 列不支持 DEFAULT,空串由 Go 侧保证(Create 时显式填;zero value 入库即空串)。
	RefreshToken string `gorm:"type:text;not null"`
	// RefreshTokenExpiresAt nil = provider 的 refresh_token 永不过期(如 GitLab PAT / Google);否则是具体到期时间。
	// 用指针而非 time.Time 零值:MySQL 严格模式拒绝 '0000-00-00',零值必须落 NULL。
	RefreshTokenExpiresAt *time.Time

	// AccessToken 缓存层:短期(1-2 小时),避免每次调 API 都刷新。
	// 过期了由 Adapter 自己刷新并回写这两个字段(走 service.UpdateTokens)。
	AccessToken string `gorm:"type:text"`
	// AccessTokenExpiresAt nil = 不过期(如 GitLab PAT 复用此列存静态 token);否则是具体到期时间。
	AccessTokenExpiresAt *time.Time

	// LastSyncAt 上次 Sync 完成的时间点。下次 Adapter.Sync(since) 传它进去。
	// nil = 从未同步过,Sync 走全量(since = 零值 → Adapter 内部当 "拉所有")。
	LastSyncAt *time.Time

	// Metadata provider-specific 元信息。飞书场景:open_id、name、email。
	// jsonb,查询不常用所以不建 GIN;主要是 Sync / 审计时 log 里带上 人可读的身份。
	Metadata datatypes.JSON `gorm:"type:json"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名,和其他模块一致的扁平命名。
func (UserIntegration) TableName() string { return "user_integrations" }

// OrgFeishuConfig 每个 org 的飞书自建应用凭证。
//
// 和 UserIntegration 的正交关系:
//   - OrgFeishuConfig = 组织在飞书开放平台注册的"应用" → app_id / app_secret(org admin 填)
//   - UserIntegration = 用户授权该应用访问自己飞书账号后拿到的 token(用户走 OAuth 自动写)
//
// 所以 org 需要先配置 App 凭证,其成员才能走 OAuth 授权。
//
// 安全:app_secret 是长期凭证,MVP 明文存(与 UserIntegration.refresh_token 一致),T4 合规
// 阶段统一升级到 KMS / envelope encryption。
//
// 索引:
//   - org_id 唯一:一个 org 对飞书只配一套应用。将来有"多 App 共存"需求再拓展。
type OrgFeishuConfig struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID  uint64 `gorm:"not null;uniqueIndex:uk_org_feishu_configs_org"`

	// AppID / AppSecret 对应飞书开放平台"自建应用"的 App ID 和 App Secret。
	AppID     string `gorm:"size:64;not null"`
	AppSecret string `gorm:"type:text;not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名。
func (OrgFeishuConfig) TableName() string { return "org_feishu_configs" }

// OrgGitLabConfig 每个 org 连接的 GitLab 实例配置。
//
// 语义和 OrgFeishuConfig 对齐 —— base_url 不是部署级而是 per-org:同一 Synapse 实例可服务多个
// 使用不同 GitLab(SaaS / 企业自建 / 多个自建)的组织。每个 org admin 在前端填自己的实例地址。
//
// 和 UserIntegration 的正交关系:
//   - OrgGitLabConfig = 组织使用哪个 GitLab 实例(admin 填)
//   - UserIntegration(provider=gitlab) = 该组织成员在该实例上的 PAT(用户填)
//
// PAT 模式没有 App 凭证概念,所以这张表只存 BaseURL + 传输层开关,不存任何密钥。
//
// 索引:
//   - org_id 唯一:一个 org 对 GitLab 只配一套实例。多实例共存需求出现再拓展(加个 name 区分)。
type OrgGitLabConfig struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	OrgID uint64 `gorm:"not null;uniqueIndex:uk_org_gitlab_configs_org"`

	// BaseURL GitLab API 根地址,必须以 /api/v4 结尾(和 pkg/sourceadapter/gitlab Config.Validate 对齐)。
	// 例:https://gitlab.com/api/v4、https://ysgit.lunalabs.cn/api/v4
	BaseURL string `gorm:"size:255;not null"`

	// InsecureSkipVerify 仅内网自签证书场景;默认 false。
	InsecureSkipVerify bool `gorm:"not null;default:false"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名。
func (OrgGitLabConfig) TableName() string { return "org_gitlab_configs" }
