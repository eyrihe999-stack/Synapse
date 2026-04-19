// register.go /oauth/register  Dynamic Client Registration (RFC 7591)。
//
// 无 auth — 任何客户端可以发起注册。安全靠 redirect_uri 前缀白名单 +(未来)rate limit。
// 注册响应的 client_id 本身对外暴露没关系:它只是让 authorize 流程能找回 metadata,
// 真正的安全由 PKCE + redirect_uri 绑定承担。
package handler

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// Register POST /oauth/register。body 是 RFC 7591 client metadata JSON。
func (h *Handler) Register(c *gin.Context) {
	// 读 body 全文留档 —— service 把原 JSON 写进 metadata 列,后续审计可看完整请求。
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeOAuthError(c, service.ErrInvalidRequest)
		return
	}

	var req service.ClientRegistrationReq
	if err := unmarshalJSON(raw, &req); err != nil {
		writeOAuthError(c, service.ErrInvalidClientMetadata)
		return
	}
	req.Metadata = raw

	// CreatedByUserID 目前不注入 —— DCR 通常在未登录状态发起(Claude Desktop 装好第一次连)。
	// 将来若想区分"已登录用户注册的 client",可以在此探测 web session 注入 UserID。

	resp, err := h.svc.RegisterClient(req)
	if err != nil {
		h.log.WarnCtx(c.Request.Context(), "oauth: register failed", map[string]any{"err": err.Error()})
		writeOAuthError(c, err)
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, resp)
}

// unmarshalJSON 独立函数为了方便未来替换成更严格的校验(e.g. disallow unknown fields)。
// 当前直接用 encoding/json 默认行为 —— RFC 7591 容许未来加字段,严格校验反而破坏兼容。
func unmarshalJSON(raw []byte, v any) error {
	return jsonUnmarshal(raw, v)
}
