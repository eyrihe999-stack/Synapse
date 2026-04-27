package service

import "context"

// UserAuthenticator 抽象"按 email+password 验 user 凭证"能力,由 main.go 注入实现。
// 之所以走接口:oauth 模块不直接依赖 user / jwt 模块,避免循环依赖并保证测试友好。
//
// 返回:
//   - userID != 0:认证成功
//   - error:密码错、账号锁、user 不存在等都返回同一个 ErrInvalidCredentials,
//     真实原因走 log(防枚举)。
type UserAuthenticator interface {
	AuthenticateByPassword(ctx context.Context, email, password, ip, userAgent string) (userID uint64, err error)
}

// AgentBootstrapper 抽象"OAuth 授权成功时为该 user 自动建一个个人 agent"能力。
// consent handler 调用此接口;具体实现由 main.go 注入(包装 agents.Service 或直接查 DB)。
//
// 返回:
//   - agentID:agents.id
//   - principalID:agents.principal_id(OAuth access_token 的 agent_id 字段落这个)
//   - error:建失败
//
// 幂等:若同 (owner_user_id, display_name) 已存在可复用(具体策略由实现决定,
// 当前默认"每次 consent 新建一个"—— 每次重新授权对应一个独立 agent,便于吊销)。
type AgentBootstrapper interface {
	CreateUserAgent(ctx context.Context, ownerUserID uint64, displayName string) (agentID, principalID uint64, err error)
}

// OAuthSessionStore 轻量 key-value,存"OAuth flow 期间的登录状态"。
// 用 Redis 实现;生命周期短(分钟级),仅用于 login → consent 之间保持登录。
//
// Cookie 是随机 sessionID,Redis 里 sessionID → userID。
// 独立于 web JWT session —— OAuth flow 不污染前端 JWT 生命周期。
type OAuthSessionStore interface {
	Create(ctx context.Context, userID uint64) (sessionID string, err error)
	Resolve(ctx context.Context, sessionID string) (userID uint64, err error)
	Delete(ctx context.Context, sessionID string) error
}
