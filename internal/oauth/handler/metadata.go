// metadata.go OAuth server metadata + resource metadata 发现端点。
//
// RFC 8414 (oauth-authorization-server) + RFC 9728 (oauth-protected-resource, MCP 采用)。
// 静态 JSON,由 config 驱动。URL 都基于 handler.cfg.Issuer 拼 —— 部署时这是唯一需要改的地方。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
)

// AuthorizationServerMetadata 返 /.well-known/oauth-authorization-server。
// 客户端(Claude Desktop 等)据此发现 authorize / token / register 端点的 URL。
func (h *Handler) AuthorizationServerMetadata(c *gin.Context) {
	md := map[string]any{
		"issuer":                                h.cfg.Issuer,
		"authorization_endpoint":                h.cfg.Issuer + "/oauth/authorize",
		"token_endpoint":                        h.cfg.Issuer + "/oauth/token",
		"registration_endpoint":                 h.cfg.Issuer + "/oauth/register",
		"revocation_endpoint":                   h.cfg.Issuer + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{oauth.ScopeMCP},
	}
	// 长寿命配置,给 CDN / client 缓存提示,但不设太长避免配置变更滞后。
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, md)
}

// ProtectedResourceMetadata 返 /.well-known/oauth-protected-resource(RFC 9728)。
// MCP 2025-03-26 规定 client 用这个端点发现 MCP server 对应哪个 AS。
// 当前 AS 就是 synapse 本身,authorization_servers 只列一条。
func (h *Handler) ProtectedResourceMetadata(c *gin.Context) {
	md := map[string]any{
		"resource":                 h.cfg.MCPResourceURL,
		"authorization_servers":    []string{h.cfg.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{oauth.ScopeMCP},
		"resource_name":            "Synapse Retrieval MCP",
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, md)
}
