package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// dcrRequest RFC 7591 § 2 Client Metadata 的请求子集(挑我们支持的字段)。
// 其他字段(如 client_uri / logo_uri / scope / contacts 等)会被 server 接受但忽略,
// Claude Desktop / Cursor / Codex 等实际 MCP client 也不依赖它们。
type dcrRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// dcrResponse RFC 7591 § 3.2 Response。
type dcrResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"` // public client(method=none)时为空
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at"` // 0 = 不过期
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// dcrErrorResponse RFC 7591 § 3.2.2 错误响应。
// error ∈ {invalid_redirect_uri, invalid_client_metadata, ...}
type dcrErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// DynamicClientRegister POST /oauth/register
//
// 匿名开放(不鉴权),仅做 per-IP 限速(Redis 滑动窗口)。
// 返回 RFC 7591 标准响应;失败时返 RFC 7591 错误格式 (不走 BaseResponse)。
//
// 安全考虑(MVP):
//   - per-IP 限速防注册风暴
//   - 注册的 client 立即可用(不做人工审核 —— 审核流程对 MCP client 零意义)
//   - redirect_uris 严格校验 scheme(https 或 http://localhost)
//   - Admin 可通过 /api/v2/oauth/clients 看到 DCR 产生的 client(registered_via='dcr'),
//     发现可疑可手动 disable(MVP 里 disable 只对自己建的生效,DCR 匿名的需要未来 admin 接口)
func (h *Handler) DynamicClientRegister(c *gin.Context) {
	// 1. per-IP 限速
	if h.dcrRateLimiter != nil {
		ip := clientIP(c)
		key := "synapse:oauth:dcr_rl:" + ip
		count, err := h.dcrRateLimiter.Add(c.Request.Context(), key, time.Now(), time.Duration(h.dcrRateLimitWindowSec)*time.Second)
		if err != nil {
			h.log.WarnCtx(c.Request.Context(), "oauth: dcr rate limit check failed", map[string]any{
				"err": err.Error(),
			})
			// Redis 挂了不让它阻断 DCR —— 继续走。限速降级,监控告警应能察觉
		} else if count > int64(h.dcrRateLimitMax) {
			writeDCRError(c, http.StatusTooManyRequests, "invalid_client_metadata", "too many registrations from this IP; retry later")
			return
		}
	}

	// 2. 解请求
	var req dcrRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeDCRError(c, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}

	// 3. 调 service.Client.Create 复用 manual 路径核心逻辑,但 ActorUserID=0 + RegisteredVia=dcr
	creds, err := h.svc.Client.Create(c.Request.Context(), service.CreateClientInput{
		ActorUserID:     0,
		ClientName:      req.ClientName,
		RedirectURIs:    req.RedirectURIs,
		GrantTypes:      req.GrantTypes,
		TokenAuthMethod: req.TokenEndpointAuthMethod,
		RegisteredVia:   oauth.RegisteredViaDCR,
	})
	if err != nil {
		// 翻译成 RFC 7591 错误码。本模块的哨兵错误大部分属于 invalid_client_metadata 类
		errCode := "invalid_client_metadata"
		if err == oauth.ErrRedirectURIInvalid || err == oauth.ErrRedirectURIsEmpty {
			errCode = "invalid_redirect_uri"
		}
		writeDCRError(c, http.StatusBadRequest, errCode, err.Error())
		return
	}

	// 4. 构造响应
	redirectURIs := req.RedirectURIs // 原样回给客户端(经过 service 侧校验后写入 DB,这里复用请求体)
	grantTypes := req.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{oauth.GrantTypeAuthorizationCode, oauth.GrantTypeRefreshToken}
	}
	responseTypes := req.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = []string{oauth.ResponseTypeCode}
	}
	authMethod := req.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = oauth.TokenAuthClientSecretPost
	}
	resp := dcrResponse{
		ClientID:                creds.ClientIDPlain,
		ClientSecret:            creds.ClientSecretPlain,
		ClientIDIssuedAt:        creds.Client.CreatedAt.Unix(),
		ClientSecretExpiresAt:   0, // 我们不过期 client_secret
		ClientName:              req.ClientName,
		RedirectURIs:            redirectURIs,
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
		TokenEndpointAuthMethod: authMethod,
	}
	c.JSON(http.StatusCreated, resp)
}

// writeDCRError RFC 7591 § 3.2.2 错误格式。
func writeDCRError(c *gin.Context, httpStatus int, code, description string) {
	c.JSON(httpStatus, dcrErrorResponse{
		Error:            code,
		ErrorDescription: description,
	})
}

// clientIP 取客户端 IP。gin 默认 ClientIP 已经处理好 TrustedProxies。
func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "" {
		return "unknown"
	}
	return ip
}

// ensureDCRRateLimitDefaults 在 Handler 构造时兜底 defaults,
// 调用方传 0 时走 DefaultDCRRateLimitWindow / DefaultDCRRateLimitMax。
func ensureDCRRateLimitDefaults(window, max int) (int, int) {
	if window <= 0 {
		window = oauth.DefaultDCRRateLimitWindow
	}
	if max <= 0 {
		max = oauth.DefaultDCRRateLimitMax
	}
	return window, max
}

// Ensure oauth.* is referenced (fmt import placeholder to avoid unused import when trimmed).
var _ = fmt.Sprint
