// Package scoped 顶级系统 agent 的跨 org 隔离核心。
//
// ScopedServices 构造时一次性绑定 (orgID, channelID, actorPrincipalID)(其中
// actorPID 固定为顶级 agent 的 principal_id),之后所有方法内部硬塞这三个值去调
// 底层 service —— 调用方(tools dispatcher / handler)**无法**传入别的 org 或
// channel,物理上就阻断了跨 scope 泄露。
//
// 设计约束(Code review 必查):
//   - ScopedServices 字段 orgID / channelID / actorPrincipalID 小写私有,**不导出** getter
//   - 所有 public method 的签名**不得包含** orgID / channelID / actorPID 参数
//   - internal/agentsys 内的其它代码(handler、tools dispatcher)**不得** import
//     channel.Service / task.Service 直接使用,只能走 ScopedServices
//   - tool schema(ParametersJSONSchema)不得包含 org_id / channel_id 字段
//
// 这些约束是本 PR 唯一依赖"纪律"而非"类型系统"的地方;但 ScopedServices 本身的
// 类型结构让"不小心写出跨 org 的调用"在 review 时极易发现。
package scoped

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	pmsvc "github.com/eyrihe999-stack/Synapse/internal/pm/service"
	taskmodel "github.com/eyrihe999-stack/Synapse/internal/task/model"
	tasksvc "github.com/eyrihe999-stack/Synapse/internal/task/service"
)

// ErrKBUnavailable KB 检索 / 读文档通路未装配(PG 缺失或 embedder 未配置)时,
// SearchKB / GetKBDocument 返此错误,dispatcher 转 JSON 给 LLM。
var ErrKBUnavailable = errors.New("scoped: kb subsystem unavailable")

// ScopedServices 见包 godoc。
//
// 触发上下文字段(triggerAuthorPID / triggerMessageID):给 PostMessage 用,
// 让 agent 的回复自动 @ 提问人 + 自动标记为对触发消息的"回复"(前端渲染引用卡片)。
// 零值(0)表示当前 scoped 不来自某条具体消息(如预算超限时的 post),此时
// PostMessage 不自动填充 mention / reply_to。
type ScopedServices struct {
	orgID            uint64
	channelID        uint64
	actorPrincipalID uint64
	triggerAuthorPID uint64 // 触发本次处理的消息的作者 principal_id,可 0
	triggerMessageID uint64 // 触发本次处理的消息 id,可 0

	messages channelsvc.MessageService
	tasks    tasksvc.TaskService
	// channelsvc.KBRefService 已退役(channel_kb_refs 表 + per-channel KB 挂载概念
	// 整体废弃)。LLM 看 KB 走 KBQuery 提供的 list_kb_documents / get_kb_document /
	// search_kb,可见集由 channels.project_id JOIN project_kb_refs 计算。
	kbQuery  channelsvc.KBQueryService // 可 nil(PG / embedder 缺失时);tool dispatcher 内做空值检查
	members  channelsvc.MemberService
	pm       *pmsvc.Service // PR-B B2:Architect 调 PM tool 用;可 nil(top-orch 不需要)
	db       *gorm.DB       // 反查 channel.project_id / projects.created_by 等;可 nil
	logger   logger.LoggerInterface

	// calledTools per-turn(单次 handleMention 内)已调过哪些 tool。
	// 用于 hardness 校验:例如 split_workstream_into_tasks 带 assignee 时,要求
	// 本 turn 必须先调过 list_org_members(让 LLM "看过" org 名册才能分配人)。
	// dispatcher 串行执行,无需锁。
	calledTools map[string]bool
}

// Deps 构造 ScopedServices 所需的底层依赖(运行期全局单例,不随 scope 变)。
//
// KBQuery 允许 nil:进程未配 PG / embedder 时整个 KB 检索通路降级,LLM 调相关 tool
// 时由 dispatcher 返回 "kb unavailable" 错误,不让 agent 启动失败。
type Deps struct {
	Messages channelsvc.MessageService
	Tasks    tasksvc.TaskService
	KBQuery  channelsvc.KBQueryService
	Members  channelsvc.MemberService
	// PM PR-B B2:Architect 调用 PM 编排 tool(create_initiative / create_workstream
	// 等)用。top-orchestrator 不需要,装配时可 nil。
	PM     *pmsvc.Service
	// DB PR-B B2:dispatcher 反查 channel.project_id / projects.created_by 等元数据
	// (Architect 用 project owner 的 user_id 作为 PM 调用 actor)。可 nil。
	DB     *gorm.DB
	Logger logger.LoggerInterface
}

// Trigger 触发本次 agent 处理的消息上下文。AuthorPrincipalID 非 0 时,
// PostMessage 会自动 @ 此 principal;MessageID 非 0 时会自动设为回复目标。
type Trigger struct {
	AuthorPrincipalID uint64
	MessageID         uint64
}

// New 构造一个绑死 (orgID, channelID, actorPrincipalID) 的 ScopedServices,不带触发上下文。
// 适合预算超限回 "budget used" 之类的无触发消息 post;handleMention 正常路径应走 NewForTrigger。
func New(orgID, channelID, actorPrincipalID uint64, deps Deps) *ScopedServices {
	return NewForTrigger(orgID, channelID, actorPrincipalID, Trigger{}, deps)
}

// NewForTrigger 同 New,但额外记录触发消息的 author/id。后续 PostMessage 会:
//  1. 如果 triggerAuthorPID 不在 mentions 列表且不等于自己,自动 @ 提问人
//  2. 如果 triggerMessageID 非 0,将本次 post 的 reply_to 默认指向触发消息
func NewForTrigger(orgID, channelID, actorPrincipalID uint64, trigger Trigger, deps Deps) *ScopedServices {
	return &ScopedServices{
		orgID:            orgID,
		channelID:        channelID,
		actorPrincipalID: actorPrincipalID,
		triggerAuthorPID: trigger.AuthorPrincipalID,
		triggerMessageID: trigger.MessageID,
		messages:         deps.Messages,
		tasks:            deps.Tasks,
		kbQuery:          deps.KBQuery,
		members:          deps.Members,
		pm:               deps.PM,
		db:               deps.DB,
		logger:           deps.Logger,
	}
}

// PM 暴露给 PM tool dispatcher。返 nil 时 dispatcher 应返"PM tool unavailable"
// (top-orchestrator 路径不装配 PM)。
func (s *ScopedServices) PM() *pmsvc.Service { return s.pm }

// LookupProjectIDForChannel 通过 channel.id 反查 project_id。
// Architect dispatch 用 —— PM 操作必须知道 project_id 但 LLM 不一定提供。
func (s *ScopedServices) LookupProjectIDForChannel(ctx context.Context) (uint64, error) {
	if s.db == nil {
		return 0, fmt.Errorf("scoped.db not configured")
	}
	var pid uint64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT project_id FROM channels WHERE id = ?", s.channelID,
	).Scan(&pid).Error; err != nil {
		return 0, fmt.Errorf("lookup project_id for channel %d: %w", s.channelID, err)
	}
	return pid, nil
}

// LookupProjectOwnerUserID 反查 project.created_by(user.id)。
//
// Architect 调用 PM tool 时,**借 project owner 身份**通过 pm.Service 的
// org membership 校验(Architect 自己是 system agent,不是 org 成员)。
// v0 简化方案;后续如有需要,加 pm.ByPrincipalAgent 接口让 system agent 直接调。
func (s *ScopedServices) LookupProjectOwnerUserID(ctx context.Context, projectID uint64) (uint64, error) {
	if s.db == nil {
		return 0, fmt.Errorf("scoped.db not configured")
	}
	var uid uint64
	if err := s.db.WithContext(ctx).Raw(
		"SELECT created_by FROM projects WHERE id = ?", projectID,
	).Scan(&uid).Error; err != nil {
		return 0, fmt.Errorf("lookup project owner for %d: %w", projectID, err)
	}
	if uid == 0 {
		return 0, fmt.Errorf("project %d has no creator", projectID)
	}
	return uid, nil
}

// ListMembersWithProfile 给 handler 组 LLM prompt 的"成员名册"用。
// 返回每个成员的 principal_id / role / display_name / kind / is_global_agent,
// orchestrator 据此渲染成 "成员:principal_id=8 Claude(org 内 agent,member)" 这种可读文本
// 喂给 LLM,使其能直接 create_task(assignee_principal_id=8) 而不是反问用户。
func (s *ScopedServices) ListMembersWithProfile(ctx context.Context) ([]channelrepo.MemberWithProfile, error) {
	return s.members.ListWithProfile(ctx, s.channelID)
}

// OrgMemberRow Architect 用 LLM tool list_org_members 返回的简化成员行。
// 不暴露 user.status / role 等字段 —— Architect 只需要"谁在这个 org,principal_id 是多少"
// 用于让用户分配任务 assignee。
type OrgMemberRow struct {
	UserID      uint64 `json:"user_id"`
	PrincipalID uint64 `json:"principal_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// ListProjectOrgMembers 列 project 所属 org 的全部 active 用户成员。
// Architect 拿这个名单后,**不自己决定**谁干哪个 task,而是把名单输出给用户,让用户分配。
// 设计上故意只返 active user(不返 banned / deleted),且不含 agent —— task assignee 通常是人。
func (s *ScopedServices) ListProjectOrgMembers(ctx context.Context, projectID uint64) ([]OrgMemberRow, error) {
	if s.db == nil {
		return nil, fmt.Errorf("scoped.db not configured")
	}
	rows := []OrgMemberRow{}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT u.id AS user_id, u.principal_id, u.email, u.display_name
		  FROM org_members om
		  JOIN projects p ON p.org_id = om.org_id
		  JOIN users u ON u.id = om.user_id
		 WHERE p.id = ? AND u.status = 1
		 ORDER BY u.display_name, u.email
	`, projectID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list org members for project %d: %w", projectID, err)
	}
	return rows, nil
}

// ListProjectKBRefs 列 project 挂载的 KB ref(source / doc 二选一);委托 PM service。
// Architect 用此 tool 看项目挂了哪些 KB,再决定要不要进一步 GetKBDocument 读全文。
func (s *ScopedServices) ListProjectKBRefs(ctx context.Context, projectID uint64) ([]ProjectKBRefRow, error) {
	if s.pm == nil {
		return nil, fmt.Errorf("scoped.pm not configured")
	}
	refs, err := s.pm.ProjectKBRef.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectKBRefRow, 0, len(refs))
	for _, r := range refs {
		out = append(out, ProjectKBRefRow{
			ID: r.ID, KBSourceID: r.KBSourceID, KBDocumentID: r.KBDocumentID,
			AttachedBy: r.AttachedBy, AttachedAt: r.AttachedAt.Unix(),
		})
	}
	return out, nil
}

// ProjectKBRefRow LLM-friendly 简化形态:Architect 只关心 "挂的是 source 还是 doc + id"。
type ProjectKBRefRow struct {
	ID           uint64 `json:"id"`
	KBSourceID   uint64 `json:"kb_source_id,omitempty"`
	KBDocumentID uint64 `json:"kb_document_id,omitempty"`
	AttachedBy   uint64 `json:"attached_by"`
	AttachedAt   int64  `json:"attached_at_unix"`
}

// ChannelID 仅供 runtime handler 内部写 audit_events.channel_id 字段使用。
//
// **不要**基于这个 getter 去调任何带 channelID 参数的底层 service —— 那会破坏
// "只有 ScopedServices 自己能把 channelID 塞进 service"的安全边界。审计写入是
// ScopedServices 外部、orchestrator 维度的事,需要暴露。
func (s *ScopedServices) ChannelID() uint64 { return s.channelID }

// OperatingOrgID 同上,仅供 runtime handler 写审计 / 预算计费使用。
func (s *ScopedServices) OperatingOrgID() uint64 { return s.orgID }

// ActorPrincipalID 同上,仅供 runtime handler 写审计的 actor_principal_id 字段。
func (s *ScopedServices) ActorPrincipalID() uint64 { return s.actorPrincipalID }

// MarkToolCalled per-turn 状态:dispatcher 在某个 tool 成功路径末尾标记。
// 后续 hardness 校验(如 split 必须先 list_org_members)读这个标记。
func (s *ScopedServices) MarkToolCalled(toolName string) {
	if s.calledTools == nil {
		s.calledTools = make(map[string]bool, 4)
	}
	s.calledTools[toolName] = true
}

// HasToolBeenCalled 同上,读取标记。未标记 → false。
func (s *ScopedServices) HasToolBeenCalled(toolName string) bool {
	return s.calledTools[toolName]
}

// ─── LLM 可调的 tool 实现 ───────────────────────────────────────────────────
//
// 下列方法是 tools dispatcher 唯一的调用面。每个都在内部塞 s.orgID /
// s.channelID / s.actorPrincipalID,调用方**不传**这些参数。

// PostMessage 在绑定的 channel 回复一条文本消息。
//
// 自动行为(来自 Trigger 上下文):
//   - 若 triggerAuthorPID 非 0 且不等于 actor,自动把它加入 mentions(去重),
//     让 agent 的回复默认 @ 提问人(async 多人场景避免"这句在回谁"歧义)
//   - 若 triggerMessageID 非 0,本次 post 的 reply_to 自动指向触发消息
//     (前端渲染为引用卡片)
//
// LLM 通过 tool 给的 mentionPrincipalIDs 不覆盖上述自动行为 —— 两者合并去重。
func (s *ScopedServices) PostMessage(ctx context.Context, body string, mentionPrincipalIDs []uint64) (*channelsvc.PostedMessage, error) {
	mentions := mentionPrincipalIDs
	if s.triggerAuthorPID != 0 && s.triggerAuthorPID != s.actorPrincipalID {
		if !containsPID(mentions, s.triggerAuthorPID) {
			// 把提问人放在最前 —— 前端渲染"@某某 ..."时语义更自然
			mentions = append([]uint64{s.triggerAuthorPID}, mentions...)
		}
	}
	return s.messages.PostAsPrincipal(ctx, s.channelID, s.actorPrincipalID, body, mentions, s.triggerMessageID)
}

func containsPID(ids []uint64, target uint64) bool {
	for _, v := range ids {
		if v == target {
			return true
		}
	}
	return false
}

// ListRecentMessages 列出本 channel 最近 limit 条消息(不含分页,limit 由 runtime
// 的 const 写死,见 runtime/const.go)。
func (s *ScopedServices) ListRecentMessages(ctx context.Context, limit int) ([]channelsvc.MessageWithMentions, error) {
	// beforeID=0 表示从最新开始取;底层 service 自己 clamp limit 上下界
	return s.messages.ListForPrincipal(ctx, s.channelID, s.actorPrincipalID, 0, limit)
}

// ListChannelKBRefs 已退役(channel 不再持有 KB ref 概念)。LLM 想知道"手头有哪些资料"
// 改调 list_kb_documents tool(它内部用 kbQuery,经由 channel.project_id 反查
// project_kb_refs 算可见集)。

// SearchKB 在本 channel 可见 KB 上做语义检索。kbQuery 未注入时返 ErrKBUnavailable
// (而不是 panic),让 dispatcher 把人读错误回喂 LLM。
func (s *ScopedServices) SearchKB(ctx context.Context, query string, topK int) ([]channelsvc.KBSearchHit, error) {
	if s.kbQuery == nil {
		return nil, ErrKBUnavailable
	}
	return s.kbQuery.SearchByPrincipal(ctx, s.channelID, s.actorPrincipalID, query, topK)
}

// GetKBDocument 拉本 channel 可见的 KB 文档全文(text 类走 OSS 原文,二进制回退到 chunks 拼接)。
func (s *ScopedServices) GetKBDocument(ctx context.Context, docID uint64) (*channelsvc.KBDocumentContent, error) {
	if s.kbQuery == nil {
		return nil, ErrKBUnavailable
	}
	return s.kbQuery.GetDocumentByPrincipal(ctx, s.channelID, docID, s.actorPrincipalID)
}

// CreateTaskArgs CreateTask 的"业务"入参 —— 不含 ChannelID / CreatorPrincipalID,
// 这两个由 ScopedServices 自己塞。
//
// OutputSpecKind / RequiredApprovals 保留 0 值走底层 service 的默认策略。
type CreateTaskArgs struct {
	Title                string
	Description          string
	OutputSpecKind       string
	AssigneePrincipalID  uint64
	ReviewerPrincipalIDs []uint64
	RequiredApprovals    int
}

// TaskCreateOut CreateTask 的返回 —— 本地 wrapper,避免 tools dispatcher 直接
// 依赖 task.service 的多返回值形态。
type TaskCreateOut struct {
	Task      *taskmodel.Task
	Reviewers []taskmodel.TaskReviewer
}

// CreateTaskInChannel PR-B B2 给 split_workstream_into_tasks 用 —— 在**指定 channel**
// (workstream channel,非 scoped 绑定的 console channel)创建 task。
//
// Architect 在 console channel 触发,但要把 task 落到对应 workstream 的 channel。
// 不能复用 CreateTask(它会写到 s.channelID = console channel)。
//
// output_spec_kind 兜底为 "markdown":底层 service 强校验空值非法
// (taskerr.ErrTaskOutputKindInvalid),而 split tool schema 没暴露这个字段给 LLM,
// 不在这里默认就一定炸。和 MCP 那条路径(mcp/tool_pm_workstream.go)行为对齐。
func (s *ScopedServices) CreateTaskInChannel(
	ctx context.Context, channelID uint64, title, description string,
	assigneePrincipalID uint64, isLightweight bool,
) (*taskmodel.Task, error) {
	task, _, err := s.tasks.CreateByPrincipal(ctx, tasksvc.CreateByPrincipalInput{
		ChannelID:            channelID,
		CreatorPrincipalID:   s.actorPrincipalID,
		InitiatorPrincipalID: s.triggerAuthorPID,
		Title:                title,
		Description:          description,
		OutputSpecKind:       "markdown",
		AssigneePrincipalID:  assigneePrincipalID,
		IsLightweight:        isLightweight,
	})
	return task, err
}

// CreateTask 在绑定的 channel 派发一个 task。
//
// 代派语义:actor(顶级 agent)作为 Creator 执行 INSERT,但如果触发本次处理的
// 消息有 author(triggerAuthorPID 非 0),把 author 作为 Initiator 传给
// service —— service 会把 created_by 记作发起人、created_via 记作 agent,并在
// LLM 没给 reviewer 时自动 fallback 为发起人。详见 task.service.CreateByPrincipal。
//
// OutputSpecKind 兜底为 "markdown":底层 service 强校验空值非法,而 LLM 可能
// 不传该字段(tools.go schema 标 optional)。和 CreateTaskInChannel / MCP 路径行为对齐。
func (s *ScopedServices) CreateTask(ctx context.Context, in CreateTaskArgs) (*TaskCreateOut, error) {
	outputKind := in.OutputSpecKind
	if outputKind == "" {
		outputKind = "markdown"
	}
	task, reviewers, err := s.tasks.CreateByPrincipal(ctx, tasksvc.CreateByPrincipalInput{
		ChannelID:            s.channelID,
		CreatorPrincipalID:   s.actorPrincipalID,
		InitiatorPrincipalID: s.triggerAuthorPID,
		Title:                in.Title,
		Description:          in.Description,
		OutputSpecKind:       outputKind,
		AssigneePrincipalID:  in.AssigneePrincipalID,
		ReviewerPrincipalIDs: in.ReviewerPrincipalIDs,
		RequiredApprovals:    in.RequiredApprovals,
	})
	if err != nil {
		return nil, err
	}
	return &TaskCreateOut{Task: task, Reviewers: reviewers}, nil
}
