package database

import (
	"context"
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

// RedisDatabase implements Redis database connection
type RedisDatabase struct {
	client *redis.Client
	config *config.RedisConfig
}

// NewRedis creates a new Redis database connection
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
