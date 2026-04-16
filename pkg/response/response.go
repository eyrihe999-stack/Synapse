package response

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// BaseResponse represents the standard API response structure
type BaseResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// PaginationResponse represents paginated response metadata
type PaginationResponse struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// PaginatedResponse represents a paginated API response
type PaginatedResponse struct {
	Code       int                `json:"code"`
	Message    string             `json:"message"`
	Result     interface{}        `json:"result"`
	Pagination PaginationResponse `json:"pagination"`
	Error      string             `json:"error,omitempty"`
}

func Success(c *gin.Context, message string, data interface{}) {
	c.JSON(http.StatusOK, BaseResponse{Code: http.StatusOK, Message: message, Result: data})
}

func SuccessWithCode(c *gin.Context, code int, message string, data interface{}) {
	c.JSON(code, BaseResponse{Code: code, Message: message, Result: data})
}

func Error(c *gin.Context, code int, message, errorDetail string) {
	c.JSON(code, BaseResponse{Code: code, Message: message, Error: errorDetail})
}

func ErrorWithData(c *gin.Context, code int, message, errorDetail string, data interface{}) {
	c.JSON(code, BaseResponse{Code: code, Message: message, Error: errorDetail, Result: data})
}

func BadRequest(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusBadRequest, message, errorDetail)
}

func Unauthorized(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusUnauthorized, message, errorDetail)
}

func Forbidden(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusForbidden, message, errorDetail)
}

func NotFound(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusNotFound, message, errorDetail)
}

func MethodNotAllowed(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusMethodNotAllowed, message, errorDetail)
}

func Conflict(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusConflict, message, errorDetail)
}

func UnprocessableEntity(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusUnprocessableEntity, message, errorDetail)
}

func TooManyRequests(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusTooManyRequests, message, errorDetail)
}

func InternalServerError(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusInternalServerError, message, errorDetail)
}

func BadGateway(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusBadGateway, message, errorDetail)
}

func ServiceUnavailable(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusServiceUnavailable, message, errorDetail)
}

func GatewayTimeout(c *gin.Context, message, errorDetail string) {
	Error(c, http.StatusGatewayTimeout, message, errorDetail)
}

func Paginated(c *gin.Context, message string, data interface{}, pagination PaginationResponse) {
	c.JSON(http.StatusOK, PaginatedResponse{
		Code: http.StatusOK, Message: message, Result: data, Pagination: pagination,
	})
}

func Created(c *gin.Context, message string, data interface{}) {
	SuccessWithCode(c, http.StatusCreated, message, data)
}

func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

func ValidationError(c *gin.Context, errors map[string]string) {
	c.JSON(http.StatusBadRequest, map[string]interface{}{
		"code": http.StatusBadRequest, "message": "Validation failed", "errors": errors,
	})
}

// APIError represents an API error with additional context
type APIError struct {
	Code      int                    `json:"code"`
	Message   string                 `json:"message"`
	Details   string                 `json:"details,omitempty"`
	Timestamp string                 `json:"timestamp"`
	Path      string                 `json:"path"`
	Method    string                 `json:"method"`
	RequestID string                 `json:"request_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

func ErrorWithContext(c *gin.Context, code int, message, details string, metadata map[string]interface{}) {
	c.JSON(code, APIError{
		Code: code, Message: message, Details: details,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Path: c.Request.URL.Path, Method: c.Request.Method,
		RequestID: c.GetString("request_id"), Metadata: metadata,
	})
}

func HealthCheck(c *gin.Context, status string, data interface{}) {
	var code int
	switch status {
	case "healthy", "ok":
		code = http.StatusOK
	case "degraded":
		code = http.StatusOK
	case "unhealthy":
		code = http.StatusServiceUnavailable
	default:
		code = http.StatusInternalServerError
	}
	c.JSON(code, map[string]interface{}{
		"status": status, "timestamp": time.Now().UTC().Format(time.RFC3339), "data": data,
	})
}
