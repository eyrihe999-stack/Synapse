package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
)

// MetadataProvider 把外部注入的 issuer + path 转成 metadata endpoint 的绝对 URL。
type MetadataProvider struct {
	// Issuer AS 的基 URL(无尾 "/"),如 https://synapse.example.com。
	// .well-known 响应里所有 endpoint URL 由 Issuer + 固定路径拼出。
	Issuer string

	// MCPResourceURL MCP server 的完整 URL,出现在 /.well-known/oauth-protected-resource
	// 的 resource 字段里。默认 Issuer + "/api/v2/mcp"。
	MCPResourceURL string
}

// asMetadata RFC 8414 Authorization Server Metadata 响应结构。
// 字段遵循 RFC 8414 §2 + MCP 2025-11-25 对 AS 的要求。
type asMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ServiceDocumentation                       string   `json:"service_documentation,omitempty"`
}

// protectedResourceMetadata draft-ietf-oauth-resource-metadata 响应结构。
// MCP 2025-11-25 的 remote connector 靠这个告诉 client "去哪里拿 token"。
type protectedResourceMetadata struct {
	Resource              string   `json:"resource"`
	AuthorizationServers  []string `json:"authorization_servers"`
	ScopesSupported       []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// WellKnownAuthorizationServer GET /.well-known/oauth-authorization-server
//
// 匿名公开(RFC 8414 不要求认证),供 MCP client 发现 AS 元数据。
func (h *Handler) WellKnownAuthorizationServer(c *gin.Context) {
	issuer := h.trimmedIssuer()
	if issuer == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issuer_not_configured"})
		return
	}
	resp := asMetadata{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/oauth/authorize",
		TokenEndpoint:         issuer + "/oauth/token",
		RegistrationEndpoint:  issuer + "/oauth/register",
		RevocationEndpoint:    issuer + "/oauth/revoke",
		ResponseTypesSupported: []string{
			oauth.ResponseTypeCode,
		},
		GrantTypesSupported: []string{
			oauth.GrantTypeAuthorizationCode,
			oauth.GrantTypeRefreshToken,
		},
		TokenEndpointAuthMethodsSupported: []string{
			oauth.TokenAuthClientSecretBasic,
			oauth.TokenAuthClientSecretPost,
			oauth.TokenAuthNone,
		},
		CodeChallengeMethodsSupported: []string{oauth.PKCEMethodS256},
		ScopesSupported:               []string{oauth.ScopeMCPInvoke},
	}
	c.JSON(http.StatusOK, resp)
}

// WellKnownProtectedResource GET /.well-known/oauth-protected-resource
//
// 匿名公开。告知 client:这个资源(MCP 端点)的 access token 由哪个 AS 发。
// Claude Desktop remote connector 从这里拿 authorization_servers 作为 OAuth 入口。
func (h *Handler) WellKnownProtectedResource(c *gin.Context) {
	issuer := h.trimmedIssuer()
	if issuer == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issuer_not_configured"})
		return
	}
	resourceURL := h.metadata.MCPResourceURL
	if resourceURL == "" {
		resourceURL = issuer + "/api/v2/mcp"
	}
	resp := protectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{issuer},
		ScopesSupported:        []string{oauth.ScopeMCPInvoke},
		BearerMethodsSupported: []string{"header"},
	}
	c.JSON(http.StatusOK, resp)
}

// trimmedIssuer 去尾斜杠 —— 下游拼接 "/..." 时避免双斜杠。
func (h *Handler) trimmedIssuer() string {
	return strings.TrimRight(h.metadata.Issuer, "/")
}
