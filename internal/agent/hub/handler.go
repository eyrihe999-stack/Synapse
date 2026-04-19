// handler.go WS 入口:HTTP upgrade + 身份校验 + attach 到 hub。
//
// 路由:GET /api/v2/agents/hub?slug=<agent_slug>
//
// 认证:OAuth middleware 已在前级校验 access token,并把 claims(UserID / OrgID)注入 gin context。
// 本 handler 从 claims 里取 UserID,再对齐 agent.owner_user_id:
//   - agent 必须属于 token 持有人(防 A 的 token 连 B 的 agent)
//   - agent.status 必须 active
// 不检 publish —— publish 是"谁能用这个 agent",WS 连接是"agent 自己报告上线",作者权限即可。
package hub

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// LookupFunc 按 (ownerUID, slug) 解析 agent。
// 返 (agentID, status, err)。not-found 应返 (0, "", err);db 失败返非 nil err。
// 这种函数签名比 interface 更轻,main.go 直接包一行闭包注入即可。
type LookupFunc func(ctx context.Context, ownerUID uint64, slug string) (agentID uint64, status string, err error)

// WSHandler 处理 agent 侧发起的 WS upgrade。
type WSHandler struct {
	hub    *Hub
	lookup LookupFunc
	log    logger.LoggerInterface
}

// NewWSHandler 构造。
func NewWSHandler(h *Hub, lookup LookupFunc, log logger.LoggerInterface) *WSHandler {
	if h == nil || lookup == nil || log == nil {
		panic("hub handler: deps must be non-nil")
	}
	return &WSHandler{hub: h, lookup: lookup, log: log}
}

// Upgrade 处理一次 WS upgrade 请求。
func (w *WSHandler) Upgrade(c *gin.Context) {
	// 1. OAuth claims(middleware 注入)
	claims, ok := oauthmw.ClaimsFromContext(c)
	if !ok || claims == nil {
		// 理论上 middleware 已经挡了,这里防御性处理
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing oauth claims"})
		return
	}

	// 2. agent slug 从 query 取(WS 不便用 path 变量 —— 有些库会和 upgrade 冲突)
	slug := c.Query("slug")
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing ?slug="})
		return
	}

	// 3. 查 agent + ownership 校验
	agentID, status, err := w.lookup(c.Request.Context(), claims.Subject, slug)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("agent not found: %v", err)})
		return
	}
	if agentID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
		return
	}
	if status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "agent status not active"})
		return
	}

	// 4. Upgrade。OriginPatterns: "*" —— 我们自己做了 OAuth 认证,同源检查不是主要防线;
	// 生产想更严格可改成白名单域名。InsecureSkipVerify 不存在于 coder/websocket,这里用 OriginPatterns 放开。
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		// Accept 失败通常是 header 不对;不能再写 JSON(HTTP 已升级过半)
		w.log.WarnCtx(c.Request.Context(), "hub: ws upgrade failed", map[string]any{
			"agent_id": agentID, "err": err.Error(),
		})
		return
	}

	// 5. 挂到 hub,阻塞到断开
	w.hub.Attach(c.Request.Context(), conn, agentID, claims.Subject)
}
