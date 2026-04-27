package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/dto"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// CreateOAuthClient POST /api/v2/oauth/clients
//
// 用户手动建 OAuth client(每人能建自己的 —— 对应 Claude Desktop 的 Custom
// Connector UI 里需要填的 client_id / client_secret)。
//
// 响应里的 client_secret 是明文,**仅此一次返回**,客户端必须当场保存。
func (h *Handler) CreateOAuthClient(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	var req dto.CreateClientRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: oauth.CodeOAuthInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}

	creds, err := h.svc.Client.Create(c.Request.Context(), service.CreateClientInput{
		ActorUserID:     userID,
		ClientName:      req.ClientName,
		RedirectURIs:    req.RedirectURIs,
		GrantTypes:      req.GrantTypes,
		TokenAuthMethod: req.TokenAuthMethod,
		RegisteredVia:   oauth.RegisteredViaManual,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}

	var redirectURIs, grantTypes []string
	_ = json.Unmarshal([]byte(creds.Client.RedirectURIsJSON), &redirectURIs)
	_ = json.Unmarshal([]byte(creds.Client.GrantTypesJSON), &grantTypes)

	response.Success(c, "client created", dto.CreateClientResponse{
		ClientID:                creds.ClientIDPlain,
		ClientSecret:            creds.ClientSecretPlain,
		ClientName:              creds.Client.ClientName,
		RedirectURIs:            redirectURIs,
		GrantTypes:              grantTypes,
		TokenEndpointAuthMethod: creds.Client.TokenAuthMethod,
		RegisteredVia:           creds.Client.RegisteredVia,
		CreatedAt:               creds.Client.CreatedAt,
	})
}

// ListOAuthClients GET /api/v2/oauth/clients
//
// 只返当前 user 建的 client(隔离到人)。
func (h *Handler) ListOAuthClients(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	rows, err := h.svc.Client.ListByUser(c.Request.Context(), userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	out := make([]dto.ListClientResponse, 0, len(rows))
	for i := range rows {
		var redirectURIs, grantTypes []string
		_ = json.Unmarshal([]byte(rows[i].RedirectURIsJSON), &redirectURIs)
		_ = json.Unmarshal([]byte(rows[i].GrantTypesJSON), &grantTypes)
		out = append(out, dto.ListClientResponse{
			ID:                      rows[i].ID,
			ClientID:                rows[i].ClientID,
			ClientName:              rows[i].ClientName,
			RedirectURIs:            redirectURIs,
			GrantTypes:              grantTypes,
			TokenEndpointAuthMethod: rows[i].TokenAuthMethod,
			RegisteredVia:           rows[i].RegisteredVia,
			Disabled:                rows[i].Disabled,
			CreatedAt:               rows[i].CreatedAt,
		})
	}
	response.Success(c, "ok", out)
}

// DisableOAuthClient POST /api/v2/oauth/clients/:id/disable
//
// 禁用 + 级联吊销该 client 所有 access/refresh tokens。
func (h *Handler) DisableOAuthClient(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: oauth.CodeOAuthInvalidRequest, Message: "invalid id",
		})
		return
	}
	if err := h.svc.Client.Disable(c.Request.Context(), id, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "client disabled", nil)
}
