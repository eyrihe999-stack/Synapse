package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	"github.com/eyrihe999-stack/Synapse/internal/oauth/service"
)

// ─── PAT DTO(内联 —— 非 OAuth 标准端点,走 BaseResponse)────────────────────

type createPATRequest struct {
	Label     string `json:"label" binding:"required"`
	ExpiresIn int    `json:"expires_in_seconds,omitempty"` // 0 = 不过期
}

type createPATResponse struct {
	ID        uint64     `json:"id"`
	Token     string     `json:"token"` // 明文,只此一次
	Label     string     `json:"label"`
	AgentID   uint64     `json:"agent_principal_id"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type patListItem struct {
	ID         uint64     `json:"id"`
	Label      string     `json:"label"`
	AgentID    uint64     `json:"agent_principal_id"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreatePAT POST /api/v2/users/me/pats
//
// 返 **明文 token,仅此一次**。后续拿 token 去调 MCP 端点时走 Bearer header。
func (h *Handler) CreatePAT(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	var req createPATRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: oauth.CodeOAuthInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	res, err := h.svc.PAT.Create(c.Request.Context(), service.CreatePATInput{
		UserID:    userID,
		Label:     req.Label,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "pat created", createPATResponse{
		ID:        res.PAT.ID,
		Token:     res.Token,
		Label:     res.PAT.Label,
		AgentID:   res.PAT.AgentID,
		ExpiresAt: res.PAT.ExpiresAt,
		CreatedAt: res.PAT.CreatedAt,
	})
}

// ListPATs GET /api/v2/users/me/pats
func (h *Handler) ListPATs(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	rows, err := h.svc.PAT.ListByUser(c.Request.Context(), userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	out := make([]patListItem, 0, len(rows))
	for i := range rows {
		out = append(out, patListItem{
			ID:         rows[i].ID,
			Label:      rows[i].Label,
			AgentID:    rows[i].AgentID,
			LastUsedAt: rows[i].LastUsedAt,
			ExpiresAt:  rows[i].ExpiresAt,
			RevokedAt:  rows[i].RevokedAt,
			CreatedAt:  rows[i].CreatedAt,
		})
	}
	response.Success(c, "ok", out)
}

// RevokePAT DELETE /api/v2/users/me/pats/:id
func (h *Handler) RevokePAT(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	idRaw := c.Param("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: oauth.CodeOAuthInvalidRequest, Message: "invalid id",
		})
		return
	}
	if err := h.svc.PAT.Revoke(c.Request.Context(), id, userID); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "pat revoked", nil)
}
