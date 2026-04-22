package middleware

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
)

// RequestID 给每个请求生成唯一 ID,写入 X-Request-ID 响应头 + gin 上下文 + request.Context
// 值。logger 的 *Ctx 方法会自动把它当作 trace_id 注入到每条日志,实现"同一请求的所有日志
// 能靠 trace_id 串起来",和 sayso-server 的 SLS 查询习惯保持一致。
//
// 客户端传了 X-Request-ID 就用客户端的,没传现生成 "<unixnano>-<rand>"。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}

		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)

		ctx := logger.WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

func generateRequestID() string {
	//sayso-lint:ignore time-now-utc
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(1000))
}
