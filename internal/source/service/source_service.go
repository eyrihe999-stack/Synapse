// source_service.go 知识源管理 service。
//
// 职责:
//   - 列出 / 查 source(任何 org 成员可读)
//   - 改 visibility(仅 source owner)
//   - 给 document 模块用的 EnsureManualUpload(lazy 创建用户的 manual_upload source)
//
// M2 阶段不接 RBAC 权限位 —— 改 visibility 仅做"是否 source owner"硬规则校验。
//
// 关键硬规则:
//   - source owner 永远可改自己的 visibility
//   - visibility 取值必须是 org / group / private 之一
//   - M2 不开放删除接口(manual_upload 不应被用户主动删除)
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/integration/gitlab"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	permmodel "github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/eyrihe999-stack/Synapse/internal/source/dto"
	"github.com/eyrihe999-stack/Synapse/internal/source/model"
	"github.com/eyrihe999-stack/Synapse/internal/source/repository"
	"gorm.io/gorm"
)

// SourceService 定义 source 管理的业务操作。
//
//sayso-lint:ignore interface-pollution
type SourceService interface {
	// EnsureManualUpload 幂等地确保某 user 在某 org 下的 manual_upload source 存在。
	// 给 document 上传链路调:upload 前先拿到 source_id。
	//
	// 可能的错误:
	//   - ErrSourceInternal:数据库操作失败
	EnsureManualUpload(ctx context.Context, orgID, userID uint64) (*dto.SourceResponse, error)

	// CreateCustomSource 用户自建一个 kind=custom 的数据源。callerUserID 自动成为 owner。
	// name 必填(trim 后非空,<=MaxSourceNameLength);visibility 省略 → org。
	//
	// 可能的错误:
	//   - ErrSourceInvalidName:name trim 后为空或超长
	//   - ErrSourceInvalidVisibility:visibility 非法
	//   - ErrSourceNameExists:同一 owner 在该 org 下已有同名 source
	//   - ErrSourceInternal
	CreateCustomSource(ctx context.Context, orgID, callerUserID uint64, req dto.CreateSourceRequest) (*dto.SourceResponse, error)

	// GetSource 查单个 source(必须属于该 org)。
	//
	// 可能的错误:
	//   - ErrSourceNotFound:source 不存在或不属于该 org
	//   - ErrSourceInternal:数据库操作失败
	GetSource(ctx context.Context, orgID, sourceID uint64) (*dto.SourceResponse, error)

	// ListSources 分页列出 org 下的 source。
	//
	// scope:
	//   - visible(默认):只列 caller 能读的 source(owner / visibility=org / ACL 命中)
	//   - all:           列全 org(任何成员可调,看作"管理视图")
	//
	// kindFilter 为空 → 全部 kind。
	ListSources(ctx context.Context, orgID, callerUserID uint64, scope ListScope, kindFilter string, page, size int) (*dto.ListSourcesResponse, error)

	// ListMySources 列出某 user 在某 org 中作为 owner 的所有 source(不分页)。
	ListMySources(ctx context.Context, orgID, userID uint64) ([]dto.SourceResponse, error)

	// FilterIDsByKinds 在 ids 集合里按 kind 白名单过滤,只返 ID。
	// 跨库场景的"中间件":document handler 已经算好可见 source ID 集,要再按 source.kind
	// 限定(如知识库文档页只展示 manual_upload),documents 在 PG / sources 在 MySQL
	// 跨库 subquery 不可行 → 走这里求交集再传回 documents repo。
	// kinds 空 → 不过滤(返回 ids 副本);ids 空 → 短路返空。
	FilterIDsByKinds(ctx context.Context, orgID uint64, ids []uint64, kinds []string) ([]uint64, error)

	// UpdateVisibility 改 source 的 visibility。仅 source owner 可调用。
	//
	// 可能的错误:
	//   - ErrSourceNotFound / ErrSourceForbidden /
	//     ErrSourceInvalidVisibility / ErrSourceInternal
	UpdateVisibility(ctx context.Context, orgID, sourceID, callerUserID uint64, req dto.UpdateVisibilityRequest) (*dto.SourceResponse, error)

	// DeleteSource 删除 source。仅 source owner 可调用;前置:该 source 下所有 doc 已清空。
	// 删除流程:
	//   1. 加载 source(验证归属、存在)
	//   2. owner-only 硬规则校验
	//   3. DocumentCounter 计数该 source 下尚存 doc;>0 → ErrSourceHasDocuments
	//   4. 批量撤销挂在该 source 上的所有 ACL 行(permission 模块同事务写 audit)
	//   5. 删 source 本体(同事务写 source.delete audit)
	//
	// 可能的错误:
	//   - ErrSourceNotFound / ErrSourceForbidden / ErrSourceHasDocuments / ErrSourceInternal
	DeleteSource(ctx context.Context, orgID, sourceID, callerUserID uint64) error

	// SetDocumentCounter 晚绑定注入 doc 计数能力。document 模块依赖 PG,装配顺序上晚于
	// source service,因此走 setter;未设置 → DeleteSource 视作"该 source 下没有 doc"放行。
	SetDocumentCounter(dc DocumentCounter)

	// SetGitLabDeps 晚绑定注入 GitLab 集成所需的依赖。任一为 nil → GitLab 三方法返 ErrSourceInternal。
	// 装配顺序:asyncjob.Service / user_integration repo 装好后调一次。
	SetGitLabDeps(uiStore UserIntegrationStore, enqueuer GitLabSyncEnqueuer, factory GitLabClientFactory)

	// SetPublicBaseURL 服务对外可达根 URL(无尾 "/"),用于在 CreateGitLabSource 响应里拼完整
	// webhook URL。空串允许 — 前端会 fallback 到 window.location.origin 并显示警告。
	SetPublicBaseURL(baseURL string)

	// SetAsyncJobLookup 注入 asyncjob 查询能力。GetGitLabSyncStatus 走它。
	SetAsyncJobLookup(j AsyncJobLookup)

	// ─── ACL 管理(M3) ─────────────────────────────────────────────────────

	// GrantSourceACL 给某 source 加一条 ACL(group/user → read/write)。仅 source owner 可。
	//
	// 校验:
	//   - subject_type ∈ {group, user};permission ∈ {read, write}
	//   - subject_type=group:目标 group 存在且属于该 org
	//   - subject_type=user:目标 user 是该 org 的成员
	//   - 不允许给 source owner 自己授权(owner 隐式 admin)
	//   - 同 (source, subject) 已有 ACL → 409,提示用 PATCH
	GrantSourceACL(ctx context.Context, orgID, sourceID, callerUserID uint64, req dto.GrantSourceACLRequest) (*dto.SourceACLResponse, error)

	// ListSourceACL 列某 source 上的所有 ACL 行。任何 org 成员可查(用于前端权限管理 UI)。
	ListSourceACL(ctx context.Context, orgID, sourceID uint64) (*dto.ListSourceACLResponse, error)

	// UpdateSourceACL 改某条 ACL 的 permission(read↔write)。仅 source owner 可。
	UpdateSourceACL(ctx context.Context, orgID, sourceID, aclID, callerUserID uint64, req dto.UpdateSourceACLRequest) (*dto.SourceACLResponse, error)

	// RevokeSourceACL 删某条 ACL 行。仅 source owner 可。
	RevokeSourceACL(ctx context.Context, orgID, sourceID, aclID, callerUserID uint64) error

	// ─── GitLab 同步源(integration.gitlab.manage perm) ──────────────────────

	// CreateGitLabSource 创建一条 kind=gitlab_repo 的同步源,callerUserID 自动成为 owner。
	//
	// 流程:
	//   1. 校验 visibility / branch / project_id 取值
	//   2. 调 GitLab `/api/v4/user` 验 PAT 有效 → 拿到 GitLab user id 作 external_account_id
	//   3. upsert user_integrations(provider="gitlab",external_account_id=GitLab user id,access_token=PAT)
	//   4. 调 GitLab `/api/v4/projects/<project_id>` 校验 owner 凭据可读该 project + 取 path_with_namespace
	//   5. 生成 webhook secret 明文 + SHA-256 hash
	//   6. CreateSource(kind=gitlab_repo, external_ref=project_id, name=path_with_namespace, gitlab_*)
	//   7. EnqueueFullSync(orgID, callerUserID, sourceID) → 拿 job_id
	//
	// 响应 webhook secret 明文**只在创建时返一次**;DB 存 hash。
	//
	// 可能的错误:
	//   - ErrSourceInvalidVisibility / ErrSourceInvalidRequest(branch / project_id 校验)
	//   - ErrSourceGitLabAuthFailed:PAT 401
	//   - ErrSourceGitLabRepoNotFound:GitLab 凭据看不到该 project
	//   - ErrSourceGitLabUpstream:GitLab 5xx / 网络
	//   - ErrSourceNameExists:owner 同 path_with_namespace 已建过同 source
	//   - ErrSourceInternal
	CreateGitLabSource(ctx context.Context, orgID, callerUserID uint64, req dto.CreateGitLabSourceRequest) (*dto.CreateGitLabSourceResponse, error)

	// DeleteGitLabSource 删除 gitlab_repo source。注意:perm gate 已在 router 层 RequirePerm 拦住;
	// 本方法不再做"必须是 owner"硬规则(perm 拥有者即可调,默认只 owner 拿到 perm)。
	//
	// 前置守卫:source 下没有 doc 才能删 — 同 manual_upload 走 docCounter。
	// 删除会顺带清 ACL 行(走现有 DeleteSource 路径)。
	DeleteGitLabSource(ctx context.Context, orgID, sourceID, callerUserID uint64) error

	// TriggerGitLabResync 重新触发该 source 的全量同步。同 EnqueueFullSync 幂等(同 source 已 active 任务复用)。
	//
	// 可能的错误:
	//   - ErrSourceNotFound:source 不存在 / 不属于该 org / 不是 gitlab_repo
	//   - ErrSourceInternal:enqueue 失败
	TriggerGitLabResync(ctx context.Context, orgID, sourceID, callerUserID uint64) (*dto.TriggerResyncResponse, error)

	// GetGitLabSyncStatus 查指定 source 当前 / 最近一次 GitLab sync 任务的状态。
	// 前端轮询此端点展示进度。从未同步过 → 返 status="never" + 其他字段零值。
	GetGitLabSyncStatus(ctx context.Context, orgID, sourceID, callerUserID uint64) (*dto.GitLabSyncStatusResponse, error)

	// HandleGitLabWebhook 处理 GitLab webhook 推送。**不**鉴权 user(GitLab 不会发 user token);
	// 只验签:sha256(headerToken) == source.gitlab_webhook_secret_hash。
	//
	// 行为:
	//   1. 反查 source(sourceID 不存在 → ErrSourceNotFound;非 gitlab_repo → ErrSourceNotFound,
	//      避免侧信道泄露 source kind)
	//   2. 校验 X-Gitlab-Token header 的 sha256 hash(常量时间比较)
	//   3. 解析 payload(只信 ref / before / after / project.id 几个字段)
	//   4. 仅当 ref 等于 source.gitlab_branch 时入队 incremental sync(其他分支静默 ack)
	//   5. project.id 必须等于 source.external_ref(防伪)
	//   6. enqueue 用 IdempotencyKey "gitlab:<source_id>:incr:<after_sha>" — 同一 push 重发不重跑
	//
	// 返 (jobID, accepted, err):
	//   - accepted=true 表示已入队(jobID 是新或复用的 job id)
	//   - accepted=false + err==nil 表示静默 ack(分支不匹配 / push 删除分支等无操作场景)
	//   - err != nil:验签失败 / payload 非法 / DB 错
	HandleGitLabWebhook(ctx context.Context, sourceID uint64, headerToken string, eventBody []byte) (jobID uint64, accepted bool, err error)
}

// ─── 实现 ────────────────────────────────────────────────────────────────────

type sourceService struct {
	repo       repository.Repository
	aclOps     ACLOps              // M3 ACL 操作(实现注入,通常是 permission/repository)
	subjectVal SubjectValidator    // M3 ACL 授权目标合法性校验
	permFilter VisibleSourceFilter // ListSources(scope=visible) 用;nil 时降级为 all
	docCounter DocumentCounter     // DeleteSource 前置守卫;nil → 视作 0 条 doc
	uiStore    UserIntegrationStore // GitLab 集成(CreateGitLabSource 用);nil → GitLab 端点不可用
	enqueuer   GitLabSyncEnqueuer  // GitLab 同步任务入队(CreateGitLabSource / Resync 用);nil 同上
	gitLabFactory GitLabClientFactory // 构造 GitLab REST 客户端;nil → GitLab 端点不可用
	jobLookup  AsyncJobLookup       // 查 GitLab sync 任务状态;nil → GetGitLabSyncStatus 不可用
	publicBaseURL string             // 服务对外根 URL(无尾 /),用于拼 webhook URL;空 → 前端 fallback
	logger     logger.LoggerInterface
}

// GitLabClientFactory 给 service 层"按 (baseURL, PAT) 拿 GitLab 客户端"的能力。
// 装配层用 internal/integration/gitlab.New 直接绑;抽 factory 是为了 service 测试时注入 fake。
type GitLabClientFactory func(baseURL, pat string) GitLabClient

// GitLabClient service 层用到的 GitLab 客户端方法子集。
//
// 实际实现 internal/integration/gitlab.Client 是其超集;声明本接口的目的是允许
// CreateGitLabSource 单测时注入 mock,而不必启 HTTP server。
type GitLabClient interface {
	VerifyToken(ctx context.Context) (*gitlab.User, error)
	GetProject(ctx context.Context, projectID string) (*gitlab.Project, error)
}

// NewSourceService 构造一个 SourceService 实例。
//
// aclOps / subjectVal 在 M3 用于 source ACL 管理;若调用方暂不需要 ACL(测试)
// 可传 nil,但调用 ACL 方法会返 ErrSourceInternal。
//
// permFilter 给 visible-scope 列表过滤用;nil 时 visible 自动降级为 all。
//
// GitLab 相关依赖(uiStore / enqueuer / gitLabFactory)走 setter 晚绑定,装配顺序:
// asyncjob.Service / user_integration repo 装好后再调 SetGitLabDeps —— 避免本构造签名继续膨胀。
func NewSourceService(
	repo repository.Repository,
	aclOps ACLOps,
	subjectVal SubjectValidator,
	permFilter VisibleSourceFilter,
	log logger.LoggerInterface,
) SourceService {
	return &sourceService{repo: repo, aclOps: aclOps, subjectVal: subjectVal, permFilter: permFilter, logger: log}
}

// SetGitLabDeps 装配期注入 GitLab 集成依赖。任何一个为 nil 都会让 GitLab 三方法返 ErrSourceInternal。
// 调用方在 main.go 装配完 asyncjob.Service / user_integration repo 后调一次。
func (s *sourceService) SetGitLabDeps(uiStore UserIntegrationStore, enqueuer GitLabSyncEnqueuer, factory GitLabClientFactory) {
	s.uiStore = uiStore
	s.enqueuer = enqueuer
	s.gitLabFactory = factory
}

// SetPublicBaseURL 装配期注入服务对外根 URL(无尾 "/")。空串允许 — 前端 fallback。
func (s *sourceService) SetPublicBaseURL(baseURL string) {
	s.publicBaseURL = strings.TrimRight(baseURL, "/")
}

// SetAsyncJobLookup 装配期注入 asyncjob 查询能力。
func (s *sourceService) SetAsyncJobLookup(j AsyncJobLookup) {
	s.jobLookup = j
}

// CreateCustomSource 见接口注释。
func (s *sourceService) CreateCustomSource(ctx context.Context, orgID, callerUserID uint64, req dto.CreateSourceRequest) (*dto.SourceResponse, error) {
	name, err := normalizeSourceName(req.Name)
	if err != nil {
		s.logger.WarnCtx(ctx, "source name 非法", map[string]any{"org_id": orgID, "caller": callerUserID, "name": req.Name})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	visibility := req.Visibility
	if visibility == "" {
		visibility = model.VisibilityOrg
	}
	if !model.IsValidVisibility(visibility) {
		s.logger.WarnCtx(ctx, "visibility 取值非法", map[string]any{"value": visibility})
		return nil, fmt.Errorf("invalid visibility: %w", source.ErrSourceInvalidVisibility)
	}

	// 重名预检(DB uk_sources_owner_name 兜底)
	if existing, findErr := s.repo.FindSourceByOwnerAndName(ctx, orgID, callerUserID, name); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "source name 已被占用", map[string]any{"org_id": orgID, "owner": callerUserID, "name": name})
		return nil, fmt.Errorf("source name taken: %w", source.ErrSourceNameExists)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 source name 失败", findErr, map[string]any{"org_id": orgID, "owner": callerUserID, "name": name})
		return nil, fmt.Errorf("check source name: %w: %w", findErr, source.ErrSourceInternal)
	}

	src := &model.Source{
		OrgID:       orgID,
		Kind:        model.KindCustom,
		OwnerUserID: callerUserID,
		ExternalRef: "",
		Name:        name,
		Visibility:  visibility,
	}
	if err := s.repo.CreateSource(ctx, src); err != nil {
		s.logger.ErrorCtx(ctx, "创建 custom source 失败", err, map[string]any{"org_id": orgID, "owner": callerUserID, "name": name})
		return nil, fmt.Errorf("create custom source: %w: %w", err, source.ErrSourceInternal)
	}
	s.logger.InfoCtx(ctx, "custom source 创建成功", map[string]any{"org_id": orgID, "source_id": src.ID, "name": name})
	resp := sourceToDTO(src)
	return &resp, nil
}

// EnsureManualUpload 幂等确保 manual_upload source。
func (s *sourceService) EnsureManualUpload(ctx context.Context, orgID, userID uint64) (*dto.SourceResponse, error) {
	src, created, err := s.repo.EnsureManualUploadSource(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "ensure manual_upload source 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("ensure manual_upload: %w: %w", err, source.ErrSourceInternal)
	}
	if created {
		s.logger.InfoCtx(ctx, "lazy 创建 manual_upload source", map[string]any{"org_id": orgID, "user_id": userID, "source_id": src.ID})
	}
	resp := sourceToDTO(src)
	return &resp, nil
}

// GetSource 查单个 source(确认属于该 org)。
func (s *sourceService) GetSource(ctx context.Context, orgID, sourceID uint64) (*dto.SourceResponse, error) {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	resp := sourceToDTO(src)
	return &resp, nil
}

// ListSources 分页列 org 下的 source(按 scope 过滤)。
//
// scope=visible(默认):先调 permFilter.VisibleSourceIDsInOrg(read 权限)算可见 id,
// 再用 IN 过滤;空集合直接返空(不打 DB)。permFilter 为 nil → 退化为 all。
//
// scope=all:不带 IN 过滤,纯 org 内列。
func (s *sourceService) ListSources(ctx context.Context, orgID, callerUserID uint64, scope ListScope, kindFilter string, page, size int) (*dto.ListSourcesResponse, error) {
	page, size = normalizePaging(page, size)
	if kindFilter != "" && !model.IsValidKind(kindFilter) {
		s.logger.WarnCtx(ctx, "kind 过滤值非法", map[string]any{"org_id": orgID, "kind": kindFilter})
		return nil, fmt.Errorf("invalid kind: %w", source.ErrSourceInvalidKind)
	}

	var idsFilter []uint64
	if scope == ListScopeVisible {
		if s.permFilter == nil {
			s.logger.WarnCtx(ctx, "permFilter 未注入,scope=visible 降级为 all", map[string]any{"org_id": orgID})
		} else {
			ids, err := s.permFilter.VisibleSourceIDsInOrg(ctx, orgID, callerUserID, "read")
			if err != nil {
				s.logger.ErrorCtx(ctx, "VisibleSourceIDsInOrg 失败", err, map[string]any{"org_id": orgID, "user_id": callerUserID})
				return nil, fmt.Errorf("visible filter: %w: %w", err, source.ErrSourceInternal)
			}
			// 空 slice 也要传(repo 层应短路返空,而不是无 IN 子句)
			if ids == nil {
				ids = []uint64{}
			}
			idsFilter = ids
		}
	}

	items, total, err := s.repo.ListSourcesByOrg(ctx, orgID, kindFilter, idsFilter, page, size)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列 org source 失败", err, map[string]any{"org_id": orgID})
		return nil, fmt.Errorf("list sources: %w: %w", err, source.ErrSourceInternal)
	}
	out := make([]dto.SourceResponse, 0, len(items))
	for _, sr := range items {
		out = append(out, sourceToDTO(sr))
	}
	return &dto.ListSourcesResponse{Items: out, Total: total, Page: page, Size: size}, nil
}

// ListMySources 列出某 user 在某 org 中作为 owner 的所有 source。
func (s *sourceService) ListMySources(ctx context.Context, orgID, userID uint64) ([]dto.SourceResponse, error) {
	items, err := s.repo.ListSourcesByOwner(ctx, orgID, userID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列我的 source 失败", err, map[string]any{"org_id": orgID, "user_id": userID})
		return nil, fmt.Errorf("list my sources: %w: %w", err, source.ErrSourceInternal)
	}
	out := make([]dto.SourceResponse, 0, len(items))
	for _, sr := range items {
		out = append(out, sourceToDTO(sr))
	}
	return out, nil
}

// FilterIDsByKinds 见接口注释。
func (s *sourceService) FilterIDsByKinds(ctx context.Context, orgID uint64, ids []uint64, kinds []string) ([]uint64, error) {
	out, err := s.repo.ListSourceIDsByKindsInIDs(ctx, orgID, kinds, ids)
	if err != nil {
		s.logger.ErrorCtx(ctx, "filter source ids by kinds 失败", err, map[string]any{
			"org_id": orgID, "kinds": kinds, "ids_len": len(ids),
		})
		return nil, fmt.Errorf("filter ids by kinds: %w: %w", err, source.ErrSourceInternal)
	}
	return out, nil
}

// UpdateVisibility 改 source 的 visibility。仅 owner 可调用。
func (s *sourceService) UpdateVisibility(ctx context.Context, orgID, sourceID, callerUserID uint64, req dto.UpdateVisibilityRequest) (*dto.SourceResponse, error) {
	if !model.IsValidVisibility(req.Visibility) {
		s.logger.WarnCtx(ctx, "visibility 取值非法", map[string]any{"source_id": sourceID, "value": req.Visibility})
		return nil, fmt.Errorf("invalid visibility: %w", source.ErrSourceInvalidVisibility)
	}
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非 source owner 尝试改 visibility", map[string]any{"source_id": sourceID, "caller": callerUserID, "owner": src.OwnerUserID})
		return nil, fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}
	if err := s.repo.UpdateSourceVisibility(ctx, sourceID, req.Visibility); err != nil {
		s.logger.ErrorCtx(ctx, "更新 source visibility 失败", err, map[string]any{"source_id": sourceID})
		return nil, fmt.Errorf("update visibility: %w: %w", err, source.ErrSourceInternal)
	}
	src.Visibility = req.Visibility
	s.logger.InfoCtx(ctx, "source visibility 已更新", map[string]any{"source_id": sourceID, "new": req.Visibility})
	resp := sourceToDTO(src)
	return &resp, nil
}

// DeleteSource 见接口注释。
func (s *sourceService) DeleteSource(ctx context.Context, orgID, sourceID, callerUserID uint64) error {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非 source owner 尝试删除 source", map[string]any{"source_id": sourceID, "caller": callerUserID, "owner": src.OwnerUserID})
		return fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}

	// 前置:确认该 source 下没有 doc。docCounter 未注入(PG 未装配)→ 视作 0 放行。
	if s.docCounter != nil {
		cnt, err := s.docCounter.CountBySource(ctx, orgID, sourceID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "统计 source 下 doc 失败", err, map[string]any{"source_id": sourceID})
			return fmt.Errorf("count docs by source: %w: %w", err, source.ErrSourceInternal)
		}
		if cnt > 0 {
			s.logger.WarnCtx(ctx, "source 下仍有 doc,拒绝删除", map[string]any{"source_id": sourceID, "doc_count": cnt})
			return fmt.Errorf("source has %d docs: %w", cnt, source.ErrSourceHasDocuments)
		}
	}

	// 先清挂在该 source 上的 ACL(按每行写 audit)。跨事务:若此步成功、下一步删 source 失败,
	// 重试时 BulkRevoke 对空集合返回 0,不会重复;doc 计数仍为 0,删除可继续 —— 重试安全。
	if s.aclOps != nil {
		if _, err := s.aclOps.BulkRevokeACLsByResource(ctx,
			permmodel.ACLResourceTypeSource, sourceID,
			model.AuditActionSourceACLRevoke, model.AuditTargetSourceACL,
		); err != nil {
			s.logger.ErrorCtx(ctx, "批量撤销 source ACL 失败", err, map[string]any{"source_id": sourceID})
			return fmt.Errorf("bulk revoke acls: %w: %w", err, source.ErrSourceInternal)
		}
	}

	if err := s.repo.DeleteSource(ctx, sourceID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("source gone: %w", source.ErrSourceNotFound)
		}
		s.logger.ErrorCtx(ctx, "删除 source 失败", err, map[string]any{"source_id": sourceID})
		return fmt.Errorf("delete source: %w: %w", err, source.ErrSourceInternal)
	}
	s.logger.InfoCtx(ctx, "source 已删除", map[string]any{"source_id": sourceID, "owner": callerUserID})
	return nil
}

// SetDocumentCounter 见接口注释。
func (s *sourceService) SetDocumentCounter(dc DocumentCounter) { s.docCounter = dc }

// ─── 内部工具 ────────────────────────────────────────────────────────────────

// loadSource 按 (org_id, source_id) 加载,翻译 NotFound + 跨 org 越界为 ErrSourceNotFound。
func (s *sourceService) loadSource(ctx context.Context, orgID, sourceID uint64) (*model.Source, error) {
	src, err := s.repo.FindSourceByID(ctx, sourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.WarnCtx(ctx, "source 不存在", map[string]any{"source_id": sourceID})
			return nil, fmt.Errorf("find source: %w", source.ErrSourceNotFound)
		}
		s.logger.ErrorCtx(ctx, "查 source 失败", err, map[string]any{"source_id": sourceID})
		return nil, fmt.Errorf("find source: %w: %w", err, source.ErrSourceInternal)
	}
	if src.OrgID != orgID {
		s.logger.WarnCtx(ctx, "source 不属于该 org", map[string]any{"source_id": sourceID, "wanted_org": orgID, "actual_org": src.OrgID})
		return nil, fmt.Errorf("cross-org access: %w", source.ErrSourceNotFound)
	}
	return src, nil
}

// normalizeSourceName 校验并标准化 source 名称(去首尾空白 + 长度检查)。
func normalizeSourceName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("source name empty: %w", source.ErrSourceInvalidName)
	}
	if len(name) > source.MaxSourceNameLength {
		return "", fmt.Errorf("source name too long: %w", source.ErrSourceInvalidName)
	}
	return name, nil
}

// normalizePaging 钳位 page / size 到合法区间。
func normalizePaging(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = source.DefaultPageSize
	}
	if size > source.MaxPageSize {
		size = source.MaxPageSize
	}
	return page, size
}

// ─── ACL 管理实现 ────────────────────────────────────────────────────────────

// GrantSourceACL 见接口注释。
func (s *sourceService) GrantSourceACL(ctx context.Context, orgID, sourceID, callerUserID uint64, req dto.GrantSourceACLRequest) (*dto.SourceACLResponse, error) {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非 source owner 尝试授权", map[string]any{"source_id": sourceID, "caller": callerUserID})
		return nil, fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}
	// 取值校验
	if !permmodel.IsValidACLSubjectType(req.SubjectType) {
		return nil, fmt.Errorf("invalid subject type: %w", permission.ErrACLInvalidSubjectType)
	}
	if !permmodel.IsValidACLPermission(req.Permission) {
		return nil, fmt.Errorf("invalid permission: %w", permission.ErrACLInvalidPermission)
	}
	// owner 不能给自己授权
	if req.SubjectType == permmodel.ACLSubjectTypeUser && req.SubjectID == src.OwnerUserID {
		s.logger.WarnCtx(ctx, "拒绝给 source owner 自己授权", map[string]any{"source_id": sourceID, "owner": src.OwnerUserID})
		return nil, fmt.Errorf("cannot grant to owner: %w", permission.ErrACLOnOwnSubject)
	}
	// subject 合法性校验
	if err := s.validateSubject(ctx, orgID, req.SubjectType, req.SubjectID); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// 重复预检
	if existing, findErr := s.aclOps.FindACL(ctx, permmodel.ACLResourceTypeSource, sourceID, req.SubjectType, req.SubjectID); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "ACL 已存在", map[string]any{"source_id": sourceID, "subject_type": req.SubjectType, "subject_id": req.SubjectID})
		return nil, fmt.Errorf("acl exists: %w", permission.ErrACLExists)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查 ACL 失败", findErr, nil)
		return nil, fmt.Errorf("check acl: %w: %w", findErr, source.ErrSourceInternal)
	}

	acl := &permmodel.ResourceACL{
		OrgID:        orgID,
		ResourceType: permmodel.ACLResourceTypeSource,
		ResourceID:   sourceID,
		SubjectType:  req.SubjectType,
		SubjectID:    req.SubjectID,
		Permission:   req.Permission,
		GrantedBy:    callerUserID,
	}
	if err := s.aclOps.GrantACL(ctx, acl, model.AuditActionSourceACLGrant, model.AuditTargetSourceACL); err != nil {
		s.logger.ErrorCtx(ctx, "grant acl 失败", err, map[string]any{"source_id": sourceID})
		return nil, fmt.Errorf("grant acl: %w: %w", err, source.ErrSourceInternal)
	}
	s.logger.InfoCtx(ctx, "ACL 授权成功", map[string]any{"source_id": sourceID, "acl_id": acl.ID, "subject_type": req.SubjectType, "subject_id": req.SubjectID, "permission": req.Permission})
	resp := aclToDTO(acl)
	return &resp, nil
}

// ListSourceACL 见接口注释。
func (s *sourceService) ListSourceACL(ctx context.Context, orgID, sourceID uint64) (*dto.ListSourceACLResponse, error) {
	if _, err := s.loadSource(ctx, orgID, sourceID); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	rows, err := s.aclOps.ListACLByResource(ctx, permmodel.ACLResourceTypeSource, sourceID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "列 ACL 失败", err, map[string]any{"source_id": sourceID})
		return nil, fmt.Errorf("list acl: %w: %w", err, source.ErrSourceInternal)
	}
	out := make([]dto.SourceACLResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, aclToDTO(r))
	}
	return &dto.ListSourceACLResponse{Items: out}, nil
}

// UpdateSourceACL 见接口注释。
func (s *sourceService) UpdateSourceACL(ctx context.Context, orgID, sourceID, aclID, callerUserID uint64, req dto.UpdateSourceACLRequest) (*dto.SourceACLResponse, error) {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非 source owner 尝试改 ACL", map[string]any{"source_id": sourceID, "caller": callerUserID})
		return nil, fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}
	if !permmodel.IsValidACLPermission(req.Permission) {
		return nil, fmt.Errorf("invalid permission: %w", permission.ErrACLInvalidPermission)
	}
	acl, err := s.loadACLForSource(ctx, sourceID, aclID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if err := s.aclOps.UpdateACLPermission(ctx, aclID, req.Permission, model.AuditActionSourceACLUpdate, model.AuditTargetSourceACL); err != nil {
		s.logger.ErrorCtx(ctx, "改 ACL permission 失败", err, map[string]any{"acl_id": aclID})
		return nil, fmt.Errorf("update acl: %w: %w", err, source.ErrSourceInternal)
	}
	acl.Permission = req.Permission
	s.logger.InfoCtx(ctx, "ACL permission 已更新", map[string]any{"source_id": sourceID, "acl_id": aclID, "new": req.Permission})
	resp := aclToDTO(acl)
	return &resp, nil
}

// RevokeSourceACL 见接口注释。
func (s *sourceService) RevokeSourceACL(ctx context.Context, orgID, sourceID, aclID, callerUserID uint64) error {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "非 source owner 尝试 revoke ACL", map[string]any{"source_id": sourceID, "caller": callerUserID})
		return fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}
	if _, err := s.loadACLForSource(ctx, sourceID, aclID); err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if err := s.aclOps.RevokeACL(ctx, aclID, model.AuditActionSourceACLRevoke, model.AuditTargetSourceACL); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("acl not found: %w", permission.ErrACLNotFound)
		}
		s.logger.ErrorCtx(ctx, "revoke ACL 失败", err, map[string]any{"acl_id": aclID})
		return fmt.Errorf("revoke acl: %w: %w", err, source.ErrSourceInternal)
	}
	s.logger.InfoCtx(ctx, "ACL 已撤销", map[string]any{"source_id": sourceID, "acl_id": aclID})
	return nil
}

// validateSubject 校验 (subject_type, subject_id) 在该 org 内合法。
func (s *sourceService) validateSubject(ctx context.Context, orgID uint64, subjectType string, subjectID uint64) error {
	switch subjectType {
	case permmodel.ACLSubjectTypeGroup:
		ok, err := s.subjectVal.GroupExistsInOrg(ctx, orgID, subjectID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "校验 group 存在失败", err, map[string]any{"org_id": orgID, "group_id": subjectID})
			return fmt.Errorf("group check: %w: %w", err, source.ErrSourceInternal)
		}
		if !ok {
			return fmt.Errorf("group not in org: %w", permission.ErrACLSubjectNotFound)
		}
	case permmodel.ACLSubjectTypeUser:
		ok, err := s.subjectVal.UserIsOrgMember(ctx, orgID, subjectID)
		if err != nil {
			s.logger.ErrorCtx(ctx, "校验 user 是 org 成员失败", err, map[string]any{"org_id": orgID, "user_id": subjectID})
			return fmt.Errorf("user check: %w: %w", err, source.ErrSourceInternal)
		}
		if !ok {
			return fmt.Errorf("user not in org: %w", permission.ErrACLSubjectNotFound)
		}
	default:
		return fmt.Errorf("invalid subject type: %w", permission.ErrACLInvalidSubjectType)
	}
	return nil
}

// loadACLForSource 加载 ACL 行,确认其 resource_type='source' 且 resource_id 匹配(防越权)。
func (s *sourceService) loadACLForSource(ctx context.Context, sourceID, aclID uint64) (*permmodel.ResourceACL, error) {
	acl, err := s.aclOps.FindACLByID(ctx, aclID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("acl not found: %w", permission.ErrACLNotFound)
		}
		s.logger.ErrorCtx(ctx, "查 ACL 失败", err, map[string]any{"acl_id": aclID})
		return nil, fmt.Errorf("find acl: %w: %w", err, source.ErrSourceInternal)
	}
	if acl.ResourceType != permmodel.ACLResourceTypeSource || acl.ResourceID != sourceID {
		s.logger.WarnCtx(ctx, "ACL 不属于该 source", map[string]any{"acl_id": aclID, "source_id": sourceID, "actual_resource_id": acl.ResourceID})
		return nil, fmt.Errorf("acl not for this source: %w", permission.ErrACLNotFound)
	}
	return acl, nil
}
