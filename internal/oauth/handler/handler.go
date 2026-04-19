// Package handler oauth 模块 HTTP 层。
//
// 端点总览(RFC 6749 / 7591 / 7009 / 8414 + MCP 2025-03-26):
//   - GET  /.well-known/oauth-authorization-server  server metadata
//   - GET  /.well-known/oauth-protected-resource    resource server metadata(指向 MCP endpoint)
//   - POST /oauth/register                          DCR,自注册 public client
//   - GET  /oauth/authorize                         authorize(未登录跳 login,已登录渲染 consent)
//   - POST /oauth/authorize                         consent 提交
//   - POST /oauth/token                             code / refresh 交换
//   - POST /oauth/revoke                            撤销 refresh token
//
// 本文件只放 Handler struct + 构造器 + 错误映射 helper;具体端点实现拆到同包其他文件。
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// Config handler 层配置。Issuer 必须和 service.Config.Issuer 严格相等,
// .well-known metadata / JWT iss claim / consent 页 UI 都引用同一个。
// MCPResourceURL 是 protected-resource metadata 指向的 MCP endpoint(task 9 创建)。
//
// CookieSecret 给 /oauth 流程 cookie(登录态)做 HMAC-SHA256 签名,
// 建议 ≥ 32 字节。可以和 service signing key 分开,也可以复用同一 cfg.OAuth.SigningKey。
// CookieSecure = true 时 cookie 只走 HTTPS,生产必须开。
type Config struct {
	Issuer         string
	MCPResourceURL string // e.g. "https://synapse.example.com/api/v2/retrieval/mcp"
	CookieSecret   []byte
	CookieSecure   bool
}

// Handler 持 service + config + adapters + logger。login / org adapter 允许 handler
// 解耦 user / organization 具体 service,保持 oauth 模块单向依赖(handler → service → repo)。
type Handler struct {
	svc        service.Service
	cfg        Config
	login      LoginAdapter
	org        OrgAdapter
	flowCookie *flowCookie
	log        logger.LoggerInterface
}

func New(cfg Config, svc service.Service, login LoginAdapter, org OrgAdapter, log logger.LoggerInterface) *Handler {
	if svc == nil || log == nil || login == nil || org == nil {
		panic("oauth handler: svc / login / org / log must be non-nil")
	}
	if cfg.Issuer == "" {
		panic("oauth handler: Issuer must be set")
	}
	fc, err := newFlowCookie(cfg.CookieSecret, cfg.CookieSecure)
	if err != nil {
		panic("oauth handler: " + err.Error())
	}
	return &Handler{
		svc:        svc,
		cfg:        cfg,
		login:      login,
		org:        org,
		flowCookie: fc,
		log:        log,
	}
}

// ─── OAuth 错误响应 ─────────────────────────────────────────────────────────

// writeOAuthError 按 RFC 6749 §5.2 格式写 JSON 错误。
// 根据 service 层 sentinel 映射到 OAuth error code + 对应 HTTP 状态。
func writeOAuthError(c *gin.Context, err error) {
	code := mapErrorCode(err)
	status := oauthErrorStatus(code)

	// 错误响应也要 no-store —— 防代理 / 浏览器缓存泄漏。
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	body := gin.H{"error": code}
	if msg := err.Error(); msg != "" {
		body["error_description"] = msg
	}
	c.JSON(status, body)
}

func mapErrorCode(err error) string {
	switch {
	case errors.Is(err, service.ErrInvalidRequest):
		return oauth.ErrCodeInvalidRequest
	case errors.Is(err, service.ErrInvalidClient):
		return oauth.ErrCodeInvalidClient
	case errors.Is(err, service.ErrInvalidGrant):
		return oauth.ErrCodeInvalidGrant
	case errors.Is(err, service.ErrInvalidClientMetadata):
		return oauth.ErrCodeInvalidClientMetadata
	case errors.Is(err, service.ErrInvalidRedirectURI):
		return oauth.ErrCodeInvalidRedirectURI
	case errors.Is(err, service.ErrInvalidScope):
		return oauth.ErrCodeInvalidScope
	case errors.Is(err, service.ErrUnsupportedResponseType):
		return oauth.ErrCodeUnsupportedResponseType
	case errors.Is(err, service.ErrUnsupportedGrantType):
		return oauth.ErrCodeUnsupportedGrantType
	case errors.Is(err, service.ErrAccessDenied):
		return oauth.ErrCodeAccessDenied
	case errors.Is(err, service.ErrServerError):
		return oauth.ErrCodeServerError
	default:
		// 未知错误统一 server_error 外加内部 log,不泄漏 err 详情给 client。
		return oauth.ErrCodeServerError
	}
}

func oauthErrorStatus(code string) int {
	switch code {
	case oauth.ErrCodeInvalidClient:
		// RFC 6749 §5.2:invalid_client 在走 HTTP 401 的同时要带 WWW-Authenticate;
		// 当前我们没用 Basic auth(public client only),401 不带 auth header 也可以。
		return http.StatusUnauthorized
	case oauth.ErrCodeServerError:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}
