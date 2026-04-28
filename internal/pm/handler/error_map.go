package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
)

// sendServiceError 把 service 层的哨兵错误翻译成前端可读的 BaseResponse。
//
// 业务码用 HHHSSCCCC 格式,HTTP 状态码统一 200(对齐仓库既有风格);
// 仅 ErrPMInternal 用 500。
func (h *Handler) sendServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, pm.ErrForbidden):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeForbidden, Message: "forbidden", Error: err.Error()})

	// Project
	case errors.Is(err, pm.ErrProjectNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectNotFound, Message: "project not found"})
	case errors.Is(err, pm.ErrProjectNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectNameInvalid, Message: "project name invalid"})
	case errors.Is(err, pm.ErrProjectNameDup):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectNameDuplicated, Message: "project name already used"})
	case errors.Is(err, pm.ErrProjectArchived):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectArchived, Message: "project archived"})

	// Initiative(T7 任务里 service 实现完会用到)
	case errors.Is(err, pm.ErrInitiativeNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeNotFound, Message: "initiative not found"})
	case errors.Is(err, pm.ErrInitiativeNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeNameInvalid, Message: "initiative name invalid"})
	case errors.Is(err, pm.ErrInitiativeNameDup):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeNameDuplicated, Message: "initiative name duplicated"})
	case errors.Is(err, pm.ErrInitiativeStatusInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeStatusInvalid, Message: "initiative status invalid"})
	case errors.Is(err, pm.ErrInitiativeArchived):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeArchived, Message: "initiative archived"})
	case errors.Is(err, pm.ErrInitiativeNotEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeNotEmpty, Message: "initiative has active workstreams"})
	case errors.Is(err, pm.ErrInitiativeSystem):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeInitiativeSystem, Message: "system initiative cannot be modified"})

	// Version
	case errors.Is(err, pm.ErrVersionNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeVersionNotFound, Message: "version not found"})
	case errors.Is(err, pm.ErrVersionNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeVersionNameInvalid, Message: "version name invalid"})
	case errors.Is(err, pm.ErrVersionNameDup):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeVersionNameDuplicated, Message: "version name duplicated"})
	case errors.Is(err, pm.ErrVersionStatusInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeVersionStatusInvalid, Message: "version status invalid"})
	case errors.Is(err, pm.ErrVersionSystem):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeVersionSystem, Message: "system version cannot be modified"})

	// Workstream(T7 任务里 service 实现完会用到)
	case errors.Is(err, pm.ErrWorkstreamNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeWorkstreamNotFound, Message: "workstream not found"})
	case errors.Is(err, pm.ErrWorkstreamNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeWorkstreamNameInvalid, Message: "workstream name invalid"})
	case errors.Is(err, pm.ErrWorkstreamStatusInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeWorkstreamStatusInvalid, Message: "workstream status invalid"})
	case errors.Is(err, pm.ErrWorkstreamInitiativeInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeWorkstreamInitiativeInvalid, Message: "workstream initiative invalid"})
	case errors.Is(err, pm.ErrWorkstreamVersionInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeWorkstreamVersionInvalid, Message: "workstream version invalid"})

	// ProjectKBRef
	case errors.Is(err, pm.ErrProjectKBRefInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectKBRefInvalid, Message: "project kb ref invalid"})
	case errors.Is(err, pm.ErrProjectKBRefNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectKBRefNotFound, Message: "project kb ref not found"})
	case errors.Is(err, pm.ErrProjectKBRefDuplicated):
		c.JSON(http.StatusOK, response.BaseResponse{Code: pm.CodeProjectKBRefDuplicated, Message: "project kb ref already attached"})

	default:
		h.log.ErrorCtx(c.Request.Context(), "pm: unmapped service error", err, nil)
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code: pm.CodePMInternal, Message: "internal error", Error: err.Error(),
		})
	}
}
