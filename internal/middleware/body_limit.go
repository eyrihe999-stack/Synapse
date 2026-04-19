package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBodySize 返回一个中间件,限制请求体大小。
// 超过 limit 字节时返回 413 Request Entity Too Large。
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}
