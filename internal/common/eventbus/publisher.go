package eventbus

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
)

// NewRedisPublisher 构造基于 Redis Streams 的 Publisher。
//
// client 复用 common/database.RedisDatabase.GetClient() 返回的实例;本包不负责连接 /
// 关闭 Redis。maxLen 走 XADD MAXLEN ~ 近似裁剪(O(1),保留约 maxLen 条),<=0 视为不裁剪
// —— 生产环境务必设置,避免 stream 无限增长。
func NewRedisPublisher(client *redis.Client, maxLen int64) Publisher {
	return &redisPublisher{client: client, maxLen: maxLen}
}

type redisPublisher struct {
	client *redis.Client
	maxLen int64
}

func (p *redisPublisher) Publish(ctx context.Context, stream string, fields map[string]any) (string, error) {
	if stream == "" {
		return "", fmt.Errorf("eventbus publish: stream is required")
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("eventbus publish: fields are required")
	}
	args := &redis.XAddArgs{
		Stream: stream,
		Values: fields,
	}
	if p.maxLen > 0 {
		// Approx=true 触发 XADD MAXLEN ~(O(1) 近似裁剪),
		// 而不是 MAXLEN(精确裁剪,会全表扫)。
		args.MaxLen = p.maxLen
		args.Approx = true
	}
	id, err := p.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("eventbus publish xadd: %w", err)
	}
	return id, nil
}
