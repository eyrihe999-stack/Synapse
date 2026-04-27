package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// sendServiceError 把 service 层的哨兵错误翻译成前端可读的 BaseResponse。
//
// 业务码用 HHHSSCCCC 格式,HTTP 状态码统一 200(对齐仓库既有风格),只有
// ErrChannelInternal 用 500。
func (h *Handler) sendServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chanerr.ErrForbidden):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeForbidden, Message: "forbidden", Error: err.Error()})
	case errors.Is(err, chanerr.ErrProjectNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeProjectNotFound, Message: "project not found"})
	case errors.Is(err, chanerr.ErrProjectNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeProjectNameInvalid, Message: "project name invalid"})
	case errors.Is(err, chanerr.ErrProjectNameDup):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeProjectNameDuplicated, Message: "project name already used"})
	case errors.Is(err, chanerr.ErrProjectArchived):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeProjectArchived, Message: "project archived"})

	case errors.Is(err, chanerr.ErrVersionNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeVersionNotFound, Message: "version not found"})
	case errors.Is(err, chanerr.ErrVersionNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeVersionNameInvalid, Message: "version name invalid"})
	case errors.Is(err, chanerr.ErrVersionNameDup):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeVersionNameDuplicated, Message: "version name duplicated"})
	case errors.Is(err, chanerr.ErrVersionStatusInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeVersionStatusInvalid, Message: "version status invalid"})

	case errors.Is(err, chanerr.ErrChannelNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelNotFound, Message: "channel not found"})
	case errors.Is(err, chanerr.ErrChannelNameInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelNameInvalid, Message: "channel name invalid"})
	case errors.Is(err, chanerr.ErrChannelArchived):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelArchived, Message: "channel archived"})

	case errors.Is(err, chanerr.ErrMemberRoleInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMemberRoleInvalid, Message: "member role invalid"})
	case errors.Is(err, chanerr.ErrMemberLastOwner):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMemberLastOwner, Message: "cannot remove or demote last owner"})
	case errors.Is(err, chanerr.ErrMemberAlreadyExists):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMemberAlreadyExists, Message: "principal already a channel member"})
	case errors.Is(err, chanerr.ErrMemberNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMemberNotFound, Message: "channel member not found"})
	case errors.Is(err, chanerr.ErrPrincipalNotInOrg):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodePrincipalNotInOrg, Message: "principal not in channel org"})

	case errors.Is(err, chanerr.ErrMessageBodyInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMessageBodyInvalid, Message: "message body invalid"})
	case errors.Is(err, chanerr.ErrMessageKindInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMessageKindInvalid, Message: "message kind invalid"})
	case errors.Is(err, chanerr.ErrMessageMentionNotInChannel):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMessageMentionNotInChannel, Message: "mention target not in channel"})
	case errors.Is(err, chanerr.ErrMessageReplyTargetNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeMessageReplyTargetNotFound, Message: "reply target not found in this channel"})

	case errors.Is(err, chanerr.ErrKBRefInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeKBRefInvalid, Message: "kb ref invalid"})
	case errors.Is(err, chanerr.ErrKBRefNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeKBRefNotFound, Message: "kb ref not found"})

	case errors.Is(err, chanerr.ErrReactionEmojiInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeReactionEmojiInvalid, Message: "reaction emoji not in allowed set"})

	case errors.Is(err, chanerr.ErrChannelDocumentTitleInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentTitleInvalid, Message: "shared document title invalid"})
	case errors.Is(err, chanerr.ErrChannelDocumentKindInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentKindInvalid, Message: "shared document content_kind invalid"})
	case errors.Is(err, chanerr.ErrChannelDocumentContentTooLarge):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentContentTooLarge, Message: "shared document content too large"})
	case errors.Is(err, chanerr.ErrChannelDocumentContentEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentContentEmpty, Message: "shared document content empty"})
	case errors.Is(err, chanerr.ErrChannelDocumentLockHeld):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentLockHeld, Message: "document locked by another principal"})
	case errors.Is(err, chanerr.ErrChannelDocumentLockNotHeld):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentLockNotHeld, Message: "lock not held by caller"})
	case errors.Is(err, chanerr.ErrChannelDocumentVersionNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentVersionNotFound, Message: "shared document version not found"})
	case errors.Is(err, chanerr.ErrChannelDocumentNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentNotFound, Message: "shared document not found"})

	case errors.Is(err, uploadtoken.ErrTokenExpired):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentUploadTokenExpired, Message: "upload token expired, request a new presigned url"})
	case errors.Is(err, uploadtoken.ErrInvalidToken):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentUploadTokenInvalid, Message: "upload commit token invalid"})
	case errors.Is(err, chanerr.ErrChannelDocumentBaseVersionStale):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelDocumentBaseVersionStale, Message: "document changed since you downloaded; re-download and retry"})

	case errors.Is(err, chanerr.ErrChannelAttachmentMimeInvalid):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelAttachmentMimeInvalid, Message: "attachment mime type not allowed (only image/png|jpeg|gif|webp)"})
	case errors.Is(err, chanerr.ErrChannelAttachmentTooLarge):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelAttachmentTooLarge, Message: "attachment too large"})
	case errors.Is(err, chanerr.ErrChannelAttachmentEmpty):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelAttachmentEmpty, Message: "attachment content empty"})
	case errors.Is(err, chanerr.ErrChannelAttachmentNotFound):
		c.JSON(http.StatusOK, response.BaseResponse{Code: chanerr.CodeChannelAttachmentNotFound, Message: "attachment not found"})

	default:
		h.log.ErrorCtx(c.Request.Context(), "channel: unmapped service error", err, nil)
		c.JSON(http.StatusInternalServerError, response.BaseResponse{
			Code: chanerr.CodeChannelInternal, Message: "internal error", Error: err.Error(),
		})
	}
}
