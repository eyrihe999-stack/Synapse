// Package database 提供 MySQL / Postgres / Redis 的连接封装。
//
// ═════════════════════════════════════════════════════════════════════════════
//  Synapse Redis Key Registry
//  (项目所有 Redis key 的唯一登记处 —— 新增/改名前必须先更新这里)
// ═════════════════════════════════════════════════════════════════════════════
//
// 命名约定:
//   - 统一前缀 "synapse:",隔离共用同一 Redis 实例的其他业务
//   - 第二段 {scope} 表示用途域 (session / rl / ...)
//   - **所有 key 必须设 TTL** —— 没 TTL 意味着 key 永久累积,禁止
//   - 新增 key 前先把它登记到下表,再写实现
//
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ Key 格式                                │ 类型   │ TTL   │ 写入/读取/删除                         │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:session:{user_id}:{device_id}   │ string │  7d   │ internal/user/service/session_store.go │
//   │   value: JSON(user.SessionInfo)         │ (JSON) │(配置) │   Save/Get/Del/DeleteAll               │
//   │   用途: Web 用户 session                │        │       │ internal/common/middleware/auth.go            │
//   │        踢设备 = DEL                     │        │       │   JWTAuthWithSession 读取校验         │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:email_code:{email}              │ string │ 10m   │ internal/user/service/code_store.go    │
//   │   value: JSON {code,ip,created_at}      │ (JSON) │(配置) │   Store/Get/Delete                     │
//   │   用途: 邮箱验证码,单码原则             │        │       │ 第二次 SET 直接覆盖旧码,实现"一邮一码"│
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:email_rl:{email}:{YYYY-MM-DD}   │ string │ 24h   │ internal/user/service/code_store.go    │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   IncrDailyCount (IncrAndExpireIfNew) │
//   │   用途: 单邮箱每日发送上限计数器        │        │       │ UTC 过天 key 名自动切换,老 key 自毁   │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:email_attempt:{email}           │ string │ 10m   │ internal/user/service/code_store.go    │
//   │   value: 整数(INCR 计数)                │ (int)  │(配置) │   IncrAttempt/DeleteAttempt            │
//   │   用途: 验错次数,触顶时码作废(防爆破)  │        │       │ TTL 与 email_code 同步,一起进一起出    │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:pwd_reset:{token}               │ string │ 15m   │ internal/user/service/pwd_reset_store  │
//   │   value: JSON {email,created_at}        │ (JSON) │(配置) │   Store/Get/Delete                     │
//   │   用途: 密码重置一次性 token            │        │       │ Confirm 成功即 Delete + LogoutAll      │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:email_verify:{token}            │ string │ 24h   │ internal/user/service/email_verify_store│
//   │   value: JSON {user_id,email,created_at}│ (JSON) │(配置) │   Store/Get/Delete                     │
//   │   用途: 邮箱激活一次性 token (M1.1)     │        │       │ Verify 成功即 Delete;OAuth 未验邮箱触发│
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:login_fail:{email}              │ string │ 15m   │ internal/user/service/login_guard.go   │
//   │   value: 整数(INCR 计数)                │ (int)  │(配置) │   IncrLoginFail/ResetLoginFail         │
//   │   用途: 连续登录失败锁账号(防爆破)      │        │       │ Login 成功清零,超阈值返 ErrAccountLocked│
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:register_ip:{ip}                │ zset   │ 60s   │ internal/user/service/login_guard.go   │
//   │   member: 时间戳(ns,字符串)             │ (ZSET) │(配置) │   SlidingWindowAdd (Lua 原子)         │
//   │   用途: per-IP 注册滑动窗口限流         │        │       │ 超阈值拒 /register,防注册轰炸         │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:login_fail_rec:{email}:{date}   │ string │ 24h   │ internal/user/service/login_audit.go   │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter (IncrAndExpireIfNew)   │
//   │   用途: per-email 每日失败事件写入配额  │        │       │ 超阈值 skip 写 login_events,防表撑爆 │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:resend_verify_cooldown:{uid}    │ string │ 60s   │ internal/user/service/email_verification│
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-user 重发激活邮件 cooldown  │        │       │ 60s 内第二次调 resend 直接拒          │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:new_device_mail:{uid}:{date}    │ string │ 24h   │ internal/user/service/login_audit.go   │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-user 新设备通知邮件日限     │        │       │ 超 dailyNewDeviceMailCap 跳过邮件     │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:send_code_cd:{email}            │ string │ 60s   │ internal/user/service/email_code.go    │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-email 发码 cooldown         │        │       │ 60s 内第二次调 send-code 直接拒       │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:pwd_reset_cd:{email}            │ string │ 60s   │ internal/user/service/password_reset   │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-email 密码重置 cooldown     │        │       │ 60s 内重复调 reset-request 直接拒     │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:change_email_cd:{user_id}       │ string │ 60s   │ internal/user/service/account_security │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-user 改邮箱 cooldown        │        │       │ 防 session 被盗后循环触发改邮箱       │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:email_changed_notice:{old}:{d}  │ string │ 24h   │ internal/user/service/account_security │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter                         │
//   │   用途: per-old-email 每日邮箱变更通知  │        │       │ 超 emailChangedNoticeDailyCap 跳过告警│
//   │        告警日限(第二道防护)              │        │       │                                        │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:login_ip_fail:{ip}              │ string │ 15min │ internal/user/service/login_audit.go   │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │   TouchCounter + GetCounter           │
//   │   用途: per-IP 登录失败限流 (P3)        │        │       │ 超 loginIPFailMax 该 IP 临时锁 15min │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:inv_resend_cd:{invitation_id}   │ string │ 60s   │ internal/organization/service/         │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │     invite_guard.go (TouchCounter)    │
//   │   用途: 单条邀请 Resend cooldown        │        │       │ 60s 内第二次 Resend 直接拒            │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:inv_send_email:{email}:{date}   │ string │ 24h   │ internal/organization/service/         │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │     invite_guard.go (TouchCounter)    │
//   │   用途: per-target-email 每日收邀上限   │        │       │ 跨 org/inviter 合计,防受害者被轰炸   │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:inv_send_inviter:{uid}:{date}   │ string │ 24h   │ internal/organization/service/         │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │     invite_guard.go (TouchCounter)    │
//   │   用途: per-inviter 每日发邀上限        │        │       │ 防单号被盗后大规模 spray              │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//   │ synapse:inv_send_org:{org_id}:{date}    │ string │ 24h   │ internal/organization/service/         │
//   │   value: 整数(INCR 计数)                │ (int)  │(固定) │     invite_guard.go (TouchCounter)    │
//   │   用途: per-org 每日发邀上限(兜底)      │        │       │ 防整个 org 被滥用成邀请邮件喷射源    │
//   +─────────────────────────────────────────+────────+───────+────────────────────────────────────────+
//
// ═════════════════════════════════════════════════════════════════════════════
//  Streams 登记(事件总线,PR #3 起)
//  (和上面 KV 表结构不同:Stream 没 TTL,有 MAXLEN;value 是字段集不是单值)
//  实现 / 配置:internal/common/eventbus + config.EventBus(*_stream / max_len)
// ═════════════════════════════════════════════════════════════════════════════
//
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//   │ Stream Key                              │ MAXLEN  │ 发布者 / 消费 group     │ 字段集                     │
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//   │ synapse:asyncjob:events                 │ ~100000 │ 发布:internal/asyncjob/ │ job_id / org_id / user_id   │
//   │   用途: job 进终态(succeeded/failed/    │ (可配)  │       service           │ / kind / status             │
//   │        canceled)时广播完成事件,给      │         │ 消费:workflow-advancer │ / idempotency_key / error   │
//   │        workflow 引擎推进 step           │         │       (PR #5 落)        │ / result (JSON string)     │
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//   │ synapse:workflow:events                 │ ~100000 │ 发布:workflow 引擎      │ event_type / workflow_id    │
//   │   用途: workflow 内部跃迁(step.ready/   │ (可配)  │       (PR #5 落)        │ / step_id                   │
//   │        step.completed / approval...),  │         │ 消费:workflow-advancer │ / payload (JSON string)    │
//   │        驱动 advancer 推进下游 step      │         │                         │                             │
//   │        PR #3 只占位,PR #5 正式启用       │         │                         │                             │
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//   │ synapse:channel:events                  │ ~100000 │ 发布:channel/service    │ event_type / org_id         │
//   │   用途: channel 层业务事件              │ (可配)  │ 消费:                   │ / channel_id / message_id   │
//   │        message.posted / channel.created │         │  - channel-event-card-  │ / author_principal_id       │
//   │        channel.archived / member.added/ │         │    writer (PR #4'起)    │ / mentioned_principal_ids   │
//   │        removed / kb_ref.* / project.*   │         │  - top-orchestrator     │   (csv)                     │
//   │                                         │         │    (PR #6')             │                             │
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//   │ synapse:task:events                     │ ~100000 │ 发布:task/service       │ event_type / org_id         │
//   │   用途: task 状态变化                   │ (可配)  │ 消费:                   │ / channel_id / task_id      │
//   │        task.created / claimed /         │         │  - channel-event-card-  │ / status / assignee_*       │
//   │        submitted / reviewed / ...       │         │    writer (PR #4'起)    │ / reviewer_* / ...          │
//   +─────────────────────────────────────────+─────────+─────────────────────────+─────────────────────────────+
//
// Streams 使用纪律:
//   - 嵌套结构(如 result / payload)由发布侧 JSON marshal 成 string 塞进字段,
//     消费侧自行 unmarshal;eventbus 层只做 k/v string 传输
//   - 幂等由消费端业务保障(UPDATE ... WHERE status=running 返 RowsAffected=0 视为已处理)
//   - at-least-once 投递:同一事件可能被投递多次,handler 必须幂等
//   - DB 是真相源;publish 失败不回滚 DB,由 reaper 扫状态差兜底对账
//
// 实现注记:
//   - session:
//     * 7d TTL = refresh token 有效期,每次 Save 重置为完整 7d
//     * 主键 (user_id, device_id);5 设备上限由 service 层 List 计数执行
//     * 无反向索引,列某 user 所有设备走 SCAN MATCH synapse:session:{uid}:* COUNT 100
//   - 计数类 key (login_fail / email_rl / *_cd / inv_send_* 等):
//     * 统一走 IncrAndExpireIfNew (Lua 脚本) 保证 INCR + EXPIRE 原子,避免 key 永不过期
//
// 不落 Redis 的相关数据(避免误以为在这里):
//   - IP 限流 (middleware/ip_rate_limit.go) —— 内存 sync.Map
//   - OAuth auth code / refresh token —— MySQL 表
//   - asyncjob 状态 —— MySQL 表(async_jobs.status 是唯一真相;Streams 只是完成事件的通知通道)
//   - workflow 审计日志 —— MySQL workflow_events 表(和 Redis Streams 职责分离,表管"记录",Streams 管"通知")
//   - 各模块 session / chunk 元数据 —— MySQL/PG
//
// ═════════════════════════════════════════════════════════════════════════════
package database

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/go-redis/redis/v8"
)

// RedisInterface defines the Redis contract
type RedisInterface interface {
	NoSQLDatabaseInterface
	GetClient() *redis.Client
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) (bool, error)
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
	Exists(ctx context.Context, keys ...string) (int64, error)
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, expiration time.Duration) error
	// IncrAndExpireIfNew 原子递增 key;首次创建(返回 1)时设置 TTL。
	// 用 Lua 脚本保证 INCR + EXPIRE 原子,避免 Expire 丢失导致 key 永不过期。
	IncrAndExpireIfNew(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// SlidingWindowAdd 向 key(ZSET)滑动窗口计数器 +1 并返回当前窗口内事件数。
	// 原子执行:清理窗口外成员(ZREMRANGEBYSCORE) → ZADD 当前事件 → ZCARD → 续租 EXPIRE。
	// now 是调用端的绝对时间戳(纳秒),window 为窗口长度。member 用 now+随机串避免同一纳秒冲突。
	// 返回的 count 已经包含刚 ADD 进去的本次事件;上层根据 count > max 判拒。
	SlidingWindowAdd(ctx context.Context, key string, now time.Time, window time.Duration) (int64, error)
	PingWithContext(ctx context.Context) error
}

// incrExpireScript 原子 INCR+EXPIRE(仅首次创建时设 TTL)。
var incrExpireScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return current
`)

// slidingWindowScript 真·滑动窗口:先剔除窗口外成员,再 ZADD 当前事件,再 ZCARD,续租 EXPIRE。
// KEYS[1] = ZSET key;ARGV[1] = now(纳秒);ARGV[2] = window(纳秒);
// ARGV[3] = member(同一纳秒多次并发时用调用端生成的唯一串避免覆盖)
// ARGV[4] = ttl(秒)
var slidingWindowScript = redis.NewScript(`
local cutoff = tonumber(ARGV[1]) - tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', cutoff)
redis.call('ZADD', KEYS[1], tonumber(ARGV[1]), ARGV[3])
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[4]))
return redis.call('ZCARD', KEYS[1])
`)

// RedisDatabase implements Redis database connection
type RedisDatabase struct {
	client *redis.Client
	config *config.RedisConfig
}

// NewRedis creates a new Redis database connection
//sayso-lint:ignore godoc-error-undoc
func NewRedis(cfg *config.RedisConfig) (RedisInterface, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	// 超时与池大小走 cfg,避免 Redis 抖动拖死整条 chat 链路(限流、session 校验都过 Redis)。
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolTimeout:  cfg.PoolTimeout,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisDatabase{
		client: client,
		config: cfg,
	}, nil
}

func (r *RedisDatabase) GetClient() *redis.Client { return r.client }

func (r *RedisDatabase) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return r.client.Set(ctx, key, value, expiration).Err()
}

func (r *RedisDatabase) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) (bool, error) {
	return r.client.SetNX(ctx, key, value, expiration).Result()
}

func (r *RedisDatabase) Get(ctx context.Context, key string) (string, error) {
	result := r.client.Get(ctx, key)
	if result.Err() == redis.Nil {
		return "", fmt.Errorf("key does not exist")
	}
	return result.Result()
}

func (r *RedisDatabase) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

func (r *RedisDatabase) Exists(ctx context.Context, keys ...string) (int64, error) {
	return r.client.Exists(ctx, keys...).Result()
}

func (r *RedisDatabase) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}

func (r *RedisDatabase) Expire(ctx context.Context, key string, expiration time.Duration) error {
	return r.client.Expire(ctx, key, expiration).Err()
}

func (r *RedisDatabase) IncrAndExpireIfNew(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	result, err := incrExpireScript.Run(ctx, r.client, []string{key}, int64(ttl.Seconds())).Result()
	if err != nil {
		return 0, err
	}
	n, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected script result type %T", result)
	}
	return n, nil
}

func (r *RedisDatabase) SlidingWindowAdd(ctx context.Context, key string, now time.Time, window time.Duration) (int64, error) {
	nowNs := now.UnixNano()
	// member 带纳秒 + 随机后缀,避免同一纳秒多线程 ZADD 相同 member 导致去重
	member := fmt.Sprintf("%d-%d", nowNs, randUint32())
	ttl := int64(window.Seconds())
	if ttl < 1 {
		ttl = 1
	}
	result, err := slidingWindowScript.Run(ctx, r.client,
		[]string{key},
		nowNs, int64(window), member, ttl,
	).Result()
	if err != nil {
		return 0, err
	}
	n, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected script result type %T", result)
	}
	return n, nil
}

func (r *RedisDatabase) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.client.Ping(ctx).Err()
}

func (r *RedisDatabase) PingWithContext(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisDatabase) Close() error {
	return r.client.Close()
}

// randUint32 生成一个加密安全的 uint32,用于滑动窗口 ZSET member 去重。
// 同一纳秒内并发 ZADD 相同 score+member 会被 Redis 视为同一元素,需加随机后缀避免。
func randUint32() uint32 {
	var b [4]byte
	//sayso-lint:ignore err-swallow
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
