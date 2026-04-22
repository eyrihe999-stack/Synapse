// Package handler asyncjob 模块的 HTTP 入口。
//
// 暴露单条轮询端点 —— 所有 kind 共用:
//
//	GET /api/v2/async-jobs/:id            单条任务(轮询进度用)
//
// 触发端点按业务模块自己挂(如 document 模块挂 POST .../documents/upload),
// 这样路径直观,查进度走共享路径。列表端点(按 kind 翻历史)前端未使用,保留接口定义
// 只会堆积死代码,已下线;将来真有同步历史视图需求再按需补回。
//
// 权限:owner 可查自己的 job —— (user_id == current_user)。
// 未来要让组织管理员看成员 job,在此基础上加 role check。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// JobService Handler 依赖的 service 方法集。签名和 *asyncjob/service.Service 精确匹配。
type JobService interface {
	GetJob(ctx context.Context, id uint64) (*model.Job, error)
}

// Handler HTTP 入口。
type Handler struct {
	svc JobService
	log logger.LoggerInterface
}

// New 构造 asyncjob HTTP handler。svc 为 service.Service(或兼容接口),
// log 用于业务日志 + 错误兜底。两者都不可为 nil,调用方保证传入。
func New(svc JobService, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}

// JobResponse 前端轮询拿到的任务快照。字段和 DB 对齐 + 加人可读状态。
//
// Payload 不回传 —— 前端已知自己提交了什么,无需回看。
// Result 的明细(失败条目 / 成功计数等)嵌在里面,前端按 kind 解析。
type JobResponse struct {
	// ID 走 `,string` tag 序列化成 JSON 字符串:snowflake uint64 超过 JS Number 精度(2^53),
	// 直接发数字会被前端 JSON.parse 截断末尾几位。
	ID             uint64       `json:"id,string"`
	Kind           string       `json:"kind"`
	Status         model.Status `json:"status"`
	ProgressTotal  int          `json:"progress_total"`
	ProgressDone   int          `json:"progress_done"`
	ProgressFailed int          `json:"progress_failed"`
	// Result 终态结果摘要(任务自定义的 JSON)。running 时通常为 nil。
	// 用 json.RawMessage 原样透传字节,不二次 marshal —— 若用 []byte 会被 Go 的 encoder
	// 做 base64 编码,前端就拿到字符串而不是嵌套对象。
	Result json.RawMessage `json:"result,omitempty" swaggertype:"object"`
	// Error 终态 failed 时的根因字符串。
	Error string `json:"error,omitempty"`
	// CreatedAt / StartedAt / FinishedAt 统一 unix seconds。未发生的阶段返 null。
	CreatedAt  int64  `json:"created_at"`
	StartedAt  *int64 `json:"started_at,omitempty"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// GetJob GET /api/v2/async-jobs/:id
//
//	200 → JobResponse
//	403 → 非 owner 查他人 job
//	404 → job 不存在
func (h *Handler) GetJob(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		response.BadRequest(c, "invalid job id", idStr)
		return
	}
	job, err := h.svc.GetJob(c.Request.Context(), id)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	if job == nil {
		response.NotFound(c, "job not found", "")
		return
	}
	if job.UserID != userID {
		// 不泄漏存在性:非 owner 一律 404,和"不存在"等效。403 留给将来加 admin 权限分支。
		response.NotFound(c, "job not found", "")
		return
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: toJobResponse(job),
	})
}

func toJobResponse(j *model.Job) JobResponse {
	out := JobResponse{
		ID:             j.ID,
		Kind:           j.Kind,
		Status:         j.Status,
		ProgressTotal:  j.ProgressTotal,
		ProgressDone:   j.ProgressDone,
		ProgressFailed: j.ProgressFailed,
		Error:          j.Error,
		CreatedAt:      j.CreatedAt.Unix(),
	}
	if len(j.Result) > 0 {
		// datatypes.JSON 已经是合法 JSON 字节,直接塞给 json.RawMessage 原样透传。
		out.Result = json.RawMessage(j.Result)
	}
	if j.StartedAt != nil {
		t := j.StartedAt.Unix()
		out.StartedAt = &t
	}
	if j.FinishedAt != nil {
		t := j.FinishedAt.Unix()
		out.FinishedAt = &t
	}
	return out
}
