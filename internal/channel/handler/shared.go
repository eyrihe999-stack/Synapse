package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	chanerr "github.com/eyrihe999-stack/Synapse/internal/channel"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// parseUint64Param 提取 gin path param 为 uint64;失败直接写 400 响应并返 false。
//
// 原本定义在 project_handler.go;Project 物理迁到 pm 模块后那个文件被删,helper
// 拿到这里独立保留(channel 下 channel_handler / member_handler / message_handler /
// kb_ref_handler / document_handler / attachment_handler 都在用)。
func parseUint64Param(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    chanerr.CodeChannelInvalidRequest,
			Message: "invalid path param: " + name,
		})
		return 0, false
	}
	return v, true
}
