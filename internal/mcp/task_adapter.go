package mcp

import (
	"context"

	taskmodel "github.com/eyrihe999-stack/Synapse/internal/task/model"
	tasksvc "github.com/eyrihe999-stack/Synapse/internal/task/service"
)

// TaskAdapter 把 task.service 的 by-principal 方法包成 TaskFacade。
// main.go 注入:&mcp.TaskAdapter{TaskSvc: taskService.Task}
type TaskAdapter struct {
	TaskSvc tasksvc.TaskService
}

func (a *TaskAdapter) ListMyTasksForPrincipal(ctx context.Context, callerPrincipalID uint64, role, status string, limit, offset int) ([]taskmodel.Task, error) {
	return a.TaskSvc.ListMyTasksByPrincipal(ctx, callerPrincipalID, role, status, limit, offset)
}

func (a *TaskAdapter) GetTaskForPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*tasksvc.TaskDetail, error) {
	return a.TaskSvc.GetByPrincipal(ctx, taskID, callerPrincipalID)
}

func (a *TaskAdapter) CreateTaskByPrincipal(ctx context.Context, in CreateTaskByPrincipalInput) (*taskmodel.Task, []uint64, error) {
	t, rs, err := a.TaskSvc.CreateByPrincipal(ctx, tasksvc.CreateByPrincipalInput{
		ChannelID:            in.ChannelID,
		CreatorPrincipalID:   in.CreatorPrincipalID,
		Title:                in.Title,
		Description:          in.Description,
		OutputSpecKind:       in.OutputSpecKind,
		IsLightweight:        in.IsLightweight,
		AssigneePrincipalID:  in.AssigneePrincipalID,
		ReviewerPrincipalIDs: in.ReviewerPrincipalIDs,
		RequiredApprovals:    in.RequiredApprovals,
	})
	if err != nil {
		return nil, nil, err
	}
	pids := make([]uint64, len(rs))
	for i, r := range rs {
		pids[i] = r.PrincipalID
	}
	return t, pids, nil
}

func (a *TaskAdapter) ClaimTaskByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*taskmodel.Task, error) {
	return a.TaskSvc.ClaimByPrincipal(ctx, taskID, callerPrincipalID)
}

func (a *TaskAdapter) SubmitTaskByPrincipal(ctx context.Context, in SubmitTaskByPrincipalInput) (*taskmodel.Task, *taskmodel.TaskSubmission, error) {
	return a.TaskSvc.SubmitByPrincipal(ctx, tasksvc.SubmitByPrincipalInput{
		TaskID:               in.TaskID,
		SubmitterPrincipalID: in.SubmitterPrincipalID,
		ContentKind:          in.ContentKind,
		Content:              in.Content,
		InlineSummary:        in.InlineSummary,
	})
}

func (a *TaskAdapter) ReviewTaskByPrincipal(ctx context.Context, in ReviewTaskByPrincipalInput) (*taskmodel.Task, *taskmodel.TaskReview, error) {
	return a.TaskSvc.ReviewByPrincipal(ctx, tasksvc.ReviewByPrincipalInput{
		TaskID:              in.TaskID,
		SubmissionID:        in.SubmissionID,
		ReviewerPrincipalID: in.ReviewerPrincipalID,
		Decision:            in.Decision,
		Comment:             in.Comment,
	})
}
