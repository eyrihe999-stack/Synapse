package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
	taskerr "github.com/eyrihe999-stack/Synapse/internal/task"
	"github.com/eyrihe999-stack/Synapse/internal/task/dto"
	"github.com/eyrihe999-stack/Synapse/internal/task/service"
)

// parseUint64Param 通用路径参数解析,参照 channel handler 风格。
func parseUint64Param(c *gin.Context, key string) (uint64, bool) {
	raw := c.Param(key)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid " + key,
		})
		return 0, false
	}
	return v, true
}

// CreateTask POST /api/v2/tasks
func (h *Handler) CreateTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	var req dto.CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	t, reviewers, err := h.svc.Task.Create(c.Request.Context(), service.CreateInput{
		ChannelID:            req.ChannelID,
		CreatorUserID:        userID,
		Title:                req.Title,
		Description:          req.Description,
		OutputSpecKind:       req.OutputSpecKind,
		IsLightweight:        req.IsLightweight,
		AssigneePrincipalID:  req.AssigneePrincipalID,
		ReviewerPrincipalIDs: req.ReviewerPrincipalIDs,
		RequiredApprovals:    req.RequiredApprovals,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	reviewerPIDs := make([]uint64, len(reviewers))
	for i, r := range reviewers {
		reviewerPIDs[i] = r.PrincipalID
	}
	response.Success(c, "task created", dto.CreateTaskResponse{
		Task:      dto.ToTaskResponse(t),
		Reviewers: reviewerPIDs,
	})
}

// GetTask GET /api/v2/tasks/:id
func (h *Handler) GetTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.Task.Get(c.Request.Context(), taskID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	resp := dto.TaskDetailResponse{
		Task:      dto.ToTaskResponse(&d.Task),
		Reviewers: d.Reviewers,
	}
	for _, s := range d.Submissions {
		resp.Submissions = append(resp.Submissions, dto.ToSubmissionResponse(&s))
	}
	for _, r := range d.Reviews {
		resp.Reviews = append(resp.Reviews, dto.ToReviewResponse(&r))
	}
	response.Success(c, "ok", resp)
}

// ListTasksByChannel GET /api/v2/channels/:id/tasks?status=&limit=&offset=
func (h *Handler) ListTasksByChannel(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	channelID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	status := c.Query("status")
	limit, offset := parseListQuery(c)
	rows, err := h.svc.Task.ListByChannel(c.Request.Context(), channelID, userID, status, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToTaskListResponse(rows))
}

// ListMyTasks GET /api/v2/users/me/tasks?status=&limit=&offset=
func (h *Handler) ListMyTasks(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	status := c.Query("status")
	limit, offset := parseListQuery(c)
	rows, err := h.svc.Task.ListMy(c.Request.Context(), userID, status, limit, offset)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "ok", dto.ToTaskListResponse(rows))
}

// ClaimTask POST /api/v2/tasks/:id/claim
func (h *Handler) ClaimTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	t, err := h.svc.Task.Claim(c.Request.Context(), taskID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "task claimed", dto.ToTaskResponse(t))
}

// SubmitTask POST /api/v2/tasks/:id/submit
func (h *Handler) SubmitTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.SubmitTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	t, sub, err := h.svc.Task.Submit(c.Request.Context(), service.SubmitInput{
		TaskID:          taskID,
		SubmitterUserID: userID,
		ContentKind:     req.ContentKind,
		Content:         []byte(req.Content),
		InlineSummary:   req.InlineSummary,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "submission saved", dto.SubmitTaskResponse{
		Task:       dto.ToTaskResponse(t),
		Submission: dto.ToSubmissionResponse(sub),
	})
}

// ReviewTask POST /api/v2/tasks/:id/review
func (h *Handler) ReviewTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.ReviewTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	t, rv, err := h.svc.Task.Review(c.Request.Context(), service.ReviewInput{
		TaskID:         taskID,
		SubmissionID:   req.SubmissionID,
		ReviewerUserID: userID,
		Decision:       req.Decision,
		Comment:        req.Comment,
	})
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "reviewed", dto.ReviewTaskResponse{
		Task:   dto.ToTaskResponse(t),
		Review: dto.ToReviewResponse(rv),
	})
}

// CancelTask POST /api/v2/tasks/:id/cancel
func (h *Handler) CancelTask(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	t, err := h.svc.Task.Cancel(c.Request.Context(), taskID, userID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "task cancelled", dto.ToTaskResponse(t))
}

// UpdateAssignee PATCH /api/v2/tasks/:id/assignee
// body:{ "assignee_principal_id": number }  (0 = 清空执行人)
func (h *Handler) UpdateAssignee(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateAssigneeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	t, err := h.svc.Task.UpdateAssignee(c.Request.Context(), taskID, userID, req.AssigneePrincipalID)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "assignee updated", dto.ToTaskResponse(t))
}

// UpdateReviewers PATCH /api/v2/tasks/:id/reviewers
// body:{ "reviewer_principal_ids": number[], "required_approvals": number }
func (h *Handler) UpdateReviewers(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		response.Unauthorized(c, "missing user context", "")
		return
	}
	taskID, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	var req dto.UpdateReviewersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, response.BaseResponse{
			Code: taskerr.CodeTaskInvalidRequest, Message: "invalid request", Error: err.Error(),
		})
		return
	}
	t, reviewers, err := h.svc.Task.UpdateReviewers(
		c.Request.Context(), taskID, userID, req.ReviewerPrincipalIDs, req.RequiredApprovals,
	)
	if err != nil {
		h.sendServiceError(c, err)
		return
	}
	response.Success(c, "reviewers updated", dto.UpdateReviewersResponse{
		Task:      dto.ToTaskResponse(t),
		Reviewers: reviewers,
	})
}

// parseListQuery 从 query 解析 limit / offset,夹紧到合法范围。
func parseListQuery(c *gin.Context) (int, int) {
	limit := taskerr.ListDefaultLimit
	offset := 0
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}
