package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
)

// parseUint64Param 提取 gin path param 为 uint64;失败直接写 400 响应并返 false。
//
// 对齐 channel/handler/project_handler.go 同名 helper。两份独立避免跨模块依赖。
func parseUint64Param(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code:    pm.CodePMInvalidRequest,
			Message: "invalid path param: " + name,
		})
		return 0, false
	}
	return v, true
}
