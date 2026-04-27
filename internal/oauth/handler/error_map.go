package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
)

// sendServiceError 把 service 层哨兵错误翻译成前端可读 BaseResponse。
//
// 注意:本 mapping 用于 **管理 API**(/api/v2/oauth/clients/*、/api/v2/users/me/pats/*);
// 标准 OAuth 端点(/oauth/authorize、/oauth/token、/oauth/revoke)遵循 RFC 6749 错误
// 响应格式,由各自 handler 自行处理,不走这里。
func (h *Handler) sendServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, oauth.ErrForbidden):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeForbidden, Message: "forbidden", Error: err.Error()})

	case errors.Is(err, oauth.ErrClientNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeClientNameInvalid, Message: "client name invalid"})
	case errors.Is(err, oauth.ErrRedirectURIInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeRedirectURIInvalid, Message: "redirect uri invalid"})
	case errors.Is(err, oauth.ErrRedirectURIsEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeRedirectURIsEmpty, Message: "redirect_uris empty"})
	case errors.Is(err, oauth.ErrGrantTypeInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeGrantTypeInvalid, Message: "grant_type invalid"})
	case errors.Is(err, oauth.ErrTokenAuthMethodInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeTokenAuthMethodInvalid, Message: "token_endpoint_auth_method invalid"})

	case errors.Is(err, oauth.ErrClientNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeClientNotFound, Message: "client not found"})
	case errors.Is(err, oauth.ErrPATNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodePATNotFound, Message: "pat not found"})
	case errors.Is(err, oauth.ErrPATLabelInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodePATLabelInvalid, Message: "pat label invalid"})

	case errors.Is(err, oauth.ErrDCRRateLimited):
		c.JSON(http.StatusOK, response.BaseResponse{Code: oauth.CodeDCRRateLimited, Message: "dcr rate limited"})

	default:
		h.log.ErrorCtx(c.Request.Context(), "oauth: unmapped service error", err, nil)
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code: oauth.CodeOAuthInternal, Message: "internal error", Error: err.Error(),
		})
	}
}
