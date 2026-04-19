// revoke.go /oauth/revoke  RFC 7009 token revocation。
//
// 语义:始终返 200(即便 token 不存在或格式错),避免泄漏 token 是否有效。
// 只有服务器自身故障才返 5xx。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Revoke POST /oauth/revoke。body: token=...&token_type_hint=refresh_token。
func (h *Handler) Revoke(c *gin.Context) {
	token := c.PostForm("token")
	hint := c.PostForm("token_type_hint")

	if err := h.svc.Revoke(token, hint); err != nil {
		// 内部错误才记;"token 不存在"不算错,service 内部就吞了。
		h.log.ErrorCtx(c.Request.Context(), "oauth: revoke failed", err, nil)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}

	// RFC 7009:成功 = 200,空 body。不返 token 任何信息。
	c.Status(http.StatusOK)
}
