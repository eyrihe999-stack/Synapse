package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/channel/dto"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// PostChannelMessage POST /api/v2/channels/:id/messages
//
// 请求体:{"body": "...", "mentions": [principal_id, ...]}
// @xxx 文本不由服务端解析,Mentions 列表需前端 / MCP client 主动带上。
func (h *Handler) PostChannelMessage(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.PostMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}

	var replyTo uint64
	if req.ReplyToMessageID != nil {
		replyTo = *req.ReplyToMessageID
	}
	posted, err := h.svc.Message.Post(c.Request.Context(), channelID, userID, req.Body, req.Mentions, replyTo)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	// 刚 Post 的消息没有 reply preview / reactions,传 nil
	response.Success(c, "message posted", dto.ToMessageResponse(posted.Message, posted.Mentions, nil, nil))
}

// AddReaction POST /api/v2/messages/:id/reactions  body: {"emoji": "👍"}
func (h *Handler) AddReaction(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	messageID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.AddReactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	if err := h.svc.Message.AddReaction(c.Request.Context(), messageID, userID, req.Emoji); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "reaction added", nil)
}

// RemoveReaction DELETE /api/v2/messages/:id/reactions/:emoji
// 注意:emoji 走 URL path,前端需要 encodeURIComponent。
func (h *Handler) RemoveReaction(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	messageID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	emoji := c.Param("emoji")
	if emoji == "" {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: chanerr.CodeChannelInvalidRequest, Message: "missing emoji",
		})
		return
	}
	if err := h.svc.Message.RemoveReaction(c.Request.Context(), messageID, userID, emoji); err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "reaction removed", nil)
}

// ListChannelMessages GET /api/v2/channels/:id/messages?before_id=&limit=
//
// 按 id 倒序分页。before_id 省略或 0 = 从最新开始;limit 省略走默认 50,上限 100。
// 响应 cursor:下一页起点(= 本页最老一条的 id);cursor=0 表示无更多。
func (h *Handler) ListChannelMessages(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}

	var beforeID uint64
	if raw := c.Query("before_id"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusOK, response.BaseResponse{
				Code: chanerr.CodeChannelInvalidRequest, Message: "invalid before_id",
			})
			return
		}
		beforeID = v
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			c.JSON(http.StatusOK, response.BaseResponse{
				Code: chanerr.CodeChannelInvalidRequest, Message: "invalid limit",
			})
			return
		}
		limit = v
	}

	items, err := h.svc.Message.List(c.Request.Context(), channelID, userID, beforeID, limit)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	resp := dto.ListMessagesResponse{
		Messages: make([]dto.ChannelMessageResponse, 0, len(items)),
	}
	for _, it := range items {
		msg := it.Message
		resp.Messages = append(resp.Messages, dto.ToMessageResponse(&msg, it.Mentions, toReplyPreviewDTO(it.ReplyPreview), toReactionsDTO(it.Reactions)))
	}
	// cursor = 本页最老一条的 id(即最后一条,倒序最后一个)。如果本页不满 limit,
	// 说明后面没更多,cursor 留 0。
	if len(items) > 0 && len(items) == effectiveLimit(limit) {
		resp.Cursor = items[len(items)-1].Message.ID
	}
	response.Success(c, "ok", resp)
}

// effectiveLimit 按 service 层规则夹紧 limit,判定是否"满页"。
func effectiveLimit(in int) int {
	if in <= 0 {
		return chanerr.MessageListDefaultLimit
	}
	if in > chanerr.MessageListMaxLimit {
		return chanerr.MessageListMaxLimit
	}
	return in
}

// toReplyPreviewDTO 把 service 层的 ReplyPreview 结构转成对外 DTO。nil → nil。
func toReplyPreviewDTO(p *channelsvc.ReplyPreview) *dto.ReplyPreviewResponse {
	if p == nil {
		return nil
	}
	return &dto.ReplyPreviewResponse{
		MessageID:         p.MessageID,
		AuthorPrincipalID: p.AuthorPrincipalID,
		BodySnippet:       p.BodySnippet,
		Missing:           p.Missing,
	}
}

// toReactionsDTO 把 service 层的 ReactionEntry 列表转成 DTO。空列表返 nil
// (DTO 端 omitempty 自然省略 JSON 字段)。
func toReactionsDTO(rs []channelsvc.ReactionEntry) []dto.ReactionEntryResponse {
	if len(rs) == 0 {
		return nil
	}
	out := make([]dto.ReactionEntryResponse, len(rs))
	for i, r := range rs {
		out[i] = dto.ReactionEntryResponse{Emoji: r.Emoji, PrincipalIDs: r.PrincipalIDs}
	}
	return out
}
