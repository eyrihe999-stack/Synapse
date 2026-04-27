// Package repository task 模块数据访问层。
//
// 一个 Repository interface 汇总所有方法,沿用 channel / organization 风格。
// 事务靠 WithTx 传同一个 gorm.DB 句柄。
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/task/model"
)

// Repository task 模块数据访问统一入口。
type Repository interface {
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ── Task ─────────────────────────────────────────────────────────────
	CreateTask(ctx context.Context, t *model.Task) error
	FindTaskByID(ctx context.Context, id uint64) (*model.Task, error)
	UpdateTaskFields(ctx context.Context, id uint64, updates map[string]any) error
	ListTasksByChannel(ctx context.Context, channelID uint64, status string, limit, offset int) ([]model.Task, error)
	// ListTasksByAssignees 派给任一 principal 的 task。
	// 一组 principal:caller 自己 + caller 是 user-agent 时的 owner user principal,
	// 让 user 的多个客户端 agent 都能看到派给 owner 的任务(P0.3)。
	ListTasksByAssignees(ctx context.Context, assigneePrincipalIDs []uint64, status string, limit, offset int) ([]model.Task, error)
	// ListTasksByReviewers 任一 principal 在 task_reviewers 白名单的 task。
	ListTasksByReviewers(ctx context.Context, reviewerPrincipalIDs []uint64, status string, limit, offset int) ([]model.Task, error)
	// ListTasksByAssigneesOrReviewers 派给任一 principal 的 + 任一 principal 作为 reviewer 的(去重)。
	// 用于 list_my_tasks role=either(默认)。
	ListTasksByAssigneesOrReviewers(ctx context.Context, principalIDs []uint64, status string, limit, offset int) ([]model.Task, error)

	// ── Reviewers ────────────────────────────────────────────────────────
	AddReviewers(ctx context.Context, taskID uint64, principalIDs []uint64) error
	ListReviewers(ctx context.Context, taskID uint64) ([]model.TaskReviewer, error)
	IsReviewer(ctx context.Context, taskID, principalID uint64) (bool, error)

	// ── Submissions ──────────────────────────────────────────────────────
	CreateSubmission(ctx context.Context, s *model.TaskSubmission) error
	FindSubmissionByID(ctx context.Context, id uint64) (*model.TaskSubmission, error)
	LatestSubmission(ctx context.Context, taskID uint64) (*model.TaskSubmission, error)
	ListSubmissions(ctx context.Context, taskID uint64) ([]model.TaskSubmission, error)

	// ── Reviews ──────────────────────────────────────────────────────────
	CreateReview(ctx context.Context, r *model.TaskReview) error
	ListReviewsBySubmission(ctx context.Context, submissionID uint64) ([]model.TaskReview, error)
	CountApprovalsForSubmission(ctx context.Context, submissionID uint64) (int64, error)

	// ── 跨模块轻量查询 ────────────────────────────────────────────────────
	// LookupUserPrincipalID 按 users.id 反查 principal_id(JWT sub → principal)。
	LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error)
	// LookupAgentOwnerUserPrincipalID 按 agent.principal_id 反查 owner user 的
	// principal_id —— 用于"派给 user 本人的任务,user 的个人 agent 也能看到/认领/提交"。
	// 如果 principal 不是 agent / 是 system agent / agent 无 owner_user_id,返 (0, nil)。
	LookupAgentOwnerUserPrincipalID(ctx context.Context, agentPrincipalID uint64) (uint64, error)
	// IsChannelMember 查 principal 是否是 channel 成员(跨 channel 模块查,
	// 避免引入 channel 模块 Go 依赖)。
	IsChannelMember(ctx context.Context, channelID, principalID uint64) (bool, error)
	// GetChannelMemberRole 拿 principal 在 channel 的 role(owner/member/observer);不在返 ""。
	GetChannelMemberRole(ctx context.Context, channelID, principalID uint64) (string, error)
	// ReplaceReviewers 事务内删旧 task_reviewers 行 + 插入新列表;保留 task_reviews 历史不动。
	ReplaceReviewers(ctx context.Context, taskID uint64, newPrincipalIDs []uint64) error
	// FindChannelOrgID 查 channel 所属 org_id(跨 channel 模块轻量 SELECT)。
	FindChannelOrgID(ctx context.Context, channelID uint64) (uint64, error)
	// FindChannelStatus 查 channel status(用于 task 状态机里校验 channel 未归档)。
	FindChannelStatus(ctx context.Context, channelID uint64) (string, error)
}

// gormRepository Repository 的 GORM 实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务,fn 接到事务内的 Repository;返错误自动回滚。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}

// ── Task ────────────────────────────────────────────────────────────────

func (r *gormRepository) CreateTask(ctx context.Context, t *model.Task) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

func (r *gormRepository) FindTaskByID(ctx context.Context, id uint64) (*model.Task, error) {
	var row model.Task
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find task: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) UpdateTaskFields(ctx context.Context, id uint64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	res := r.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update task fields: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *gormRepository) ListTasksByChannel(ctx context.Context, channelID uint64, status string, limit, offset int) ([]model.Task, error) {
	var rows []model.Task
	q := r.db.WithContext(ctx).Where("channel_id = ?", channelID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tasks by channel: %w", err)
	}
	return rows, nil
}

// ListTasksByAssignees 派给给定 principal 集合的 task。空集合返空。
func (r *gormRepository) ListTasksByAssignees(ctx context.Context, principalIDs []uint64, status string, limit, offset int) ([]model.Task, error) {
	if len(principalIDs) == 0 {
		return []model.Task{}, nil
	}
	var rows []model.Task
	q := r.db.WithContext(ctx).Where("assignee_principal_id IN ?", principalIDs)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tasks by assignees: %w", err)
	}
	return rows, nil
}

// ListTasksByReviewers 任一 principal 在 task_reviewers 白名单的 task。
// 多 principal 都可能是同一 task 的 reviewer,JOIN 后 SELECT DISTINCT 去重。
func (r *gormRepository) ListTasksByReviewers(ctx context.Context, principalIDs []uint64, status string, limit, offset int) ([]model.Task, error) {
	if len(principalIDs) == 0 {
		return []model.Task{}, nil
	}
	var rows []model.Task
	q := r.db.WithContext(ctx).
		Table("tasks").
		Distinct("tasks.*").
		Joins("INNER JOIN task_reviewers tr ON tr.task_id = tasks.id").
		Where("tr.principal_id IN ?", principalIDs)
	if status != "" {
		q = q.Where("tasks.status = ?", status)
	}
	if err := q.Order("tasks.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tasks by reviewers: %w", err)
	}
	return rows, nil
}

// ListTasksByAssigneesOrReviewers 去重 UNION:派给任一 principal 的 + 任一 principal 是 reviewer 的。
//
// 走 OR + 子查询(不用 GORM UNION,跨方言坑多)。task 表行数小,DISTINCT 性能可接受。
func (r *gormRepository) ListTasksByAssigneesOrReviewers(ctx context.Context, principalIDs []uint64, status string, limit, offset int) ([]model.Task, error) {
	if len(principalIDs) == 0 {
		return []model.Task{}, nil
	}
	var rows []model.Task
	q := r.db.WithContext(ctx).
		Table("tasks").
		Where("tasks.assignee_principal_id IN ? OR tasks.id IN (?)",
			principalIDs,
			r.db.Table("task_reviewers").Select("task_id").Where("principal_id IN ?", principalIDs))
	if status != "" {
		q = q.Where("tasks.status = ?", status)
	}
	if err := q.Order("tasks.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tasks by assignees-or-reviewers: %w", err)
	}
	return rows, nil
}

// ── Reviewers ───────────────────────────────────────────────────────────

func (r *gormRepository) AddReviewers(ctx context.Context, taskID uint64, principalIDs []uint64) error {
	if len(principalIDs) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(principalIDs))
	rows := make([]model.TaskReviewer, 0, len(principalIDs))
	for _, pid := range principalIDs {
		if pid == 0 {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		rows = append(rows, model.TaskReviewer{TaskID: taskID, PrincipalID: pid})
	}
	if len(rows) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return fmt.Errorf("add task reviewers: %w", err)
	}
	return nil
}

func (r *gormRepository) ListReviewers(ctx context.Context, taskID uint64) ([]model.TaskReviewer, error) {
	var rows []model.TaskReviewer
	if err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list task reviewers: %w", err)
	}
	return rows, nil
}

func (r *gormRepository) IsReviewer(ctx context.Context, taskID, principalID uint64) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.TaskReviewer{}).
		Where("task_id = ? AND principal_id = ?", taskID, principalID).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check task reviewer: %w", err)
	}
	return n > 0, nil
}

// ── Submissions ─────────────────────────────────────────────────────────

func (r *gormRepository) CreateSubmission(ctx context.Context, s *model.TaskSubmission) error {
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("create submission: %w", err)
	}
	return nil
}

func (r *gormRepository) FindSubmissionByID(ctx context.Context, id uint64) (*model.TaskSubmission, error) {
	var row model.TaskSubmission
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find submission: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) LatestSubmission(ctx context.Context, taskID uint64) (*model.TaskSubmission, error) {
	var row model.TaskSubmission
	err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("id DESC").
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("latest submission: %w", err)
	}
	return &row, nil
}

func (r *gormRepository) ListSubmissions(ctx context.Context, taskID uint64) ([]model.TaskSubmission, error) {
	var rows []model.TaskSubmission
	if err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	return rows, nil
}

// ── Reviews ─────────────────────────────────────────────────────────────

func (r *gormRepository) CreateReview(ctx context.Context, rv *model.TaskReview) error {
	if err := r.db.WithContext(ctx).Create(rv).Error; err != nil {
		return fmt.Errorf("create review: %w", err)
	}
	return nil
}

func (r *gormRepository) ListReviewsBySubmission(ctx context.Context, submissionID uint64) ([]model.TaskReview, error) {
	var rows []model.TaskReview
	if err := r.db.WithContext(ctx).
		Where("submission_id = ?", submissionID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list reviews by submission: %w", err)
	}
	return rows, nil
}

func (r *gormRepository) CountApprovalsForSubmission(ctx context.Context, submissionID uint64) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.TaskReview{}).
		Where("submission_id = ? AND decision = ?", submissionID, "approved").
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count approvals: %w", err)
	}
	return n, nil
}

// ── 跨模块 ─────────────────────────────────────────────────────────────

func (r *gormRepository) LookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).
		Table("users").
		Where("id = ?", userID).
		Select("principal_id").
		Scan(&pid).Error
	if err != nil {
		return 0, fmt.Errorf("lookup user principal: %w", err)
	}
	return pid, nil
}

// LookupAgentOwnerUserPrincipalID:agent.principal_id → owner_user_id → users.principal_id。
// 返 0 的三种情况:
//   - principal 不是 agent(是 user / 不存在)
//   - agent 是 system kind(owner_user_id NULL)
//   - 历史脏数据(owner_user_id 指向不存在的 user)
//
// 都返 (0, nil) 让调用方用单一逻辑"0 表示无 owner,跳过 union"。
func (r *gormRepository) LookupAgentOwnerUserPrincipalID(ctx context.Context, agentPrincipalID uint64) (uint64, error) {
	var pid uint64
	err := r.db.WithContext(ctx).Raw(`
		SELECT u.principal_id
		FROM agents a
		JOIN users u ON u.id = a.owner_user_id
		WHERE a.principal_id = ?
		  AND a.kind = 'user'
		  AND a.owner_user_id IS NOT NULL
		LIMIT 1
	`, agentPrincipalID).Scan(&pid).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lookup agent owner principal: %w", err)
	}
	return pid, nil
}

func (r *gormRepository) IsChannelMember(ctx context.Context, channelID, principalID uint64) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Table("channel_members").
		Where("channel_id = ? AND principal_id = ?", channelID, principalID).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check channel member: %w", err)
	}
	return n > 0, nil
}

// GetChannelMemberRole 返回 principal 在 channel 里的 role,非成员返 "" + nil(不报错)。
// 给 task service 判断 "actor 是否 channel owner" 用(变更 assignee / reviewers 的权限判定)。
func (r *gormRepository) GetChannelMemberRole(ctx context.Context, channelID, principalID uint64) (string, error) {
	var role string
	err := r.db.WithContext(ctx).
		Table("channel_members").
		Where("channel_id = ? AND principal_id = ?", channelID, principalID).
		Select("role").
		Scan(&role).Error
	if err != nil {
		return "", fmt.Errorf("get channel member role: %w", err)
	}
	return role, nil
}

// ReplaceReviewers 事务内替换 task 的 reviewer 列表:删除所有现有 task_reviewers 行,
// 插入 newPrincipalIDs(去重过)。保留 task_reviews 历史投票行不动 —— 审计用,
// 当前判定按新 reviewer 列表 + 现存投票中仍在新列表内的部分重新计数(由 service 层处理)。
func (r *gormRepository) ReplaceReviewers(ctx context.Context, taskID uint64, newPrincipalIDs []uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ?", taskID).Delete(&model.TaskReviewer{}).Error; err != nil {
			return fmt.Errorf("clear reviewers: %w", err)
		}
		if len(newPrincipalIDs) == 0 {
			return nil
		}
		seen := make(map[uint64]struct{}, len(newPrincipalIDs))
		rows := make([]model.TaskReviewer, 0, len(newPrincipalIDs))
		for _, pid := range newPrincipalIDs {
			if pid == 0 {
				continue
			}
			if _, dup := seen[pid]; dup {
				continue
			}
			seen[pid] = struct{}{}
			rows = append(rows, model.TaskReviewer{TaskID: taskID, PrincipalID: pid})
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("insert reviewers: %w", err)
		}
		return nil
	})
}

func (r *gormRepository) FindChannelOrgID(ctx context.Context, channelID uint64) (uint64, error) {
	var org uint64
	err := r.db.WithContext(ctx).
		Table("channels").
		Where("id = ?", channelID).
		Select("org_id").
		Scan(&org).Error
	if err != nil {
		return 0, fmt.Errorf("find channel org_id: %w", err)
	}
	return org, nil
}

func (r *gormRepository) FindChannelStatus(ctx context.Context, channelID uint64) (string, error) {
	var status string
	err := r.db.WithContext(ctx).
		Table("channels").
		Where("id = ?", channelID).
		Select("status").
		Scan(&status).Error
	if err != nil {
		return "", fmt.Errorf("find channel status: %w", err)
	}
	return status, nil
}
