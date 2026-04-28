// Package service channel 模块业务层。
//
// 拆分:每个 entity 一个 Service interface + struct 实现(project / version /
// channel / member)。顶层 Service 聚合所有子 service,便于 wiring 层一次性注入。
//
// 权限模型:channel 模块的操作分两层权限检查 ——
//  1. **Org 归属**:调用者必须是 project/channel 所属 org 的成员。通过注入的
//     OrgMembershipChecker(包装 organization.OrgService.IsMember)校验
//  2. **Channel 内角色**:改 member / archive channel 等要 owner;其他操作要
//     至少 member。通过 channel_members 表自查
//
// 被加入 channel 的 principal(可能是 user 也可能是 agent),要单独校验它属于
// 该 channel 所在的 org —— 通过 PrincipalOrgResolver 完成分叉查询(user 走
// org_members,agent 走 agents.org_id)。
package service

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
)

// OrgMembershipChecker 由 wiring 层注入,实际调 organization.OrgService.IsMember。
// 语义:userID 是否是 orgID 的活跃成员。
type OrgMembershipChecker interface {
	IsMember(ctx context.Context, orgID, userID uint64) (bool, error)
}

// OrgMembershipCheckerFunc 函数适配器,便于 main.go 直接注入 orgService.IsMember。
type OrgMembershipCheckerFunc func(ctx context.Context, orgID, userID uint64) (bool, error)

// IsMember 实现 OrgMembershipChecker。
func (f OrgMembershipCheckerFunc) IsMember(ctx context.Context, orgID, userID uint64) (bool, error) {
	return f(ctx, orgID, userID)
}

// PrincipalOrgResolver 由 wiring 层注入,统一回答"principal 是否属于 org"。
// 实现见 principal_resolver.go(查 principals + users + agents 三表分叉)。
type PrincipalOrgResolver interface {
	IsPrincipalInOrg(ctx context.Context, principalID, orgID uint64) (bool, error)
}

// Service 聚合 channel 模块所有业务接口。
//
// Project / Version 已物理迁到 internal/pm 模块,channel.Service 不再持有这两个
// 子服务;前端 / MCP 直接调 pm 模块的 /api/v2/projects / /api/v2/versions 路由。
type Service struct {
	Channel    ChannelService
	Member     MemberService
	Message    MessageService
	// KBRef 已退役 —— 老 channel_kb_refs 表 + per-channel KB 挂载概念整体废弃,
	// 改由 pm.ProjectKBRefService 在 project 维度管理。
	KBQuery    KBQueryService
	Document   DocumentService
	Attachment AttachmentService
}

// Config 构造 Service 所需的配置项。
//
// ChannelEventStream 事件总线 stream key(message.posted 等),空串 = 不发事件。
// 由 cmd/synapse 从 config.EventBus.ChannelStream 传入。
//
// OSSPathPrefix 共享文档版本上传 OSS 时的 key 前缀(同 task / document 模块约定)。
// 空串则在 buildOSSKey 内退化为 "synapse"。
type Config struct {
	ChannelEventStream string
	OSSPathPrefix      string
}

// New 构造 Service。依赖:
//   - repo:channel repository
//   - orgChecker:org 成员关系查询器(包装 orgSvc.IsMember)
//   - principalResolver:principal ↔ org 归属查询器
//   - publisher:eventbus 发布器(可 nil,MessageService 按 nil 降级"只写 DB")
//   - oss:ossupload.Client(可 nil;DocumentService 在 nil 时所有写路径返 ErrChannelInternal)
//   - uploadSigner:OSS 直传 commit token 签名器(可 nil;nil 时 Presign/Commit 路径返错)
//   - docRepo / embedder:KBQueryService 需要的 KB 数据访问 + query 向量化器(都可 nil;
//     nil 时 KBQuery 字段也为 nil,KB 检索 / 读文档相关 tool 在调用方按 nil 检查降级)
//   - log:日志接口
func New(
	cfg Config,
	repo repository.Repository,
	orgChecker OrgMembershipChecker,
	principalResolver PrincipalOrgResolver,
	publisher eventbus.Publisher,
	oss ossupload.Client,
	uploadSigner *uploadtoken.Signer,
	docRepo docrepo.Repository,
	embedder embedding.Embedder,
	log logger.LoggerInterface,
) *Service {
	var kbQuery KBQueryService
	if docRepo != nil {
		// embedder / oss 允许 nil —— SearchByPrincipal / GetDocument 内部已对它们做空值降级。
		kbQuery = NewKBQueryService(repo, docRepo, oss, embedder, log)
	}
	return &Service{
		Channel:    newChannelService(repo, orgChecker, publisher, cfg.ChannelEventStream, log),
		Member:     newMemberService(repo, orgChecker, principalResolver, publisher, cfg.ChannelEventStream, log),
		Message:    newMessageService(repo, publisher, cfg.ChannelEventStream, log),
		KBQuery:    kbQuery,
		Document:   newDocumentService(repo, oss, publisher, cfg.ChannelEventStream, cfg.OSSPathPrefix, uploadSigner, log),
		Attachment: newAttachmentService(repo, oss, cfg.OSSPathPrefix, uploadSigner, log),
	}
}
