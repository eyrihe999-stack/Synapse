package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	taskerr "github.com/eyrihe999-stack/Synapse/internal/task"
)

// sendServiceError 把 service 层的哨兵错误翻译成 BaseResponse。
// 和 channel 模块风格一致:业务错误 HTTP 200 + body code;只 ErrTaskInternal 返 500。
func (h *Handler) sendServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, taskerr.ErrForbidden):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeForbidden, Message: "forbidden", Error: err.Error()})

	case errors.Is(err, taskerr.ErrTaskTitleInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskTitleInvalid, Message: "task title invalid"})
	case errors.Is(err, taskerr.ErrTaskDescriptionInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskDescriptionInvalid, Message: "task description invalid"})
	case errors.Is(err, taskerr.ErrTaskOutputKindInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskOutputKindInvalid, Message: "output kind invalid"})
	case errors.Is(err, taskerr.ErrTaskStatusInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskStatusInvalid, Message: "task status invalid"})
	case errors.Is(err, taskerr.ErrTaskStateTransition):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskStateTransition, Message: "state transition not allowed"})
	case errors.Is(err, taskerr.ErrTaskNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskNotFound, Message: "task not found"})
	case errors.Is(err, taskerr.ErrTaskAlreadyClaimed):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeTaskAlreadyClaimed, Message: "task already claimed"})

	case errors.Is(err, taskerr.ErrSubmissionEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeSubmissionEmpty, Message: "submission empty"})
	case errors.Is(err, taskerr.ErrSubmissionTooLarge):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeSubmissionTooLarge, Message: "submission too large"})
	case errors.Is(err, taskerr.ErrSubmissionContentKind):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeSubmissionContentKind, Message: "submission content kind mismatch"})
	case errors.Is(err, taskerr.ErrSubmissionNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeSubmissionNotFound, Message: "submission not found"})

	case errors.Is(err, taskerr.ErrReviewerDuplicate):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeReviewerDuplicate, Message: "reviewer already decided"})
	case errors.Is(err, taskerr.ErrDecisionInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeDecisionInvalid, Message: "decision invalid"})
	case errors.Is(err, taskerr.ErrAssigneeNotInChannel):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeAssigneeNotInChannel, Message: "assignee not in channel"})
	case errors.Is(err, taskerr.ErrReviewerNotInChannel):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeReviewerNotInChannel, Message: "reviewer not in channel"})
	case errors.Is(err, taskerr.ErrRequiredApprovalsRange):
		c.JSON(http.StatusOK, response.BaseResponse{Code: taskerr.CodeRequiredApprovalsRange, Message: "required_approvals out of range"})

	default:
		h.log.ErrorCtx(c.Request.Context(), "task: unmapped service error", err, nil)
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code: taskerr.CodeTaskInternal, Message: "internal error", Error: err.Error(),
		})
	}
}
