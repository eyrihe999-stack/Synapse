package middleware

import (
	"sync"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/gin-gonic/gin"
)

// ipRateBucket 单 IP 固定窗口计数器,count 与 resetAt 在同一锁下读写。
type ipRateBucket struct {
	mu      sync.Mutex
	count   int
	resetAt int64 // unix seconds
}

// IPRateLimit 基于客户端 IP 的固定窗口限流中间件。
//
// 用途:在 /api/v1/auth/* 这类无鉴权入口前兜底,降低暴力破解 / 注册轰炸风险。
// 实现:in-memory,单实例有效。多实例部署如需全局限流,应换成 Redis 版。
//
// 后台 goroutine 周期性清理过期 bucket,daemon 性质,进程退出自然结束。
//
// 参数:
//   - max:每个窗口允许的请求数(超过返回 429)
//   - window:窗口长度
func IPRateLimit(max int, window time.Duration) gin.HandlerFunc {
	var buckets sync.Map // string(ip) -> *ipRateBucket
	windowSec := int64(window.Seconds())
	if windowSec <= 0 {
		windowSec = 1
	}

	// 清理间隔取窗口的 5 倍,至少 1 分钟,避免 ticker 太频繁。
	sweepInterval := window * 5
	if sweepInterval < time.Minute {
		sweepInterval = time.Minute
	}
	//sayso-lint:ignore bare-goroutine
	go func() {
		t := time.NewTicker(sweepInterval)
		defer t.Stop()
		for range t.C {
			//sayso-lint:ignore time-now-utc
			now := time.Now().Unix()
			buckets.Range(func(k, v any) bool {
				b, ok := v.(*ipRateBucket)
				if !ok {
					return true
				}
				b.mu.Lock()
				stale := b.resetAt > 0 && b.resetAt < now
				b.mu.Unlock()
				if stale {
					buckets.Delete(k)
				}
				return true
			})
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		//sayso-lint:ignore time-now-utc
		now := time.Now().Unix()
		v, _ := buckets.LoadOrStore(ip, &ipRateBucket{})
		//sayso-lint:ignore type-assert
		b := v.(*ipRateBucket)
		b.mu.Lock()
		if now >= b.resetAt {
			b.count = 0
			b.resetAt = now + windowSec
		}
		b.count++
		count := b.count
		b.mu.Unlock()
		if count > max {
			response.TooManyRequests(c, "Too many requests", "rate limit exceeded")
			c.Abort()
			return
		}
		c.Next()
	}
}
