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

	channelmodel "github.com/eyrihe999-stack/Synapse/internal/channel/model"
	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
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
	kbRefs   channelsvc.KBRefService
	kbQuery  channelsvc.KBQueryService // 可 nil(PG / embedder 缺失时);tool dispatcher 内做空值检查
	members  channelsvc.MemberService
	logger   logger.LoggerInterface
}

// Deps 构造 ScopedServices 所需的底层依赖(运行期全局单例,不随 scope 变)。
//
// KBQuery 允许 nil:进程未配 PG / embedder 时整个 KB 检索通路降级,LLM 调相关 tool
// 时由 dispatcher 返回 "kb unavailable" 错误,不让 agent 启动失败。
type Deps struct {
	Messages channelsvc.MessageService
	Tasks    tasksvc.TaskService
	KBRefs   channelsvc.KBRefService
	KBQuery  channelsvc.KBQueryService
	Members  channelsvc.MemberService
	Logger   logger.LoggerInterface
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
		kbRefs:           deps.KBRefs,
		kbQuery:          deps.KBQuery,
		members:          deps.Members,
		logger:           deps.Logger,
	}
}

// ListMembersWithProfile 给 handler 组 LLM prompt 的"成员名册"用。
// 返回每个成员的 principal_id / role / display_name / kind / is_global_agent,
// orchestrator 据此渲染成 "成员:principal_id=8 Claude(org 内 agent,member)" 这种可读文本
// 喂给 LLM,使其能直接 create_task(assignee_principal_id=8) 而不是反问用户。
func (s *ScopedServices) ListMembersWithProfile(ctx context.Context) ([]channelrepo.MemberWithProfile, error) {
	return s.members.ListWithProfile(ctx, s.channelID)
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

// ListChannelKBRefs 列出本 channel 挂载的 KB 引用(文档/代码库片段)。
// LLM 组 prompt 时会用这个告诉模型"手头有哪些资料可引"。
func (s *ScopedServices) ListChannelKBRefs(ctx context.Context) ([]channelmodel.ChannelKBRef, error) {
	return s.kbRefs.ListForPrincipal(ctx, s.channelID, s.actorPrincipalID)
}

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

// CreateTask 在绑定的 channel 派发一个 task。
//
// 代派语义:actor(顶级 agent)作为 Creator 执行 INSERT,但如果触发本次处理的
// 消息有 author(triggerAuthorPID 非 0),把 author 作为 Initiator 传给
// service —— service 会把 created_by 记作发起人、created_via 记作 agent,并在
// LLM 没给 reviewer 时自动 fallback 为发起人。详见 task.service.CreateByPrincipal。
func (s *ScopedServices) CreateTask(ctx context.Context, in CreateTaskArgs) (*TaskCreateOut, error) {
	task, reviewers, err := s.tasks.CreateByPrincipal(ctx, tasksvc.CreateByPrincipalInput{
		ChannelID:            s.channelID,
		CreatorPrincipalID:   s.actorPrincipalID,
		InitiatorPrincipalID: s.triggerAuthorPID,
		Title:                in.Title,
		Description:          in.Description,
		OutputSpecKind:       in.OutputSpecKind,
		AssigneePrincipalID:  in.AssigneePrincipalID,
		ReviewerPrincipalIDs: in.ReviewerPrincipalIDs,
		RequiredApprovals:    in.RequiredApprovals,
	})
	if err != nil {
		return nil, err
	}
	return &TaskCreateOut{Task: task, Reviewers: reviewers}, nil
}
