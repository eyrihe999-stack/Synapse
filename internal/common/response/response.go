// Package response 统一 HTTP 响应结构与便捷构造器。
//
// 所有业务 handler 的 JSON 出口都应走本包构造 BaseResponse —— 要么调用封装好的
// helper(Success/BadRequest/NotFound 等),要么手写 `c.JSON(code, BaseResponse{...})`。
// 禁止直接返回 gin.H / map / 自定义 struct,否则前端约定的 {code,message,result,error}
// 契约会被破坏(参见 tools/sayso-lint/gin.go checkResponseShape 规则)。
//
// 合法例外:
//   - NoContent:204 空 body,用于 navigator.sendBeacon 等 fire-and-forget 场景
//   - c.Redirect:OAuth 浏览器重定向,不是 API 数据出口
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BaseResponse 项目唯一的 JSON 响应结构。
//
// 字段约定:
//   - Code:业务状态码。成功为 200/201,失败为业务错误码(HHHSSCCCC)
//   - Message:人类可读摘要
//   - Result:成功时的 payload。omitempty —— 纯状态操作(删除/踢会话/登出)
//     没有数据要返时,字段整个省掉,前端靠 Code 判断成败,不依赖 Result 存在与否
//   - Error:失败时的详细描述,omitempty
type BaseResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Success 返回 200 OK + BaseResponse。data 可为 nil(纯状态操作)。
func Success(c *gin.Context, message string, data interface{}) {
	c.JSON(http.StatusOK, BaseResponse{Code: http.StatusOK, Message: message, Result: data})
}

// SuccessWithCode 返回自定义 HTTP code(如 201、202) + BaseResponse。
// 被 Created 内部调用,业务 handler 通常不直接使用。
func SuccessWithCode(c *gin.Context, code int, message string, data interface{}) {
	c.JSON(code, BaseResponse{Code: code, Message: message, Result: data})
}

// Error 返回自定义 HTTP code + BaseResponse(只填 Error 不填 Result)。
// 被 BadRequest / Unauthorized / NotFound 等内部委托,业务 handler 通常不直接使用。
func Error(c *gin.Context, code int, message, errorDetail string) {
	c.JSON(code, BaseResponse{Code: code, Message: message, Error: errorDetail})
}

func BadRequest(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusBadRequest, message, errorDetail)
}

func Unauthorized(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusUnauthorized, message, errorDetail)
}

func NotFound(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusNotFound, message, errorDetail)
}

func Forbidden(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusForbidden, message, errorDetail)
}

func TooManyRequests(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusTooManyRequests, message, errorDetail)
}

func InternalServerError(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusInternalServerError, message, errorDetail)
}

// Created 返回 201 Created + BaseResponse。用于资源创建成功。
func Created(c *gin.Context, message string, data interface{}) {
	SuccessWithCode(c, http.StatusCreated, message, data)
}

// NoContent 返回 204 空 body。
//
// 只在真正不需要响应体的场景使用,目前仅 navigator.sendBeacon 调用的
// /auth/logout-beacon 端点用此响应 —— Beacon API 拿不到响应体,返 204 是 web 标准做法。
// 常规业务 API 成功响应应走 Success(c, msg, nil) 返 BaseResponse,保持契约统一。
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}
