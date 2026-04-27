// Package service task 业务层。
//
// 权限模型:
//   - Create / Get / ListByChannel / Cancel:channel member
//   - Claim / Submit:assignee(principal)或其 owner user(当 assignee 指向 user
//     时,其所有个人 agent 都可代为 claim/submit;反向同理)
//   - Review:task_reviewers 白名单内
//
// 事件:service 层在 DB commit 后 best-effort XADD 到 synapse:task:events;失败
// 只 warn,DB 是真相源(消费者失活也能从 DB 拉兜底)。
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	taskerr "github.com/eyrihe999-stack/Synapse/internal/task"
	"github.com/eyrihe999-stack/Synapse/internal/task/model"
	"github.com/eyrihe999-stack/Synapse/internal/task/repository"
)

// Config 构造 Service 所需配置。
type Config struct {
	// OSSPathPrefix OSS 顶层前缀,一般取 "synapse"(对齐 documents 的约定,
	// 由 cmd/synapse 从 config.OSS.PathPrefix 传入)。
	OSSPathPrefix string
	// TaskEventStream 事件 stream key(XADD 目标)。空串 = 不发事件(单测场景)。
	TaskEventStream string
}

// Service task 模块门面。
type Service struct {
	Task TaskService
}

// New 构造 Service。publisher / oss 可 nil(单测降级);主路径二者都必须就位。
func New(cfg Config, repo repository.Repository, oss ossupload.Client, publisher eventbus.Publisher, log logger.LoggerInterface) *Service {
	return &Service{
		Task: newTaskService(cfg, repo, oss, publisher, log),
	}
}

// TaskService 对外接口。
//
// HTTP 路径以 *UserID* 为入参(JWT 里的 users.id);MCP 路径以 *PrincipalID* 为
// 入参(agent principal)。两者核心逻辑通过 *ByPrincipal 方法共享 —— User 变体
// 只多做一次 user.id → principal_id 反查。
type TaskService interface {
	Create(ctx context.Context, in CreateInput) (*model.Task, []model.TaskReviewer, error)
	Get(ctx context.Context, taskID, callerUserID uint64) (*TaskDetail, error)
	ListByChannel(ctx context.Context, channelID, callerUserID uint64, status string, limit, offset int) ([]model.Task, error)
	ListMy(ctx context.Context, callerUserID uint64, status string, limit, offset int) ([]model.Task, error)
	Claim(ctx context.Context, taskID, callerUserID uint64) (*model.Task, error)
	Submit(ctx context.Context, in SubmitInput) (*model.Task, *model.TaskSubmission, error)
	Review(ctx context.Context, in ReviewInput) (*model.Task, *model.TaskReview, error)
	Cancel(ctx context.Context, taskID, callerUserID uint64) (*model.Task, error)

	// ── 变更 assignee / reviewers(PR 方案 A)──
	//
	// 权限:actor 必须是 task 创建人或 task 所在 channel 的 owner。
	// 状态限制:
	//   - UpdateAssignee:任何非终态(open/assigned/in_progress/submitted/changes_requested)都允许
	//   - UpdateReviewers:只在 submitted 之前允许(open/assigned/in_progress),提交后不改
	//     避免和已在飞的审批流冲突
	UpdateAssignee(ctx context.Context, taskID, callerUserID, newAssigneePrincipalID uint64) (*model.Task, error)
	UpdateReviewers(ctx context.Context, taskID, callerUserID uint64, newReviewerPrincipalIDs []uint64, newRequiredApprovals int) (*model.Task, []uint64, error)

	// ── by principal(MCP 用)── 跳过 user_id → principal_id 反查,直接以
	// caller 的 principal 做权限检查。submitter 只支持"自己 = assignee";
	// MVP 不做 agent-代-user submit(见实施后记)
	CreateByPrincipal(ctx context.Context, in CreateByPrincipalInput) (*model.Task, []model.TaskReviewer, error)
	GetByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*TaskDetail, error)
	ListByAssigneePrincipal(ctx context.Context, assigneePrincipalID uint64, status string, limit, offset int) ([]model.Task, error)
	// ListMyTasksByPrincipal 按 role 列 caller 关心的 task。role 取值:
	//   - "" / "either":派给我的 + 我作为 reviewer 待审的(去重)
	//   - "assignee":只列派给我的
	//   - "reviewer":只列我作为 reviewer 的
	//
	// 不会反查 owner user 的任务,owner 反查由 P0.3 在调用端拼 candidate
	// principal id 列表后调下层 repo 方法实现。
	ListMyTasksByPrincipal(ctx context.Context, callerPrincipalID uint64, role, status string, limit, offset int) ([]model.Task, error)
	ClaimByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*model.Task, error)
	SubmitByPrincipal(ctx context.Context, in SubmitByPrincipalInput) (*model.Task, *model.TaskSubmission, error)
	ReviewByPrincipal(ctx context.Context, in ReviewByPrincipalInput) (*model.Task, *model.TaskReview, error)
}

// CreateByPrincipalInput Create 的 by-principal 变体参数。
//
// InitiatorPrincipalID 代派语义:非 0 且 != CreatorPrincipalID 时,视作"agent 代
// 人派任务"——task 的 created_by 记 Initiator(发起人),created_via 记 Creator
// (代派的 agent);reviewer 列表为空时自动 fallback 为 [Initiator]。等于 0 或
// 等于 Creator 时走老语义(手动创建,created_by = caller,created_via = 0)。
//
// IsLightweight=true 表示"轻量任务":submit 不需要文件,只走 inline_summary。
// OutputSpecKind 在 IsLightweight=true 时仍要填(用于前端展示),但 submit 校验
// 只看 IsLightweight。
type CreateByPrincipalInput struct {
	ChannelID             uint64
	CreatorPrincipalID    uint64
	InitiatorPrincipalID  uint64 // 0 = 非代派
	Title                 string
	Description           string
	OutputSpecKind        string
	IsLightweight         bool
	AssigneePrincipalID   uint64
	ReviewerPrincipalIDs  []uint64
	RequiredApprovals     int
}

// SubmitByPrincipalInput Submit by-principal 变体。
type SubmitByPrincipalInput struct {
	TaskID               uint64
	SubmitterPrincipalID uint64
	ContentKind          string
	Content              []byte
	InlineSummary        string
}

// ReviewByPrincipalInput Review by-principal 变体。
type ReviewByPrincipalInput struct {
	TaskID              uint64
	SubmissionID        uint64
	ReviewerPrincipalID uint64
	Decision            string
	Comment             string
}

// CreateInput 创建 task 的参数。
type CreateInput struct {
	ChannelID             uint64
	CreatorUserID         uint64
	Title                 string
	Description           string
	OutputSpecKind        string
	IsLightweight         bool     // true 时 submit 走 inline_summary,不要文件
	AssigneePrincipalID   uint64   // 0 表示 open 状态等人认领
	ReviewerPrincipalIDs  []uint64 // 必须和 RequiredApprovals 配合:RequiredApprovals <= len
	RequiredApprovals     int      // 0 自动填 1
}

// SubmitInput 提交产物参数。
type SubmitInput struct {
	TaskID          uint64
	SubmitterUserID uint64
	ContentKind     string // 必须和 task.output_spec_kind 一致
	Content         []byte // 单文件 bytes,≤ MaxSubmissionByteSize
	InlineSummary   string
}

// ReviewInput 审批参数。
type ReviewInput struct {
	TaskID          uint64
	SubmissionID    uint64
	ReviewerUserID  uint64
	Decision        string // approved / request_changes / rejected
	Comment         string
}

// TaskDetail Get 返回的完整视图。
type TaskDetail struct {
	Task        model.Task
	Reviewers   []uint64 // principal_id 列表
	Submissions []model.TaskSubmission
	Reviews     []model.TaskReview // 所有 submission 的所有 review 合并;前端按 submission_id 分组
}

type taskService struct {
	cfg       Config
	repo      repository.Repository
	oss       ossupload.Client
	publisher eventbus.Publisher
	logger    logger.LoggerInterface
}

func newTaskService(cfg Config, repo repository.Repository, oss ossupload.Client, publisher eventbus.Publisher, log logger.LoggerInterface) TaskService {
	return &taskService{cfg: cfg, repo: repo, oss: oss, publisher: publisher, logger: log}
}

// ── By principal(MCP 用)────────────────────────────────────────────────

// CreateByPrincipal Create 的核心 —— 跳过 user→principal 反查。
func (s *taskService) CreateByPrincipal(ctx context.Context, in CreateByPrincipalInput) (*model.Task, []model.TaskReviewer, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > taskerr.TitleMaxLen {
		return nil, nil, taskerr.ErrTaskTitleInvalid
	}
	if len(in.Description) > taskerr.DescriptionMaxLen {
		return nil, nil, taskerr.ErrTaskDescriptionInvalid
	}
	if !taskerr.IsValidOutputKind(in.OutputSpecKind) {
		return nil, nil, taskerr.ErrTaskOutputKindInvalid
	}
	reviewers := dedupPrincipalIDs(in.ReviewerPrincipalIDs)

	// 自动反查代派:caller 是 user-agent(agents.kind='user' + owner_user_id)且
	// 没有显式传 Initiator 时,自动以 owner_user_principal_id 作 Initiator ——
	// 表达"agent 代它的 owner 派任务"语义。
	// 系统 agent / user 直接走 web JWT 时 owner=0,不变。
	initiatorPID := in.InitiatorPrincipalID
	if initiatorPID == 0 && in.CreatorPrincipalID != 0 {
		ownerPID, err := s.repo.LookupAgentOwnerUserPrincipalID(ctx, in.CreatorPrincipalID)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup creator owner: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if ownerPID != 0 && ownerPID != in.CreatorPrincipalID {
			initiatorPID = ownerPID
		}
	}

	// 代派 / 手动 分叉:
	//   - 代派(Initiator 非 0 且 != Creator):created_by=Initiator / created_via=Creator;
	//     reviewer 空 → fallback = [Initiator];required_approvals 自动 clamp 到 [0, len]
	//     (LLM 乱传不报错,保证任务能进可执行状态)。
	//   - 手动(Initiator=0 或 =Creator):created_by=Creator / created_via=0;
	//     required_approvals 越界保留原有 ErrRequiredApprovalsRange 报错(严格校验)。
	createdByPID := in.CreatorPrincipalID
	createdViaPID := uint64(0)
	isDelegated := initiatorPID != 0 && initiatorPID != in.CreatorPrincipalID
	if isDelegated {
		createdByPID = initiatorPID
		createdViaPID = in.CreatorPrincipalID
		// reviewer 空时 fallback 到 [Initiator] —— agent 代派的活默认让 owner 把关
		// (防 prompt injection 滥派)。
		// 但 corner case:assignee==initiator(agent 代 owner 派给 owner 自己)
		// 跳过 fallback —— owner 没必要审自己的活。
		if len(reviewers) == 0 && in.AssigneePrincipalID != initiatorPID {
			reviewers = []uint64{initiatorPID}
		}
	}

	// required_approvals 默认策略:
	//   - 显式传 >0 → 使用该值(代派下若越界 clamp;手动下越界报错)
	//   - 未传(<=0) 且有 reviewer → 自动 1
	//   - 未传(<=0) 且无 reviewer → 0(无需审批,submit 即完成)
	requiredApprovals := in.RequiredApprovals
	if requiredApprovals <= 0 {
		if len(reviewers) > 0 {
			requiredApprovals = 1
		} else {
			requiredApprovals = 0
		}
	}
	if requiredApprovals > len(reviewers) {
		if isDelegated {
			requiredApprovals = len(reviewers) // clamp,不打断 agent loop
		} else {
			return nil, nil, taskerr.ErrRequiredApprovalsRange
		}
	}

	orgID, err := s.channelOrgID(ctx, in.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireChannelOpen(ctx, in.ChannelID); err != nil {
		return nil, nil, err
	}
	if in.CreatorPrincipalID == 0 {
		return nil, nil, taskerr.ErrForbidden
	}
	if err := s.requireChannelMember(ctx, in.ChannelID, in.CreatorPrincipalID); err != nil {
		return nil, nil, err
	}
	if in.AssigneePrincipalID != 0 {
		ok, err := s.repo.IsChannelMember(ctx, in.ChannelID, in.AssigneePrincipalID)
		if err != nil {
			return nil, nil, fmt.Errorf("check assignee member: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, nil, taskerr.ErrAssigneeNotInChannel
		}
	}
	for _, rid := range reviewers {
		ok, err := s.repo.IsChannelMember(ctx, in.ChannelID, rid)
		if err != nil {
			return nil, nil, fmt.Errorf("check reviewer member: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, nil, taskerr.ErrReviewerNotInChannel
		}
	}

	var (
		createdTask      model.Task
		createdReviewers []model.TaskReviewer
	)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		t := &model.Task{
			OrgID:                 orgID,
			ChannelID:             in.ChannelID,
			Title:                 title,
			Description:           in.Description,
			CreatedByPrincipalID:  createdByPID,
			CreatedViaPrincipalID: createdViaPID,
			AssigneePrincipalID:   in.AssigneePrincipalID,
			Status:                taskerr.StatusOpen,
			OutputSpecKind:        in.OutputSpecKind,
			IsLightweight:         in.IsLightweight,
			RequiredApprovals:     requiredApprovals,
		}
		if err := tx.CreateTask(ctx, t); err != nil {
			return err
		}
		if err := tx.AddReviewers(ctx, t.ID, reviewers); err != nil {
			return err
		}
		createdTask = *t
		createdReviewers, _ = tx.ListReviewers(ctx, t.ID)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create task tx: %w: %w", err, taskerr.ErrTaskInternal)
	}

	s.publishTaskEvent(ctx, "task.created", &createdTask, map[string]any{
		"assignee_principal_id": strconv.FormatUint(createdTask.AssigneePrincipalID, 10),
		"reviewer_count":        strconv.Itoa(len(createdReviewers)),
	})
	return &createdTask, createdReviewers, nil
}

// GetByPrincipal Get 核心。
func (s *taskService) GetByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*TaskDetail, error) {
	if callerPrincipalID == 0 {
		return nil, taskerr.ErrForbidden
	}
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.requireChannelMember(ctx, t.ChannelID, callerPrincipalID); err != nil {
		return nil, err
	}
	reviewerRows, err := s.repo.ListReviewers(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list reviewers: %w: %w", err, taskerr.ErrTaskInternal)
	}
	subs, err := s.repo.ListSubmissions(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w: %w", err, taskerr.ErrTaskInternal)
	}
	var allReviews []model.TaskReview
	for _, sb := range subs {
		rs, err := s.repo.ListReviewsBySubmission(ctx, sb.ID)
		if err != nil {
			return nil, fmt.Errorf("list reviews: %w: %w", err, taskerr.ErrTaskInternal)
		}
		allReviews = append(allReviews, rs...)
	}
	reviewerPIDs := make([]uint64, len(reviewerRows))
	for i, r := range reviewerRows {
		reviewerPIDs[i] = r.PrincipalID
	}
	return &TaskDetail{Task: *t, Reviewers: reviewerPIDs, Submissions: subs, Reviews: allReviews}, nil
}

// ListByAssigneePrincipal 直接 by principal 列 task(老接口,只列 assignee 视角)。
// 新代码(MCP list_my_tasks)请用 ListMyTasksByPrincipal,支持 reviewer 视角 + owner 反查。
func (s *taskService) ListByAssigneePrincipal(ctx context.Context, assigneePrincipalID uint64, status string, limit, offset int) ([]model.Task, error) {
	if assigneePrincipalID == 0 {
		return nil, taskerr.ErrForbidden
	}
	if limit <= 0 {
		limit = taskerr.ListDefaultLimit
	}
	if limit > taskerr.ListMaxLimit {
		limit = taskerr.ListMaxLimit
	}
	return s.repo.ListTasksByAssignees(ctx, []uint64{assigneePrincipalID}, status, limit, offset)
}

// ListMyTasksByPrincipal 按 role 列 caller 关心的 task。详见接口注释。
//
// owner 反查:caller 是 user-agent 时,自动把 owner user 的 principal_id 加进
// candidate 列表,让 user 的多个客户端 agent 都能看到/认领派给 user 本人的任务。
func (s *taskService) ListMyTasksByPrincipal(ctx context.Context, callerPrincipalID uint64, role, status string, limit, offset int) ([]model.Task, error) {
	if callerPrincipalID == 0 {
		return nil, taskerr.ErrForbidden
	}
	if limit <= 0 {
		limit = taskerr.ListDefaultLimit
	}
	if limit > taskerr.ListMaxLimit {
		limit = taskerr.ListMaxLimit
	}
	candidates, err := s.expandCallerCandidates(ctx, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	switch role {
	case "", "either":
		return s.repo.ListTasksByAssigneesOrReviewers(ctx, candidates, status, limit, offset)
	case "assignee":
		return s.repo.ListTasksByAssignees(ctx, candidates, status, limit, offset)
	case "reviewer":
		return s.repo.ListTasksByReviewers(ctx, candidates, status, limit, offset)
	default:
		return nil, fmt.Errorf("invalid role %q: %w", role, taskerr.ErrTaskRoleInvalid)
	}
}

// expandCallerCandidates 把 caller principal 扩展成 [caller, ownerUserPrincipal]
// (caller 是 user-agent 时;否则就一个 caller)。
//
// 用于 list_my_tasks / claim / submit 三处:让 user 的个人 agent 在权限校验上
// 等价于 user 本人。系统 agent / user 直接登录时无 owner,只返 caller 自己。
func (s *taskService) expandCallerCandidates(ctx context.Context, callerPrincipalID uint64) ([]uint64, error) {
	ownerPID, err := s.repo.LookupAgentOwnerUserPrincipalID(ctx, callerPrincipalID)
	if err != nil {
		return nil, fmt.Errorf("lookup owner principal: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if ownerPID == 0 || ownerPID == callerPrincipalID {
		return []uint64{callerPrincipalID}, nil
	}
	return []uint64{callerPrincipalID, ownerPID}, nil
}

// ClaimByPrincipal Claim 核心。
//
// 放宽校验:caller 是 user-agent 时,允许认领 assignee=owner_user 的任务(把 user
// 已派的活让 user 自己的 Claude / Cursor 接手)。认领后 assignee_principal_id 改为
// caller 本人 —— 后续 submit 校验只看 caller == assignee 即可。
func (s *taskService) ClaimByPrincipal(ctx context.Context, taskID, callerPrincipalID uint64) (*model.Task, error) {
	if callerPrincipalID == 0 {
		return nil, taskerr.ErrForbidden
	}
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != taskerr.StatusOpen && t.Status != taskerr.StatusInProgress {
		return nil, taskerr.ErrTaskStateTransition
	}
	if err := s.requireChannelMember(ctx, t.ChannelID, callerPrincipalID); err != nil {
		return nil, err
	}
	candidates, err := s.expandCallerCandidates(ctx, callerPrincipalID)
	if err != nil {
		return nil, err
	}
	if t.AssigneePrincipalID != 0 && !uint64InSlice(candidates, t.AssigneePrincipalID) {
		return nil, taskerr.ErrTaskAlreadyClaimed
	}
	updates := map[string]any{
		"status":                taskerr.StatusInProgress,
		"assignee_principal_id": callerPrincipalID,
	}
	if err := s.repo.UpdateTaskFields(ctx, t.ID, updates); err != nil {
		return nil, fmt.Errorf("claim task: %w: %w", err, taskerr.ErrTaskInternal)
	}
	t.Status = taskerr.StatusInProgress
	t.AssigneePrincipalID = callerPrincipalID
	s.publishTaskEvent(ctx, "task.claimed", t, map[string]any{
		"actor_principal_id":    strconv.FormatUint(callerPrincipalID, 10),
		"assignee_principal_id": strconv.FormatUint(callerPrincipalID, 10),
	})
	return t, nil
}

// SubmitByPrincipal Submit 核心。详见接口注释("agent 代 owner user 提交"放宽)。
//
// 轻量任务(task.is_lightweight=true):content 可空 + 不上 OSS,inline_summary 必填。
// reviewer 看的是 inline_summary 而不是 OSS 文档。
func (s *taskService) SubmitByPrincipal(ctx context.Context, in SubmitByPrincipalInput) (*model.Task, *model.TaskSubmission, error) {
	if in.SubmitterPrincipalID == 0 {
		return nil, nil, taskerr.ErrForbidden
	}

	t, err := s.loadTask(ctx, in.TaskID)
	if err != nil {
		return nil, nil, err
	}
	// submit 允许:in_progress(首次)或 revision_requested(打回后重交)。
	// revision_requested 二次 submit 生成新 task_submission 行,旧 submission 保留作审计。
	if t.Status != taskerr.StatusInProgress && t.Status != taskerr.StatusRevisionRequested {
		return nil, nil, taskerr.ErrTaskStateTransition
	}

	// 放宽:submitter 是 user-agent 时,可代 owner user 提交派给 owner 的任务。
	// 系统 agent / user 直接登录时 candidates 只含自己,等价于原来的"submitter==assignee"。
	candidates, err := s.expandCallerCandidates(ctx, in.SubmitterPrincipalID)
	if err != nil {
		return nil, nil, err
	}
	if !uint64InSlice(candidates, t.AssigneePrincipalID) {
		return nil, nil, taskerr.ErrForbidden
	}

	// 校验分两路:lightweight 任务 vs 普通任务。
	if t.IsLightweight {
		// 轻量任务:不要文件,inline_summary 是产物。
		if strings.TrimSpace(in.InlineSummary) == "" {
			return nil, nil, taskerr.ErrSubmissionEmpty
		}
		if len(in.InlineSummary) > taskerr.MaxInlineSummaryLen {
			return nil, nil, taskerr.ErrSubmissionTooLarge
		}
		if len(in.Content) > 0 {
			// 拒绝混淆:lightweight 模式不允许传 content,避免日后两条路径混用产生歧义。
			return nil, nil, taskerr.ErrTaskLightweightHasContent
		}
	} else {
		if len(in.Content) == 0 {
			return nil, nil, taskerr.ErrSubmissionEmpty
		}
		if int64(len(in.Content)) > taskerr.MaxSubmissionByteSize {
			return nil, nil, taskerr.ErrSubmissionTooLarge
		}
		if !taskerr.IsValidOutputKind(in.ContentKind) {
			return nil, nil, taskerr.ErrTaskOutputKindInvalid
		}
		if in.ContentKind != t.OutputSpecKind {
			return nil, nil, taskerr.ErrSubmissionContentKind
		}
	}

	// OSS 上传仅普通任务路径。
	var ossKey string
	var byteSize int64
	if !t.IsLightweight {
		ext := taskerr.ExtensionForOutputKind(t.OutputSpecKind)
		ossKey = s.buildSubmissionOSSKey(t.OrgID, t.ID, ext)
		contentType := "text/plain"
		if t.OutputSpecKind == taskerr.OutputKindMarkdown {
			contentType = "text/markdown; charset=utf-8"
		}
		if s.oss == nil {
			return nil, nil, fmt.Errorf("oss client not configured: %w", taskerr.ErrTaskInternal)
		}
		if _, err := s.oss.PutObject(ctx, ossKey, in.Content, contentType); err != nil {
			return nil, nil, fmt.Errorf("oss put: %w: %w", err, taskerr.ErrTaskInternal)
		}
		byteSize = int64(len(in.Content))
	}

	// content_kind 落库:轻量任务统一记 'none',方便前端 / 归档逻辑判断。
	contentKind := in.ContentKind
	if t.IsLightweight {
		contentKind = taskerr.OutputKindNone
	}

	var (
		submission  model.TaskSubmission
		updatedTask model.Task
	)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		sub := &model.TaskSubmission{
			TaskID:               t.ID,
			SubmitterPrincipalID: in.SubmitterPrincipalID,
			ContentKind:          contentKind,
			OSSKey:               ossKey,
			ByteSize:             byteSize,
			InlineSummary:        in.InlineSummary,
		}
		if err := tx.CreateSubmission(ctx, sub); err != nil {
			return err
		}
		submission = *sub
		now := time.Now().UTC()
		// 无审批任务(required_approvals=0)submit 即 approved + closed,
		// 对齐 HTTP Submit 路径行为。否则走 submitted 等审批。
		status := taskerr.StatusSubmitted
		updates := map[string]any{
			"status":       status,
			"submitted_at": now,
		}
		if t.RequiredApprovals == 0 {
			status = taskerr.StatusApproved
			updates["status"] = status
			updates["closed_at"] = now
		}
		if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
			return err
		}
		updatedTask = *t
		updatedTask.Status = status
		updatedTask.SubmittedAt = &now
		if status == taskerr.StatusApproved {
			updatedTask.ClosedAt = &now
		}
		return nil
	})
	if err != nil {
		if ossKey != "" {
			if delErr := s.oss.DeleteObject(ctx, ossKey); delErr != nil {
				s.logger.WarnCtx(ctx, "task: orphan OSS object not cleaned", map[string]any{
					"key": ossKey, "err": delErr.Error(),
				})
			}
		}
		return nil, nil, fmt.Errorf("submit tx: %w: %w", err, taskerr.ErrTaskInternal)
	}
	s.publishTaskEvent(ctx, "task.submitted", &updatedTask, map[string]any{
		"actor_principal_id":  strconv.FormatUint(in.SubmitterPrincipalID, 10),
		"submission_id":       strconv.FormatUint(submission.ID, 10),
		"submitter_principal": strconv.FormatUint(in.SubmitterPrincipalID, 10),
	})
	return &updatedTask, &submission, nil
}

// ReviewByPrincipal Review 核心。
func (s *taskService) ReviewByPrincipal(ctx context.Context, in ReviewByPrincipalInput) (*model.Task, *model.TaskReview, error) {
	if !taskerr.IsValidDecision(in.Decision) {
		return nil, nil, taskerr.ErrDecisionInvalid
	}
	if len(in.Comment) > taskerr.CommentMaxLen {
		return nil, nil, taskerr.ErrTaskDescriptionInvalid
	}
	if in.ReviewerPrincipalID == 0 {
		return nil, nil, taskerr.ErrForbidden
	}

	t, err := s.loadTask(ctx, in.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if t.Status != taskerr.StatusSubmitted {
		return nil, nil, taskerr.ErrTaskStateTransition
	}
	sub, err := s.repo.FindSubmissionByID(ctx, in.SubmissionID)
	if err != nil {
		return nil, nil, fmt.Errorf("find submission: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if sub == nil || sub.TaskID != t.ID {
		return nil, nil, taskerr.ErrSubmissionNotFound
	}
	ok, err := s.repo.IsReviewer(ctx, t.ID, in.ReviewerPrincipalID)
	if err != nil {
		return nil, nil, fmt.Errorf("check reviewer: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if !ok {
		return nil, nil, taskerr.ErrForbidden
	}

	var (
		review      model.TaskReview
		updatedTask = *t
	)
	now := time.Now().UTC()
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		rv := &model.TaskReview{
			TaskID:              t.ID,
			SubmissionID:        sub.ID,
			ReviewerPrincipalID: in.ReviewerPrincipalID,
			Decision:            in.Decision,
			Comment:             in.Comment,
		}
		if err := tx.CreateReview(ctx, rv); err != nil {
			if isUniqueViolation(err) {
				return taskerr.ErrReviewerDuplicate
			}
			return err
		}
		review = *rv
		switch in.Decision {
		case taskerr.DecisionRejected:
			updates := map[string]any{"status": taskerr.StatusRejected, "closed_at": now}
			if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
				return err
			}
			updatedTask.Status = taskerr.StatusRejected
			updatedTask.ClosedAt = &now
		case taskerr.DecisionRequestChanges:
			updates := map[string]any{"status": taskerr.StatusRevisionRequested}
			if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
				return err
			}
			updatedTask.Status = taskerr.StatusRevisionRequested
		case taskerr.DecisionApproved:
			count, err := tx.CountApprovalsForSubmission(ctx, sub.ID)
			if err != nil {
				return err
			}
			if count >= int64(t.RequiredApprovals) {
				updates := map[string]any{"status": taskerr.StatusApproved, "closed_at": now}
				if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
					return err
				}
				updatedTask.Status = taskerr.StatusApproved
				updatedTask.ClosedAt = &now
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("review tx: %w: %w", err, taskerr.ErrTaskInternal)
	}
	s.publishTaskEvent(ctx, "task.reviewed", &updatedTask, map[string]any{
		"actor_principal_id":    strconv.FormatUint(in.ReviewerPrincipalID, 10),
		"submission_id":         strconv.FormatUint(sub.ID, 10),
		"reviewer_principal_id": strconv.FormatUint(in.ReviewerPrincipalID, 10),
		"decision":              in.Decision,
	})
	return &updatedTask, &review, nil
}

// ── Create ──────────────────────────────────────────────────────────────

func (s *taskService) Create(ctx context.Context, in CreateInput) (*model.Task, []model.TaskReviewer, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > taskerr.TitleMaxLen {
		return nil, nil, taskerr.ErrTaskTitleInvalid
	}
	if len(in.Description) > taskerr.DescriptionMaxLen {
		return nil, nil, taskerr.ErrTaskDescriptionInvalid
	}
	if !taskerr.IsValidOutputKind(in.OutputSpecKind) {
		return nil, nil, taskerr.ErrTaskOutputKindInvalid
	}
	reviewers := dedupPrincipalIDs(in.ReviewerPrincipalIDs)
	// required_approvals 默认策略:
	//   - 显式传 >0 → 使用该值
	//   - 未传(<=0) 且有 reviewer → 自动 1(至少 1 人通过即结)
	//   - 未传(<=0) 且无 reviewer → 0(任务无需审批,submit 即完成)
	requiredApprovals := in.RequiredApprovals
	if requiredApprovals <= 0 {
		if len(reviewers) > 0 {
			requiredApprovals = 1
		} else {
			requiredApprovals = 0
		}
	}
	if requiredApprovals > len(reviewers) {
		return nil, nil, taskerr.ErrRequiredApprovalsRange
	}

	orgID, err := s.channelOrgID(ctx, in.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireChannelOpen(ctx, in.ChannelID); err != nil {
		return nil, nil, err
	}

	creatorPrincipalID, err := s.lookupUserPrincipalID(ctx, in.CreatorUserID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireChannelMember(ctx, in.ChannelID, creatorPrincipalID); err != nil {
		return nil, nil, err
	}

	// Assignee / reviewers 必须都是 channel 成员(系统 agent 自动加入也算成员)
	if in.AssigneePrincipalID != 0 {
		ok, err := s.repo.IsChannelMember(ctx, in.ChannelID, in.AssigneePrincipalID)
		if err != nil {
			return nil, nil, fmt.Errorf("check assignee member: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, nil, taskerr.ErrAssigneeNotInChannel
		}
	}
	for _, rid := range reviewers {
		ok, err := s.repo.IsChannelMember(ctx, in.ChannelID, rid)
		if err != nil {
			return nil, nil, fmt.Errorf("check reviewer member: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, nil, taskerr.ErrReviewerNotInChannel
		}
	}

	status := taskerr.StatusOpen
	if in.AssigneePrincipalID == 0 {
		// 没指派 → open 状态等人 claim
		status = taskerr.StatusOpen
	}

	var (
		createdTask      model.Task
		createdReviewers []model.TaskReviewer
	)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		t := &model.Task{
			OrgID:                 orgID,
			ChannelID:             in.ChannelID,
			Title:                 title,
			Description:           in.Description,
			CreatedByPrincipalID:  creatorPrincipalID,
			CreatedViaPrincipalID: 0, // HTTP 路径 = 手动创建
			AssigneePrincipalID:   in.AssigneePrincipalID,
			Status:                status,
			OutputSpecKind:        in.OutputSpecKind,
			IsLightweight:         in.IsLightweight,
			RequiredApprovals:     requiredApprovals,
		}
		if err := tx.CreateTask(ctx, t); err != nil {
			return err
		}
		if err := tx.AddReviewers(ctx, t.ID, reviewers); err != nil {
			return err
		}
		createdTask = *t
		createdReviewers, _ = tx.ListReviewers(ctx, t.ID)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create task tx: %w: %w", err, taskerr.ErrTaskInternal)
	}

	s.publishTaskEvent(ctx, "task.created", &createdTask, map[string]any{
		"assignee_principal_id": strconv.FormatUint(createdTask.AssigneePrincipalID, 10),
		"reviewer_count":        strconv.Itoa(len(createdReviewers)),
	})
	return &createdTask, createdReviewers, nil
}

// ── Get / List ──────────────────────────────────────────────────────────

func (s *taskService) Get(ctx context.Context, taskID, callerUserID uint64) (*TaskDetail, error) {
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	if err := s.requireChannelMember(ctx, t.ChannelID, callerPrincipalID); err != nil {
		return nil, err
	}

	reviewerRows, err := s.repo.ListReviewers(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list reviewers: %w: %w", err, taskerr.ErrTaskInternal)
	}
	subs, err := s.repo.ListSubmissions(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w: %w", err, taskerr.ErrTaskInternal)
	}
	// 合并所有 submission 的 reviews 一次 fetch
	var allReviews []model.TaskReview
	for _, sb := range subs {
		rs, err := s.repo.ListReviewsBySubmission(ctx, sb.ID)
		if err != nil {
			return nil, fmt.Errorf("list reviews: %w: %w", err, taskerr.ErrTaskInternal)
		}
		allReviews = append(allReviews, rs...)
	}

	reviewerPIDs := make([]uint64, len(reviewerRows))
	for i, r := range reviewerRows {
		reviewerPIDs[i] = r.PrincipalID
	}
	return &TaskDetail{
		Task:        *t,
		Reviewers:   reviewerPIDs,
		Submissions: subs,
		Reviews:     allReviews,
	}, nil
}

func (s *taskService) ListByChannel(ctx context.Context, channelID, callerUserID uint64, status string, limit, offset int) ([]model.Task, error) {
	if limit <= 0 {
		limit = taskerr.ListDefaultLimit
	}
	if limit > taskerr.ListMaxLimit {
		limit = taskerr.ListMaxLimit
	}
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	if err := s.requireChannelMember(ctx, channelID, callerPrincipalID); err != nil {
		return nil, err
	}
	return s.repo.ListTasksByChannel(ctx, channelID, status, limit, offset)
}

func (s *taskService) ListMy(ctx context.Context, callerUserID uint64, status string, limit, offset int) ([]model.Task, error) {
	if limit <= 0 {
		limit = taskerr.ListDefaultLimit
	}
	if limit > taskerr.ListMaxLimit {
		limit = taskerr.ListMaxLimit
	}
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListTasksByAssignees(ctx, []uint64{callerPrincipalID}, status, limit, offset)
}

// ── Claim ───────────────────────────────────────────────────────────────

func (s *taskService) Claim(ctx context.Context, taskID, callerUserID uint64) (*model.Task, error) {
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != taskerr.StatusOpen && t.Status != taskerr.StatusInProgress {
		return nil, taskerr.ErrTaskStateTransition
	}

	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	if err := s.requireChannelMember(ctx, t.ChannelID, callerPrincipalID); err != nil {
		return nil, err
	}

	// 若已有 assignee 且不是本人 → 拒绝(MVP:未来支持"代别人 claim" 再扩)
	if t.AssigneePrincipalID != 0 && t.AssigneePrincipalID != callerPrincipalID {
		return nil, taskerr.ErrTaskAlreadyClaimed
	}

	updates := map[string]any{
		"status":                taskerr.StatusInProgress,
		"assignee_principal_id": callerPrincipalID,
	}
	if err := s.repo.UpdateTaskFields(ctx, t.ID, updates); err != nil {
		return nil, fmt.Errorf("claim task: %w: %w", err, taskerr.ErrTaskInternal)
	}
	t.Status = taskerr.StatusInProgress
	t.AssigneePrincipalID = callerPrincipalID

	s.publishTaskEvent(ctx, "task.claimed", t, map[string]any{
		"actor_principal_id":    strconv.FormatUint(callerPrincipalID, 10),
		"assignee_principal_id": strconv.FormatUint(callerPrincipalID, 10),
	})
	return t, nil
}

// ── Submit ──────────────────────────────────────────────────────────────

func (s *taskService) Submit(ctx context.Context, in SubmitInput) (*model.Task, *model.TaskSubmission, error) {
	t, err := s.loadTask(ctx, in.TaskID)
	if err != nil {
		return nil, nil, err
	}
	// submit 允许:in_progress(首次)或 revision_requested(打回后重交)。
	if t.Status != taskerr.StatusInProgress && t.Status != taskerr.StatusRevisionRequested {
		return nil, nil, taskerr.ErrTaskStateTransition
	}

	submitterPrincipalID, err := s.lookupUserPrincipalID(ctx, in.SubmitterUserID)
	if err != nil {
		return nil, nil, err
	}
	if submitterPrincipalID != t.AssigneePrincipalID {
		return nil, nil, taskerr.ErrForbidden
	}

	// 校验分两路:lightweight 任务 vs 普通任务。
	if t.IsLightweight {
		if strings.TrimSpace(in.InlineSummary) == "" {
			return nil, nil, taskerr.ErrSubmissionEmpty
		}
		if len(in.InlineSummary) > taskerr.MaxInlineSummaryLen {
			return nil, nil, taskerr.ErrSubmissionTooLarge
		}
		if len(in.Content) > 0 {
			return nil, nil, taskerr.ErrTaskLightweightHasContent
		}
	} else {
		if len(in.Content) == 0 {
			return nil, nil, taskerr.ErrSubmissionEmpty
		}
		if int64(len(in.Content)) > taskerr.MaxSubmissionByteSize {
			return nil, nil, taskerr.ErrSubmissionTooLarge
		}
		if !taskerr.IsValidOutputKind(in.ContentKind) {
			return nil, nil, taskerr.ErrTaskOutputKindInvalid
		}
		if in.ContentKind != t.OutputSpecKind {
			return nil, nil, taskerr.ErrSubmissionContentKind
		}
	}

	// OSS 上传仅普通任务路径。
	var ossKey string
	var byteSize int64
	if !t.IsLightweight {
		ext := taskerr.ExtensionForOutputKind(t.OutputSpecKind)
		ossKey = s.buildSubmissionOSSKey(t.OrgID, t.ID, ext)
		contentType := "text/plain"
		if t.OutputSpecKind == taskerr.OutputKindMarkdown {
			contentType = "text/markdown; charset=utf-8"
		}
		if s.oss == nil {
			return nil, nil, fmt.Errorf("oss client not configured: %w", taskerr.ErrTaskInternal)
		}
		if _, err := s.oss.PutObject(ctx, ossKey, in.Content, contentType); err != nil {
			return nil, nil, fmt.Errorf("oss put: %w: %w", err, taskerr.ErrTaskInternal)
		}
		byteSize = int64(len(in.Content))
	}

	contentKind := in.ContentKind
	if t.IsLightweight {
		contentKind = taskerr.OutputKindNone
	}

	var (
		submission model.TaskSubmission
		updatedTask model.Task
	)
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		sub := &model.TaskSubmission{
			TaskID:               t.ID,
			SubmitterPrincipalID: submitterPrincipalID,
			ContentKind:          contentKind,
			OSSKey:               ossKey,
			ByteSize:             byteSize,
			InlineSummary:        in.InlineSummary,
		}
		if err := tx.CreateSubmission(ctx, sub); err != nil {
			return err
		}
		submission = *sub

		now := time.Now().UTC()
		// 无需审批任务(required_approvals=0)提交即完成,跳过 submitted 态直接 approved。
		status := taskerr.StatusSubmitted
		updates := map[string]any{
			"status":       status,
			"submitted_at": now,
		}
		if t.RequiredApprovals == 0 {
			status = taskerr.StatusApproved
			updates["status"] = status
			updates["closed_at"] = now
		}
		if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
			return err
		}
		updatedTask = *t
		updatedTask.Status = status
		updatedTask.SubmittedAt = &now
		if status == taskerr.StatusApproved {
			updatedTask.ClosedAt = &now
		}
		return nil
	})
	if err != nil {
		if ossKey != "" {
			if delErr := s.oss.DeleteObject(ctx, ossKey); delErr != nil {
				s.logger.WarnCtx(ctx, "task: orphan OSS object not cleaned", map[string]any{
					"key": ossKey, "err": delErr.Error(),
				})
			}
		}
		return nil, nil, fmt.Errorf("submit tx: %w: %w", err, taskerr.ErrTaskInternal)
	}

	s.publishTaskEvent(ctx, "task.submitted", &updatedTask, map[string]any{
		"actor_principal_id":  strconv.FormatUint(submitterPrincipalID, 10),
		"submission_id":       strconv.FormatUint(submission.ID, 10),
		"submitter_principal": strconv.FormatUint(submitterPrincipalID, 10),
	})
	return &updatedTask, &submission, nil
}

// ── Review ──────────────────────────────────────────────────────────────

func (s *taskService) Review(ctx context.Context, in ReviewInput) (*model.Task, *model.TaskReview, error) {
	if !taskerr.IsValidDecision(in.Decision) {
		return nil, nil, taskerr.ErrDecisionInvalid
	}
	if len(in.Comment) > taskerr.CommentMaxLen {
		return nil, nil, taskerr.ErrTaskDescriptionInvalid
	}

	t, err := s.loadTask(ctx, in.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if t.Status != taskerr.StatusSubmitted {
		return nil, nil, taskerr.ErrTaskStateTransition
	}

	sub, err := s.repo.FindSubmissionByID(ctx, in.SubmissionID)
	if err != nil {
		return nil, nil, fmt.Errorf("find submission: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if sub == nil || sub.TaskID != t.ID {
		return nil, nil, taskerr.ErrSubmissionNotFound
	}

	reviewerPrincipalID, err := s.lookupUserPrincipalID(ctx, in.ReviewerUserID)
	if err != nil {
		return nil, nil, err
	}
	ok, err := s.repo.IsReviewer(ctx, t.ID, reviewerPrincipalID)
	if err != nil {
		return nil, nil, fmt.Errorf("check reviewer: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if !ok {
		return nil, nil, taskerr.ErrForbidden
	}

	var (
		review      model.TaskReview
		updatedTask = *t
	)
	now := time.Now().UTC()
	err = s.repo.WithTx(ctx, func(tx repository.Repository) error {
		rv := &model.TaskReview{
			TaskID:              t.ID,
			SubmissionID:        sub.ID,
			ReviewerPrincipalID: reviewerPrincipalID,
			Decision:            in.Decision,
			Comment:             in.Comment,
		}
		if err := tx.CreateReview(ctx, rv); err != nil {
			// UNIQUE 冲突视为已决 → 翻译
			if isUniqueViolation(err) {
				return taskerr.ErrReviewerDuplicate
			}
			return err
		}
		review = *rv

		// 按 decision 推状态机
		switch in.Decision {
		case taskerr.DecisionRejected:
			updates := map[string]any{
				"status":    taskerr.StatusRejected,
				"closed_at": now,
			}
			if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
				return err
			}
			updatedTask.Status = taskerr.StatusRejected
			updatedTask.ClosedAt = &now

		case taskerr.DecisionRequestChanges:
			updates := map[string]any{
				"status": taskerr.StatusRevisionRequested,
			}
			if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
				return err
			}
			updatedTask.Status = taskerr.StatusRevisionRequested

		case taskerr.DecisionApproved:
			// 数本次 submission 的 approved 总数,满足 required_approvals 则 close
			count, err := tx.CountApprovalsForSubmission(ctx, sub.ID)
			if err != nil {
				return err
			}
			if count >= int64(t.RequiredApprovals) {
				updates := map[string]any{
					"status":    taskerr.StatusApproved,
					"closed_at": now,
				}
				if err := tx.UpdateTaskFields(ctx, t.ID, updates); err != nil {
					return err
				}
				updatedTask.Status = taskerr.StatusApproved
				updatedTask.ClosedAt = &now
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("review tx: %w: %w", err, taskerr.ErrTaskInternal)
	}

	// 根据最终状态的跃迁发事件
	s.publishTaskEvent(ctx, "task.reviewed", &updatedTask, map[string]any{
		"actor_principal_id":    strconv.FormatUint(reviewerPrincipalID, 10),
		"submission_id":         strconv.FormatUint(sub.ID, 10),
		"reviewer_principal_id": strconv.FormatUint(reviewerPrincipalID, 10),
		"decision":              in.Decision,
	})
	return &updatedTask, &review, nil
}

// ── Cancel ──────────────────────────────────────────────────────────────

func (s *taskService) Cancel(ctx context.Context, taskID, callerUserID uint64) (*model.Task, error) {
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if taskerr.IsTerminalStatus(t.Status) {
		return nil, taskerr.ErrTaskStateTransition
	}

	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	// 只有 creator 或 assignee 能 cancel
	if callerPrincipalID != t.CreatedByPrincipalID && callerPrincipalID != t.AssigneePrincipalID {
		return nil, taskerr.ErrForbidden
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"status":    taskerr.StatusCancelled,
		"closed_at": now,
	}
	if err := s.repo.UpdateTaskFields(ctx, t.ID, updates); err != nil {
		return nil, fmt.Errorf("cancel task: %w: %w", err, taskerr.ErrTaskInternal)
	}
	t.Status = taskerr.StatusCancelled
	t.ClosedAt = &now

	s.publishTaskEvent(ctx, "task.cancelled", t, nil)
	return t, nil
}

// ── helpers ─────────────────────────────────────────────────────────────

// ─── 变更 assignee / reviewers ───────────────────────────────────────────────

// UpdateAssignee 换执行人。actor 必须是 task 创建人或 channel owner;非终态才允许。
//
// 参数 newAssigneePrincipalID 可以为 0 表示"清空执行人"(任务重回 open 态)。
// 非 0 时必须是 channel 成员(防止派给 org 外 principal)。
func (s *taskService) UpdateAssignee(ctx context.Context, taskID, callerUserID, newAssigneePrincipalID uint64) (*model.Task, error) {
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	return s.updateAssigneeCore(ctx, taskID, callerPrincipalID, newAssigneePrincipalID)
}

func (s *taskService) updateAssigneeCore(ctx context.Context, taskID, callerPrincipalID, newAssigneePrincipalID uint64) (*model.Task, error) {
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.requireCreatorOrChannelOwner(ctx, t, callerPrincipalID); err != nil {
		return nil, err
	}
	// 终态禁止
	switch t.Status {
	case taskerr.StatusApproved, taskerr.StatusRejected, taskerr.StatusCancelled:
		return nil, taskerr.ErrTaskStateTransition
	}
	// 校验新 assignee 在 channel 内(0 = 清空,跳过)
	if newAssigneePrincipalID != 0 {
		ok, err := s.repo.IsChannelMember(ctx, t.ChannelID, newAssigneePrincipalID)
		if err != nil {
			return nil, fmt.Errorf("check new assignee membership: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, taskerr.ErrAssigneeNotInChannel
		}
	}
	// 状态连带调整:清空 assignee 且当前 open/in_progress → 保持 open;
	// 其它状态(submitted / revision_requested)保持不变 —— 新人接手继续原有阶段
	newStatus := t.Status
	if newAssigneePrincipalID == 0 && t.Status == taskerr.StatusInProgress {
		newStatus = taskerr.StatusOpen
	}
	updates := map[string]any{
		"assignee_principal_id": newAssigneePrincipalID,
		"status":                newStatus,
	}
	if err := s.repo.UpdateTaskFields(ctx, t.ID, updates); err != nil {
		return nil, fmt.Errorf("update assignee: %w: %w", err, taskerr.ErrTaskInternal)
	}
	t.AssigneePrincipalID = newAssigneePrincipalID
	t.Status = newStatus
	s.publishTaskEvent(ctx, "task.assignee_changed", t, map[string]any{
		"actor_principal_id":        strconv.FormatUint(callerPrincipalID, 10),
		"new_assignee_principal_id": newAssigneePrincipalID,
	})
	return t, nil
}

// UpdateReviewers 换审批人列表 + 调整 required_approvals。
// 只在 submitted 之前允许(open/assigned/in_progress);提交后不改,避免和现有审批流冲突。
// 语义:task_reviewers 表整行替换(保留 task_reviews 历史不动);required clamp 到 [0, len]。
func (s *taskService) UpdateReviewers(ctx context.Context, taskID, callerUserID uint64, newReviewerPrincipalIDs []uint64, newRequiredApprovals int) (*model.Task, []uint64, error) {
	callerPrincipalID, err := s.lookupUserPrincipalID(ctx, callerUserID)
	if err != nil {
		return nil, nil, err
	}
	return s.updateReviewersCore(ctx, taskID, callerPrincipalID, newReviewerPrincipalIDs, newRequiredApprovals)
}

func (s *taskService) updateReviewersCore(ctx context.Context, taskID, callerPrincipalID uint64, newReviewerPrincipalIDs []uint64, newRequiredApprovals int) (*model.Task, []uint64, error) {
	t, err := s.loadTask(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requireCreatorOrChannelOwner(ctx, t, callerPrincipalID); err != nil {
		return nil, nil, err
	}
	// 状态闸:只允许 open / in_progress 改 reviewers(submitted / revision_requested /
	// 终态都不允许)。submitted 后改会打乱在飞的审批流。
	switch t.Status {
	case taskerr.StatusOpen, taskerr.StatusInProgress:
		// ok
	default:
		return nil, nil, taskerr.ErrTaskStateTransition
	}
	// 去重 + 校验每个 reviewer 都在 channel 内
	reviewers := dedupPrincipalIDs(newReviewerPrincipalIDs)
	for _, pid := range reviewers {
		ok, err := s.repo.IsChannelMember(ctx, t.ChannelID, pid)
		if err != nil {
			return nil, nil, fmt.Errorf("check reviewer membership: %w: %w", err, taskerr.ErrTaskInternal)
		}
		if !ok {
			return nil, nil, taskerr.ErrReviewerNotInChannel
		}
	}
	// required_approvals clamp:
	//   - reviewers=[] → required=0(任务改为"无需审批")
	//   - reviewers 非空 → required ∈ [1, len];传入 0 / 负数视作 "至少 1"
	required := newRequiredApprovals
	if len(reviewers) == 0 {
		required = 0
	} else if required <= 0 {
		required = 1
	} else if required > len(reviewers) {
		required = len(reviewers)
	}
	// 替换 reviewer 行
	if err := s.repo.ReplaceReviewers(ctx, t.ID, reviewers); err != nil {
		return nil, nil, fmt.Errorf("replace reviewers: %w: %w", err, taskerr.ErrTaskInternal)
	}
	// 更新 required_approvals
	if t.RequiredApprovals != required {
		if err := s.repo.UpdateTaskFields(ctx, t.ID, map[string]any{
			"required_approvals": required,
		}); err != nil {
			return nil, nil, fmt.Errorf("update required_approvals: %w: %w", err, taskerr.ErrTaskInternal)
		}
		t.RequiredApprovals = required
	}
	s.publishTaskEvent(ctx, "task.reviewers_changed", t, map[string]any{
		"actor_principal_id":     strconv.FormatUint(callerPrincipalID, 10),
		"new_reviewer_count":     len(reviewers),
		"new_required_approvals": required,
	})
	return t, reviewers, nil
}

// requireCreatorOrChannelOwner 权限闸:task 创建人 OR task 所在 channel 的 owner 才放行。
func (s *taskService) requireCreatorOrChannelOwner(ctx context.Context, t *model.Task, callerPrincipalID uint64) error {
	if t.CreatedByPrincipalID == callerPrincipalID {
		return nil
	}
	role, err := s.repo.GetChannelMemberRole(ctx, t.ChannelID, callerPrincipalID)
	if err != nil {
		return fmt.Errorf("check channel owner: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if role == "owner" {
		return nil
	}
	return taskerr.ErrForbidden
}

func (s *taskService) loadTask(ctx context.Context, id uint64) (*model.Task, error) {
	t, err := s.repo.FindTaskByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find task: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if t == nil {
		return nil, taskerr.ErrTaskNotFound
	}
	return t, nil
}

func (s *taskService) lookupUserPrincipalID(ctx context.Context, userID uint64) (uint64, error) {
	pid, err := s.repo.LookupUserPrincipalID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, taskerr.ErrForbidden
		}
		return 0, fmt.Errorf("lookup user principal: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if pid == 0 {
		return 0, taskerr.ErrForbidden
	}
	return pid, nil
}

func (s *taskService) channelOrgID(ctx context.Context, channelID uint64) (uint64, error) {
	org, err := s.repo.FindChannelOrgID(ctx, channelID)
	if err != nil {
		return 0, fmt.Errorf("find channel org: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if org == 0 {
		return 0, taskerr.ErrTaskInternal
	}
	return org, nil
}

func (s *taskService) requireChannelOpen(ctx context.Context, channelID uint64) error {
	status, err := s.repo.FindChannelStatus(ctx, channelID)
	if err != nil {
		return fmt.Errorf("find channel status: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if status != "open" {
		return taskerr.ErrTaskStateTransition
	}
	return nil
}

func (s *taskService) requireChannelMember(ctx context.Context, channelID, principalID uint64) error {
	ok, err := s.repo.IsChannelMember(ctx, channelID, principalID)
	if err != nil {
		return fmt.Errorf("check channel member: %w: %w", err, taskerr.ErrTaskInternal)
	}
	if !ok {
		return taskerr.ErrForbidden
	}
	return nil
}

func (s *taskService) buildSubmissionOSSKey(orgID, taskID uint64, ext string) string {
	prefix := s.cfg.OSSPathPrefix
	if prefix == "" {
		prefix = "synapse"
	}
	// 8 字节随机 hex = 16 字符;避免同毫秒并发碰撞
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	random := hex.EncodeToString(buf)
	ts := time.Now().UTC().Format("20060102150405")
	return fmt.Sprintf("%s/%d/tasks/%d/%s-%s.%s", prefix, orgID, taskID, ts, random, ext)
}

func (s *taskService) publishTaskEvent(ctx context.Context, eventType string, t *model.Task, extra map[string]any) {
	if s.publisher == nil || s.cfg.TaskEventStream == "" {
		return
	}
	fields := map[string]any{
		"event_type":              eventType,
		"org_id":                  strconv.FormatUint(t.OrgID, 10),
		"channel_id":              strconv.FormatUint(t.ChannelID, 10),
		"task_id":                 strconv.FormatUint(t.ID, 10),
		"task_title":              t.Title, // 给 system_event consumer 渲染卡片用,省一次 DB 查询
		"status":                  t.Status,
		"created_by_principal_id": strconv.FormatUint(t.CreatedByPrincipalID, 10),
		"is_lightweight":          strconv.FormatBool(t.IsLightweight),
		"published_at":            time.Now().UTC().Format(time.RFC3339Nano),
	}
	if t.CreatedViaPrincipalID != 0 {
		fields["created_via_principal_id"] = strconv.FormatUint(t.CreatedViaPrincipalID, 10)
	}
	for k, v := range extra {
		fields[k] = v
	}
	id, err := s.publisher.Publish(ctx, s.cfg.TaskEventStream, fields)
	if err != nil {
		s.logger.WarnCtx(ctx, "task: publish event failed", map[string]any{
			"event_type": eventType, "task_id": t.ID, "err": err.Error(),
		})
		return
	}
	s.logger.DebugCtx(ctx, "task: published event", map[string]any{
		"event_type": eventType, "task_id": t.ID, "stream_id": id,
	})
}

// dedupPrincipalIDs 去重、去 0。
// uint64InSlice 简单线性查 —— candidate 列表至多 2 元素,无需 map。
func uint64InSlice(haystack []uint64, needle uint64) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func dedupPrincipalIDs(in []uint64) []uint64 {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(in))
	out := make([]uint64, 0, len(in))
	for _, id := range in {
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// isUniqueViolation 粗糙判断是否 MySQL duplicate key 错误(Error 1062)。
// 字符串匹配,和 internal/channel / internal/user 里的 mysql_err 同套路,
// 抽象成 common helper 在后续重构时一并做。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") || strings.Contains(msg, "Duplicate entry")
}
