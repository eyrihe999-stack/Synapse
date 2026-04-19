// Package handler asyncjob 模块的 HTTP 入口。
//
// 暴露两条通用查询端点 —— 所有 kind 共用:
//
//	GET /api/v2/async-jobs/:id            单条任务(轮询进度用)
//	GET /api/v2/async-jobs?kind=X&limit=N 当前用户最近 N 条任务(历史列表用)
//
// 触发端点按业务模块自己挂(如 integration 模块挂 POST .../feishu/sync),
// 这样路径直观 /integrations/feishu/sync,查进度走共享路径。
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
	"github.com/eyrihe999-stack/Synapse/internal/middleware"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/response"
)

// JobService Handler 依赖的 service 方法集。签名和 *asyncjob/service.Service 精确匹配。
type JobService interface {
	GetJob(ctx context.Context, id uint64) (*model.Job, error)
	ListRecent(ctx context.Context, userID uint64, kind string, limit int) ([]*model.Job, error)
}

// Handler HTTP 入口。
type Handler struct {
	svc JobService
	log logger.LoggerInterface
}

// New 构造。
func New(svc JobService, log logger.LoggerInterface) *Handler {
	return &Handler{svc: svc, log: log}
}

// JobResponse 前端轮询拿到的任务快照。字段和 DB 对齐 + 加人可读状态。
//
// Payload 不回传 —— 前端已知自己提交了什么,无需回看。
// Result 的明细(失败条目 / 成功计数等)嵌在里面,前端按 kind 解析。
type JobResponse struct {
	ID             uint64       `json:"id"`
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
		h.log.ErrorCtx(c.Request.Context(), "async-jobs get: svc error", err, map[string]any{"job_id": id})
		response.InternalServerError(c, "load job failed", err.Error())
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

// ListJobs GET /api/v2/async-jobs?kind=...&limit=N
//
//	200 → { jobs: [JobResponse, ...] }
//
// 权限:只返当前用户自己的 job。kind 必填(不允许"跨 kind 翻全部"—— 避免遍历风险)。
// limit 默认 10,上限由 service 层 clamp 到 50。
func (h *Handler) ListJobs(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "Missing user context", "")
		return
	}
	kind := c.Query("kind")
	if kind == "" {
		response.BadRequest(c, "kind query param required", "")
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	jobs, err := h.svc.ListRecent(c.Request.Context(), userID, kind, limit)
	if err != nil {
		h.log.ErrorCtx(c.Request.Context(), "async-jobs list: svc error", err, map[string]any{
			"user_id": userID, "kind": kind,
		})
		response.InternalServerError(c, "list jobs failed", err.Error())
		return
	}
	out := make([]JobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, toJobResponse(j))
	}
	c.JSON(http.StatusOK, response.BaseResponse{
		Code: http.StatusOK, Message: "ok",
		Result: gin.H{"jobs": out},
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
