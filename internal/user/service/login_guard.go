// login_guard.go 登录失败锁 + 注册 per-IP 滑动窗口限流 + 密码重置 store 接口。
//
// PasswordResetStore 的接口声明也放在这里(和 LoginGuard 同属"账号安全"范畴),实现在 pwd_reset_store.go。
//
// 两条防护:
//
//   per-email 登录失败锁:
//     Login 路径在密码校验 / 验证码校验失败时 Incr 计数,Incr ≥ max 即拒登,
//     返回 ErrAccountLocked;成功登录时 Reset 清零。TTL 保证到期自解锁。
//     Key: synapse:login_fail:{email}
//
//   per-IP 注册滑动窗口:
//     Register 入口调用 CheckRegisterIP,SlidingWindowAdd 已经把本次事件
//     计入窗口并返回总数;count > max 则拒,返回 ErrRegisterRateExceeded。
//     Key: synapse:register_ip:{ip}  (ZSET,nano-ts 作 score)
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

const (
	loginFailKeyPrefix   = "synapse:login_fail"
	registerIPKeyPrefix  = "synapse:register_ip"
)

// LoginGuard 登录 / 注册的反爆破抽象,便于 service 层测试时注入 fake。
type LoginGuard interface {
	// IncrLoginFail 递增 email 的连续失败计数,返回递增后的当前值。
	// 首次创建时设置 lockTTL;超阈值由上层判断。
	IncrLoginFail(ctx context.Context, email string, lockTTL time.Duration) (int64, error)
	// ResetLoginFail 登录成功后调用,清零计数。
	ResetLoginFail(ctx context.Context, email string) error
	// GetLoginFail 查询当前累计失败次数,不存在返 0。
	GetLoginFail(ctx context.Context, email string) (int64, error)

	// AddRegisterHit 向 per-IP 滑动窗口 +1,返回窗口内当前事件数(含本次)。
	// 上层根据 count > max 判断是否拒绝本次注册。
	AddRegisterHit(ctx context.Context, ip string, window time.Duration) (int64, error)

	// TouchCounter 通用 INCR + EXPIRE(若 key 首次创建才设 TTL),返回递增后的当前值。
	// 用途:per-email 失败事件写入配额、per-user cooldown 等"到阈值就 skip"场景,
	// 调用方根据返值判断是否允许后续动作。
	//
	// 约定:key 由调用方拼 "synapse:xxx:..." 并登记到 internal/common/database/redis.go 的 Key Registry。
	TouchCounter(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// GetCounter 只读:读取 key 当前计数值;不存在返 (0, nil),格式解析失败也返 (0, nil) 保守放行。
	// 用于"到阈值直接拒绝,不递增"的场景(例如 Login 入口先读 per-IP 失败计数判断是否已锁)。
	GetCounter(ctx context.Context, key string) (int64, error)
}

type redisLoginGuard struct {
	redis database.RedisInterface
	log   logger.LoggerInterface
}

// NewLoginGuard 基于 Redis 的 LoginGuard 实现。
func NewLoginGuard(rdb database.RedisInterface, log logger.LoggerInterface) LoginGuard {
	return &redisLoginGuard{redis: rdb, log: log}
}

func loginFailKey(email string) string  { return fmt.Sprintf("%s:%s", loginFailKeyPrefix, email) }
func registerIPKey(ip string) string    { return fmt.Sprintf("%s:%s", registerIPKeyPrefix, ip) }

// IncrLoginFail +1 当前 email 的失败次数,返回新值。
// 底层走 IncrAndExpireIfNew —— 首次失败时才设 lockTTL,后续失败不重置计时,
// 窗口到期自动解锁。
func (g *redisLoginGuard) IncrLoginFail(ctx context.Context, email string, lockTTL time.Duration) (int64, error) {
	n, err := g.redis.IncrAndExpireIfNew(ctx, loginFailKey(email), lockTTL)
	if err != nil {
		g.log.ErrorCtx(ctx, "递增登录失败计数失败", err, map[string]interface{}{"email": email})
		return 0, fmt.Errorf("incr login fail: %w", err)
	}
	return n, nil
}

// ResetLoginFail 清零。成功登录后调用,防止历史失败次数把用户锁住。
func (g *redisLoginGuard) ResetLoginFail(ctx context.Context, email string) error {
	if err := g.redis.Del(ctx, loginFailKey(email)); err != nil {
		g.log.ErrorCtx(ctx, "清零登录失败计数失败", err, map[string]interface{}{"email": email})
		return fmt.Errorf("reset login fail: %w", err)
	}
	return nil
}

// GetLoginFail 读取当前累计失败次数。key 不存在时返 (0, nil),不视作错误。
func (g *redisLoginGuard) GetLoginFail(ctx context.Context, email string) (int64, error) {
	val, err := g.redis.Get(ctx, loginFailKey(email))
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return 0, nil // miss = 0 次,正常
	}
	var n int64
	//sayso-lint:ignore err-swallow
	if _, err := fmt.Sscanf(val, "%d", &n); err != nil { // 解析失败走 log + 视作 0(保守放行,不因格式问题锁住用户)
		g.log.WarnCtx(ctx, "解析登录失败计数失败", map[string]interface{}{"email": email, "val": val})
		return 0, nil
	}
	return n, nil
}

// AddRegisterHit 调用底层 SlidingWindowAdd,ZSET 自己去重并清理窗口外数据。
// Redis 脚本执行失败(连接断开/脚本异常)会打 ErrorCtx 并返回包装后的 error。
func (g *redisLoginGuard) AddRegisterHit(ctx context.Context, ip string, window time.Duration) (int64, error) {
	n, err := g.redis.SlidingWindowAdd(ctx, registerIPKey(ip), time.Now().UTC(), window)
	if err != nil {
		g.log.ErrorCtx(ctx, "注册滑动窗口 +1 失败", err, map[string]interface{}{"ip": ip})
		return 0, fmt.Errorf("sliding window add: %w", err)
	}
	return n, nil
}

// TouchCounter 通用 INCR + EXPIRE IfNew。key 必须由调用方预拼好(含 synapse: 前缀),
// 返回递增后的当前值。调用方按业务阈值(第 N 次 = skip)做决策。
//
// Redis 故障时走 ErrorCtx log + 返 (0, err);调用方通常 fail-open(允许后续动作),
// 避免 Redis 挂导致整条主流程跟着挂。
func (g *redisLoginGuard) TouchCounter(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := g.redis.IncrAndExpireIfNew(ctx, key, ttl)
	if err != nil {
		g.log.ErrorCtx(ctx, "TouchCounter 失败", err, map[string]interface{}{"key": key})
		return 0, fmt.Errorf("touch counter: %w", err)
	}
	return n, nil
}

// GetCounter 只读查询。key 不存在返 (0, nil);解析失败也返 (0, nil) 保守放行,
// 不因 Redis 里有脏数据阻断业务。
func (g *redisLoginGuard) GetCounter(ctx context.Context, key string) (int64, error) {
	val, err := g.redis.Get(ctx, key)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap,log-coverage
		return 0, nil // miss = 0
	}
	var n int64
	//sayso-lint:ignore err-swallow
	if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
		g.log.WarnCtx(ctx, "GetCounter 解析失败", map[string]interface{}{"key": key, "val": val})
		return 0, nil
	}
	return n, nil
}

// ─── 密码重置 token 存取 ────────────────────────────────────────────────────

// PasswordResetEntry 密码重置 token 的 Redis 条目。token 本身作为 Redis key,此处只记 email 和时间。
type PasswordResetEntry struct {
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// PasswordResetStore 密码重置 token 的存取抽象。具体实现见 pwd_reset_store.go。
// token 作 key 的好处:同一邮箱多次发请求不会互相覆盖,每个 token 都有独立生命周期。
type PasswordResetStore interface {
	Store(ctx context.Context, token string, entry PasswordResetEntry, ttl time.Duration) error
	Get(ctx context.Context, token string) (*PasswordResetEntry, error)
	Delete(ctx context.Context, token string) error
}

// ─── 邮箱激活 token 存取 (M1.1) ─────────────────────────────────────────────

// EmailVerifyEntry 邮箱激活 token 的 Redis 条目。
// UserID 冗余记录,避免激活时再查 email → user 中间可能 email 已 pseudo 化;
// Email 也存下来做日志和幂等校验。
type EmailVerifyEntry struct {
	UserID    uint64    `json:"user_id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// EmailVerifyStore 邮箱激活 token 的存取抽象。实现见 email_verify_store.go。
// 与 PasswordResetStore 共享设计:token 作 Redis key,一次性消费 + TTL 兜底。
type EmailVerifyStore interface {
	Store(ctx context.Context, token string, entry EmailVerifyEntry, ttl time.Duration) error
	Get(ctx context.Context, token string) (*EmailVerifyEntry, error)
	Delete(ctx context.Context, token string) error
}
