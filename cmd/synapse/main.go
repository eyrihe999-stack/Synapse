// Synapse main server.
//
// 网关模式 —— 业务模块已全部下线,本 binary 现阶段只装配底层基础设施:
//
//   - user:身份 + 登录(含 Google OIDC 社交登录)
//   - organization:多租户命名空间(orgs + org_members)
//   - asyncjob framework:迁移 + 框架代码保留,runners 已删
//   - middleware:HTTP 通用中间件
//   - ingestion framework:只留接口 + pipeline 骨架
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob"
	asynchandler "github.com/eyrihe999-stack/Synapse/internal/asyncjob/handler"
	asyncrepo "github.com/eyrihe999-stack/Synapse/internal/asyncjob/repository"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/runners/docupload"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/runners/gitlabsync"
	asyncsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	dochandler "github.com/eyrihe999-stack/Synapse/internal/document/handler"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	docservice "github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	codechunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/code"
	gocodebackend "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/code/golang"
	mdchunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/markdown"
	plainchunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/plaintext"
	docpersister "github.com/eyrihe999-stack/Synapse/internal/ingestion/persister/document"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/channel"
	channelhandler "github.com/eyrihe999-stack/Synapse/internal/channel/handler"
	channelrepo "github.com/eyrihe999-stack/Synapse/internal/channel/repository"
	channelsvc "github.com/eyrihe999-stack/Synapse/internal/channel/service"
	"github.com/eyrihe999-stack/Synapse/internal/channel/uploadtoken"
	"github.com/eyrihe999-stack/Synapse/internal/pm"
	pmhandler "github.com/eyrihe999-stack/Synapse/internal/pm/handler"
	pmrepo "github.com/eyrihe999-stack/Synapse/internal/pm/repository"
	pmsvc "github.com/eyrihe999-stack/Synapse/internal/pm/service"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgrepo "github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	orgsvc "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
	"github.com/eyrihe999-stack/Synapse/internal/principal"
	permhandler "github.com/eyrihe999-stack/Synapse/internal/permission/handler"
	permrepo "github.com/eyrihe999-stack/Synapse/internal/permission/repository"
	permsvc "github.com/eyrihe999-stack/Synapse/internal/permission/service"
	"github.com/eyrihe999-stack/Synapse/internal/source"
	srchandler "github.com/eyrihe999-stack/Synapse/internal/source/handler"
	srcrepo "github.com/eyrihe999-stack/Synapse/internal/source/repository"
	srcsvc "github.com/eyrihe999-stack/Synapse/internal/source/service"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	userhandler "github.com/eyrihe999-stack/Synapse/internal/user/handler"
	userrepo "github.com/eyrihe999-stack/Synapse/internal/user/repository"
	usersvc "github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/internal/agents"
	agenthandler "github.com/eyrihe999-stack/Synapse/internal/agents/handler"
	agentrepo "github.com/eyrihe999-stack/Synapse/internal/agents/repository"
	agentsvc "github.com/eyrihe999-stack/Synapse/internal/agents/service"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/prompts"
	agentsysrepo "github.com/eyrihe999-stack/Synapse/internal/agentsys/repository"
	agentsysruntime "github.com/eyrihe999-stack/Synapse/internal/agentsys/runtime"
	"github.com/eyrihe999-stack/Synapse/internal/agentsys/scoped"
	"github.com/eyrihe999-stack/Synapse/internal/channel/eventcard"
	"github.com/eyrihe999-stack/Synapse/internal/channel/pmevent"
	"github.com/eyrihe999-stack/Synapse/internal/mcp"
	"github.com/eyrihe999-stack/Synapse/internal/oauth"
	oauthhandler "github.com/eyrihe999-stack/Synapse/internal/oauth/handler"
	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
	oauthrepo "github.com/eyrihe999-stack/Synapse/internal/oauth/repository"
	oauthsvc "github.com/eyrihe999-stack/Synapse/internal/oauth/service"
	"github.com/eyrihe999-stack/Synapse/internal/task"
	taskhandler "github.com/eyrihe999-stack/Synapse/internal/task/handler"
	taskrepo "github.com/eyrihe999-stack/Synapse/internal/task/repository"
	tasksvc "github.com/eyrihe999-stack/Synapse/internal/task/service"
	"github.com/eyrihe999-stack/Synapse/internal/transport"
	transporthandler "github.com/eyrihe999-stack/Synapse/internal/transport/handler"
	transportsvc "github.com/eyrihe999-stack/Synapse/internal/transport/service"
	gitlabclient "github.com/eyrihe999-stack/Synapse/internal/integration/gitlab"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	uirepo "github.com/eyrihe999-stack/Synapse/internal/user_integration/repository"
	"github.com/eyrihe999-stack/Synapse/internal/common/async"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
	"github.com/eyrihe999-stack/Synapse/internal/common/eventbus"
	"github.com/eyrihe999-stack/Synapse/internal/common/llm"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/oidcclient"
	"github.com/eyrihe999-stack/Synapse/internal/common/pwdpolicy"
	"github.com/eyrihe999-stack/Synapse/internal/common/idgen"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
	"github.com/eyrihe999-stack/Synapse/internal/common/response"
)

// GitSHA 通过 ldflags 在 build 时注入:`-X main.GitSHA=<sha>`。Dockerfile 里 ARG GIT_SHA 喂它。
var GitSHA = "unknown"

func main() {
	// 1. Load config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// 2. Init logger
	appLogger, err := logger.GetLogger(&cfg.Log)
	if err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer appLogger.Close()
	appLogger.Info("synapse started", map[string]interface{}{"git_sha": GitSHA})

	// 3. Connect MySQL
	db, err := database.NewGormMySQL(&cfg.Database.MySQL)
	if err != nil {
		appLogger.Fatal("failed to connect MySQL", err, nil)
	}
	appLogger.Info("MySQL connected", map[string]interface{}{
		"host": cfg.Database.MySQL.Host,
		"db":   cfg.Database.MySQL.Database,
	})

	// 4. Connect Redis
	rdb, err := database.NewRedis(&cfg.Redis)
	if err != nil {
		appLogger.Fatal("failed to connect Redis", err, nil)
	}
	defer rdb.Close()
	appLogger.Info("Redis connected", nil)

	// 4a. EventBus Publisher —— 复用 redis client,asyncjob 终态事件走这里。
	// Stream 配置来自 cfg.EventBus,默认值在 config.applyDefaults。
	eventBusPublisher := eventbus.NewRedisPublisher(rdb.GetClient(), int64(cfg.EventBus.MaxLen))
	appLogger.Info("eventbus publisher ready", map[string]interface{}{
		"asyncjob_stream": cfg.EventBus.AsyncJobStream,
		"workflow_stream": cfg.EventBus.WorkflowStream,
		"max_len":         cfg.EventBus.MaxLen,
	})

	// 4b. Connect Postgres —— 可选。保留连接通道,新 flow 实现时可直接复用;
	// 当前无模块消费,只是建立连接 + 启 pgvector extension,不 fatal。
	var pgDB *gorm.DB
	if cfg.Database.Postgres.Host != "" {
		pgDB, err = database.NewGormPostgres(&cfg.Database.Postgres)
		if err != nil {
			appLogger.Fatal("failed to connect Postgres", err, nil)
		}
		if err := database.EnablePGVectorExtension(context.Background(), pgDB); err != nil {
			appLogger.Fatal("failed to enable pgvector extension", err, nil)
		}
		appLogger.Info("Postgres + pgvector ready", map[string]interface{}{
			"host": cfg.Database.Postgres.Host,
			"db":   cfg.Database.Postgres.Database,
		})
	} else {
		appLogger.Warn("Postgres not configured; vector path disabled", nil)
	}
	_ = pgDB // reserved for new ingestion flow

	// 5. Init Snowflake
	if err := idgen.InitSnowflake(idgen.SnowflakeConfig{
		DatacenterID: cfg.Snowflake.DatacenterID,
		WorkerID:     cfg.Snowflake.WorkerID,
	}); err != nil {
		appLogger.Fatal("failed to init snowflake", err, nil)
	}

	// 6. Init JWT Manager
	jwtManager := jwt.NewJWTManager(jwt.JWTConfig{
		SecretKey:            cfg.JWT.SecretKey,
		AccessTokenDuration:  cfg.JWT.AccessTokenDuration,
		RefreshTokenDuration: cfg.JWT.RefreshTokenDuration,
		Issuer:               cfg.JWT.Issuer,
	})

	// 7. Run migrations(只剩基础设施模块;业务模块 flow 重建后再加回)
	//
	// 顺序约束:principal 先于 user / agents —— 后两者的 migration 会向 principals
	// 表回填身份根记录(详见 docs/collaboration-design.md §3.5.5)。
	ctx := context.Background()
	if err := principal.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("principal migrations failed", err, nil)
	}
	if err := user.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("user migrations failed", err, nil)
	}
	if err := organization.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("organization migrations failed", err, nil)
	}
	if err := permission.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("permission migrations failed", err, nil)
	}
	if err := source.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("source migrations failed", err, nil)
	}
	if err := asyncjob.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("asyncjob migrations failed", err, nil)
	}
	if err := user_integration.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("user_integration migrations failed", err, nil)
	}
	if err := agents.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("agents migrations failed", err, nil)
	}
	// pm 必须先于 channel 跑 —— Project / Version 的 schema 在 pm 模块管,
	// channel 模块的 channels 表(project_id 外键)依赖 projects 表先建。
	if err := pm.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("pm migrations failed", err, nil)
	}
	if err := channel.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("channel migrations failed", err, nil)
	}
	if err := task.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("task migrations failed", err, nil)
	}
	// pm 二阶段迁移:数据回填 + seed,要在 channel / task 已 ALTER 出 kind /
	// workstream_id 字段之后跑(详见 pm/migration.go RunPostMigrations 注释)。
	if err := pm.RunPostMigrations(ctx, db, appLogger); err != nil {
		appLogger.Fatal("pm post-migrations failed", err, nil)
	}
	// PR #6' agentsys:audit_events + llm_usage 两张表,顶级 agent runtime 用
	if err := agentsys.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("agentsys migrations failed", err, nil)
	}
	if err := oauth.RunMigrations(ctx, db, appLogger, nil); err != nil {
		appLogger.Fatal("oauth migrations failed", err, nil)
	}
	// document migrations 依赖 PG;配置完备才跑。未配置时保持现状(warn 已在 4b 打过)。
	if pgDB != nil {
		if err := document.RunMigrations(ctx, pgDB, cfg.Embedding.Text.ModelDim, appLogger, nil); err != nil {
			appLogger.Fatal("document migrations failed", err, nil)
		}
	}

	// 7a. OSS client 装配。document upload 链路强依赖 —— 缺失直接 fatal(生产无意义降级)。
	ossClient, err := ossupload.New(ossupload.Config{
		AccessKeyID:     cfg.OSS.AccessKeyID,
		AccessKeySecret: cfg.OSS.AccessKeySecret,
		Endpoint:        cfg.OSS.Endpoint,
		Region:          cfg.OSS.Region,
		Bucket:          cfg.OSS.Bucket,
		Domain:          cfg.OSS.Domain,
	})
	if err != nil {
		appLogger.Fatal("failed to init oss client", err, map[string]any{
			"bucket": cfg.OSS.Bucket, "region": cfg.OSS.Region,
		})
	}
	appLogger.Info("oss client ready", map[string]any{
		"bucket": cfg.OSS.Bucket, "region": cfg.OSS.Region, "path_prefix": cfg.OSS.PathPrefix,
	})

	// 7b. Ingestion pipeline 装配(Layer 2)。
	//     现阶段只构造,没有 Fetcher 接入 → pipeline 空跑,等 Layer 3+ 加 HTTP / async runner 时喂 fetcher。
	//     PG / embedding 任一缺失都跳过装配(pipeline = nil),上层调用方自己判空。
	//
	//     embedder / documentRepo 提到外层声明,KBQueryService(channel.service)和 MCP 也需要复用。
	var pipeline *ingestion.Pipeline
	var embedder embedding.Embedder
	var documentRepo docrepo.Repository
	if pgDB != nil {
		embedCfg := embedding.Config{
			Provider: cfg.Embedding.Text.Provider,
			ModelDim: cfg.Embedding.Text.ModelDim,
			Azure: embedding.AzureConfig{
				Endpoint:   cfg.Embedding.Text.Azure.Endpoint,
				Deployment: cfg.Embedding.Text.Azure.Deployment,
				APIKey:     cfg.Embedding.Text.Azure.APIKey,
				APIVersion: cfg.Embedding.Text.Azure.APIVersion,
			},
		}
		var err error
		embedder, err = embedding.New(embedCfg)
		if err != nil {
			appLogger.Fatal("failed to build embedder", err, nil)
		}
		mdCk := mdchunker.New(0)
		plainCk := plainchunker.New(0)
		// code chunker:按 Language 路由到对应 backend(一期只 Go;其他语言走 plaintext 兜底)。
		// 一个进程内 Chunker 实例线程安全(无状态);backend 也是。
		codeCk := codechunker.New(0, gocodebackend.New())
		selector := func(d *ingestion.NormalizedDoc) ingestion.Chunker {
			if d.SourceType != ingestion.SourceTypeDocument {
				return nil
			}
			// Language 非空(GitLab fetcher 按扩展名填)且 backend 已注册 → code chunker
			if d.Language != "" && codeCk.Supports(d.Language) {
				return codeCk
			}
			if isMarkdownDoc(d.MIMEType, d.FileName) {
				return mdCk
			}
			return plainCk
		}
		documentRepo = docrepo.New(pgDB)
		docPer, err := docpersister.New(documentRepo, appLogger)
		if err != nil {
			appLogger.Fatal("failed to build document persister", err, nil)
		}
		registry, err := ingestion.NewRegistry(selector, docPer)
		if err != nil {
			appLogger.Fatal("failed to build ingestion registry", err, nil)
		}
		pipeline, err = ingestion.NewPipeline(registry, embedder, appLogger, ingestion.DefaultPipelineConfig())
		if err != nil {
			appLogger.Fatal("failed to build ingestion pipeline", err, nil)
		}
		appLogger.Info("ingestion pipeline ready", map[string]any{
			"embed_provider": embedCfg.Provider,
			"embed_dim":      embedCfg.ModelDim,
		})
	}

	// 7c. Asyncjob service 装配(Layer 3)。
	//
	//     runners 列表依赖 pipeline —— pipeline 没装配成功(PG 缺失)时 runners 空起,
	//     Schedule 会返 ErrUnknownKind,document handler 会给 500,语义可接受(向用户明示"PG 未配置")。
	asyncJobRepo := asyncrepo.New(db)
	userIntegrationRepo := uirepo.New(db)
	sourceRepoForRunner := srcrepo.New(db) // gitlabsync runner 独立实例,避免循环依赖到 sourceSvc
	var runners []asyncsvc.Runner
	if pipeline != nil {
		uploadRunner, err := docupload.New(pipeline, ossClient, appLogger)
		if err != nil {
			appLogger.Fatal("docupload runner init failed", err, nil)
		}
		runners = append(runners, uploadRunner)

		gitlabRunner, err := gitlabsync.New(pipeline, sourceRepoForRunner, userIntegrationRepo, documentRepo, appLogger)
		if err != nil {
			appLogger.Fatal("gitlabsync runner init failed", err, nil)
		}
		runners = append(runners, gitlabRunner)
	}
	asyncJobSvc := asyncsvc.NewService(
		asyncsvc.Config{CompletionStream: cfg.EventBus.AsyncJobStream},
		asyncJobRepo, runners, eventBusPublisher, appLogger,
	)
	asyncJobSvc.ReapStale(ctx)

	// 8. Repositories
	userRepo := userrepo.New(db)
	orgRepo := orgrepo.New(db)
	permRepo := permrepo.New(db)
	sourceRepo := srcrepo.New(db)

	// 9. Sub-services 需要的 stores / helpers
	sessionStore := usersvc.NewSessionStore(rdb, appLogger)
	emailCodeStore := usersvc.NewEmailCodeStore(rdb, appLogger)
	emailSender := email.NewSender(&cfg.Email, appLogger)
	loginGuard := usersvc.NewLoginGuard(rdb, appLogger)
	pwdResetStore := usersvc.NewPasswordResetStore(rdb, appLogger)

	pwdPolicyOpts := []pwdpolicy.Option{pwdpolicy.WithMinLen(cfg.User.Password.MinLen)}
	if cfg.User.Password.CheckWeakList != nil {
		pwdPolicyOpts = append(pwdPolicyOpts, pwdpolicy.WithWeakListCheck(*cfg.User.Password.CheckWeakList))
	}
	pwdPolicy, err := pwdpolicy.New(pwdPolicyOpts...)
	if err != nil {
		appLogger.Fatal("failed to init password policy", err, nil)
	}
	oauthExchangeStore := usersvc.NewOAuthExchangeStore(rdb, appLogger)
	emailVerifyStore := usersvc.NewEmailVerifyStore(rdb, appLogger)

	var googleOIDC *oidcclient.GoogleClient
	if cfg.OAuthLogin.Google.Enabled {
		googleOIDC, err = oidcclient.NewGoogleClient(
			context.Background(),
			cfg.OAuthLogin.Google.ClientID,
			cfg.OAuthLogin.Google.ClientSecret,
			cfg.OAuthLogin.Google.RedirectURI,
			[]byte(cfg.OAuthLogin.StateCookieSecret),
		)
		if err != nil {
			appLogger.Fatal("failed to init google oidc client", err, nil)
		}
		appLogger.Info("google oauth login enabled", map[string]interface{}{
			"redirect_uri": cfg.OAuthLogin.Google.RedirectURI,
		})
	}

	// 10. User + Organization services
	userSvc := usersvc.NewUserService(userRepo, jwtManager, sessionStore, cfg.JWT.MaxSessionsPerUser, appLogger, emailCodeStore, emailSender, &cfg.Email, loginGuard, &cfg.User, pwdResetStore, pwdPolicy, oauthExchangeStore, emailVerifyStore)

	orgSvcCfg := orgsvc.Config{
		MaxOwnedOrgs:  cfg.Organization.MaxOwnedOrgs,
		MaxJoinedOrgs: cfg.Organization.MaxJoinedOrgs,
	}
	orgService := orgsvc.NewOrgService(orgSvcCfg, orgRepo, userSvc, appLogger)
	memberService := orgsvc.NewMemberService(orgRepo, appLogger)
	roleService := orgsvc.NewRoleService(orgRepo, appLogger)

	// 邀请流程反向依赖 user 模块:inviter display_name / accepting user email,
	// 通过 closure 适配 UserLookup 接口,避免 org/service 直接 import user/service 包。
	invitationUserLookup := orgsvc.UserLookupFunc(func(ctx context.Context, userID uint64) (*orgsvc.InviteUserInfo, error) {
		p, err := userSvc.GetProfile(ctx, userID)
		if err != nil {
			return nil, err
		}
		return &orgsvc.InviteUserInfo{
			Email:       p.Email,
			DisplayName: p.DisplayName,
			// user 资料暂无 locale 字段,先跟全局 email.locale 走;后续接入多语言时从 profile 读。
			Locale: cfg.Email.Locale,
		}, nil
	})
	inviteGuard := orgsvc.NewInviteGuard(rdb, appLogger)
	invitationService := orgsvc.NewInvitationService(
		orgsvc.InvitationConfig{
			FrontendBaseURL: cfg.Organization.InvitationFrontendBaseURL,
		},
		orgRepo, emailSender, invitationUserLookup, inviteGuard, appLogger,
	)

	// M3.7 owner 孤儿态 guard:user 注销流程反向依赖 org 侧查询。
	userSvc.SetOwnerChecker(orgsvc.NewOwnerCheckerAdapter(orgRepo, appLogger))

	// 10b. Permission service(权限组 + 审计;依赖 org service 校验 user 是否是 org 成员)
	permGroupSvc := permsvc.NewGroupService(
		permRepo,
		permsvc.OrgMembershipCheckerFunc(orgService.IsMember),
		appLogger,
	)

	// 10c. Permission 判定 service(读 source 元信息 + ACL 表 + group_members 算 perm)
	// 必须在 sourceSvc 之前构造,因为 sourceSvc 用它做 ListSources(scope=visible) 的可见性过滤。
	sourceLookupForPerm := sourceLookupAdapter{repo: sourceRepo}
	orgRoleLookupForPerm := orgRoleLookupAdapter{repo: orgRepo}
	permJudgeSvc := permsvc.NewPermissionService(permRepo, &sourceLookupForPerm, &orgRoleLookupForPerm, appLogger)

	// 10d. Source service(知识源:权限承载者;document 上传链路 lazy 创建 manual_upload)
	//
	// 注入:
	//   - aclOps:permRepo 直接对得上 ACLOps 接口签名(GrantACL/UpdateACLPermission/RevokeACL/...)
	//   - subjectVal:闭包适配,组合 permRepo(查 group 存在) + orgService(查 user 是 org 成员)
	//   - permFilter:permJudgeSvc 实现 VisibleSourceFilter,给 ListSources(visible) 用
	sourceSubjectVal := sourceSubjectValidator{permRepo: permRepo, isMember: orgService.IsMember}
	sourceSvc := srcsvc.NewSourceService(sourceRepo, permRepo, &sourceSubjectVal, permJudgeSvc, appLogger)

	// GitLab 集成依赖晚绑定:asyncJobSvc / userIntegrationRepo 已构造完成,这里把三件套接进去。
	// gitlabFactory 用 closure 把 service.GitLabClient 接口绑到具体 client.New;装配测试时可注入 mock。
	gitlabFactory := func(baseURL, pat string) srcsvc.GitLabClient {
		return gitlabclient.New(baseURL, pat)
	}
	sourceSvc.SetGitLabDeps(userIntegrationRepo, gitlabSyncEnqueuer{svc: asyncJobSvc, log: appLogger}, gitlabFactory)
	// PublicBaseURL 优先用 Server.PublicBaseURL,空则 fallback 到 OAuth.Issuer
	// (本地 dev 接 Claude Desktop 已经给 OAuth 配过 ngrok URL,免重复)。
	publicBaseURL := cfg.Server.PublicBaseURL
	if publicBaseURL == "" {
		publicBaseURL = cfg.OAuth.Issuer
	}
	sourceSvc.SetPublicBaseURL(publicBaseURL)
	sourceSvc.SetAsyncJobLookup(asyncJobLookupAdapter{repo: asyncJobRepo})

	// 10da. PM(项目管理关系层 PR-A):Project / Initiative / Version / Workstream /
	// ProjectKBRef 五张表 + service + HTTP 路由。
	pmRepo := pmrepo.New(db)
	pmOrgChecker := pmsvc.OrgMembershipCheckerFunc(orgService.IsMember)
	pmService := pmsvc.New(
		pmsvc.Config{PMEventStream: cfg.EventBus.PMStream},
		pmRepo, pmOrgChecker, eventBusPublisher, appLogger,
	)
	pmH := pmhandler.NewHandler(pmService, appLogger)

	// 10e. Channel(collaboration Phase 1 PR #2 + PR #4' 扩展:messages / kb_refs)
	//
	// uploadSigner:OSS 直传 commit token 签名器。进程启动时随机生成 secret,重启
	// 后 in-flight token 失效(5min 内的失败,客户端重试即可)。零配置 + 无 rotate 负担。
	channelRepo := channelrepo.New(db)
	channelOrgChecker := channelsvc.OrgMembershipCheckerFunc(orgService.IsMember)
	channelPrincipalResolver := channelsvc.NewPrincipalOrgResolver(db, channelOrgChecker)
	uploadSigner, err := uploadtoken.NewSigner()
	if err != nil {
		appLogger.Fatal("failed to init channel upload signer", err, nil)
	}
	channelService := channelsvc.New(
		channelsvc.Config{
			ChannelEventStream: cfg.EventBus.ChannelStream,
			OSSPathPrefix:      cfg.OSS.PathPrefix,
		},
		channelRepo, channelOrgChecker, channelPrincipalResolver,
		eventBusPublisher, ossClient, uploadSigner,
		documentRepo, embedder, appLogger,
	)

	// 11. Handlers
	userH := userhandler.NewHandler(userSvc, appLogger, googleOIDC, cfg.OAuthLogin.Google.FrontendRedirectBase, cfg.OAuthLogin.CookieSecure)
	orgH := orghandler.NewOrgHandler(orgService, memberService, roleService, invitationService, appLogger)
	orgH.Ready.Store(true)
	permH := permhandler.NewPermHandler(permGroupSvc, appLogger)
	permH.Ready.Store(true)
	auditQuerySvc := permsvc.NewAuditQueryService(permRepo, appLogger)
	auditH := permhandler.NewAuditHandler(auditQuerySvc)
	sourceH := srchandler.NewSourceHandler(sourceSvc, appLogger)
	sourceH.Ready.Store(true)
	channelH := channelhandler.NewHandler(channelService, appLogger)

	// 10f. Task(PR #4'):channel 内结构化任务
	taskRepo := taskrepo.New(db)
	taskService := tasksvc.New(
		tasksvc.Config{
			OSSPathPrefix:   cfg.OSS.PathPrefix,
			TaskEventStream: cfg.EventBus.TaskStream,
		},
		taskRepo, ossClient, eventBusPublisher, appLogger,
	)
	taskH := taskhandler.NewHandler(taskService, appLogger)

	// asyncjob + document handlers(Layer 3)。document 需要 PG;缺失时路由不挂,HTTP 触不到。
	// 11a. Transport (agent WS 网关) + agents 模块装配。
	//
	//      agentHub:本地内存实现,支持 500-1000 agent 并发 WS 连接
	//      agentRepo / agentService:DB-backed agent_registry CRUD + rotate + 权限校验
	//      transportAuth:DBAuthenticator —— 从 agent_registry 表验握手 apikey
	agentHub := transportsvc.NewLocalHub(appLogger)
	agentRepoImpl := agentrepo.New(db)
	agentRoleLookup := agentsRoleLookupAdapter{repo: orgRepo}
	hubAdapter := &hubAgentIDAdapter{hub: agentHub}
	agentService := agentsvc.NewAgentService(
		agentsvc.Config{}, agentRepoImpl, &agentRoleLookup, hubAdapter, appLogger,
	)
	transportAuth := agentsvc.NewDBAuthenticator(agentRepoImpl, appLogger)
	transportH := transporthandler.New(agentHub, transportAuth, appLogger)
	agentsH := agenthandler.New(agentService, appLogger)

	// 11b. OAuth AS(PR #5' Stage 1)—— OAuth 2.1 AS + DCR + PAT + 统一 Bearer 中间件
	//
	// 依赖注入:
	//   - agentBootstrapper:OAuth consent / PAT 创建时自动建 user-kind agent。
	//     查 user 的第一个 org → 调 agents.BootstrapUserAgent(kind=user, owner_user_id=<user>)
	//   - userAuthenticator:OAuth consent 页的 password 登录(不走邮箱验证码,简化 OAuth flow)
	//   - sessionStore:OAuth flow 期间的短时登录 session(Redis, TTL=10min)
	//   - dcrRateLimiter:per-IP 限速,包 rdb.SlidingWindowAdd
	oauthRepoImpl := oauthrepo.New(db)
	oauthAgentBootstrapper := oauthAgentBootstrapperImpl{
		orgService:   orgService,
		userRepo:     userRepo,
		agentService: agentService,
		log:          appLogger,
	}
	oauthUserAuthenticator := oauthPasswordAuthenticatorImpl{
		userRepo: userRepo,
		log:      appLogger,
	}
	oauthSessionStore := oauthsvc.NewRedisSessionStore(rdb, 10*time.Minute)
	oauthService := oauthsvc.New(oauthsvc.Config{}, oauthRepoImpl, &oauthAgentBootstrapper, appLogger)
	oauthH := oauthhandler.NewHandler(oauthService, oauthhandler.Config{
		RateLimiter:           oauthhandler.DCRRateLimiterFunc(rdb.SlidingWindowAdd),
		DCRRateLimitWindowSec: cfg.OAuth.DCRRateLimitWindow,
		DCRRateLimitMax:       cfg.OAuth.DCRRateLimitMax,
		Metadata: oauthhandler.MetadataProvider{
			Issuer:         cfg.OAuth.Issuer,
			MCPResourceURL: "", // 默认取 Issuer + /api/v2/mcp;PR #5' 阶段 2 MCP 上线后确认
		},
		SessionStore:         oauthSessionStore,
		UserAuthenticator:    &oauthUserAuthenticator,
		AgentBootstrapper:    &oauthAgentBootstrapper,
		AccessTokenTTL:       cfg.OAuth.AccessTokenTTL,
		RefreshTokenTTL:      cfg.OAuth.RefreshTokenTTL,
		AuthorizationCodeTTL: cfg.OAuth.AuthorizationCodeTTL,
		CookieSecure:         cfg.Server.Mode == "release", // dev=false 允许 http cookie
	}, appLogger)

	// 11c. MCP Server(PR #5' Stage 2)—— 把 channel / task / kb tool 暴露给 Claude
	// Desktop / Cursor / Codex。依赖 OAuth 认证中间件(挂在 MCP 路由组上),tool
	// 内从 request.Context 取 agent principal。
	//
	// KBAdapter 退化成薄壳,delegate 到 channelService.KBQuery(语义检索 / 读文档 /
	// 列文档)和 channelService.KBRef(列挂载关系)。业务规则在 channelsvc.KBQueryService。
	mcpServer := mcp.New(mcp.Config{
		ServerName:    "Synapse",
		ServerVersion: "0.1.0-stage2",
	}, mcp.Deps{
		ChannelSvc: &mcp.ChannelAdapter{
			ChannelSvc: channelService.Channel,
			MessageSvc: channelService.Message,
			MemberSvc:  channelService.Member,
		},
		TaskSvc: &mcp.TaskAdapter{TaskSvc: taskService.Task},
		KBSvc: &mcp.KBAdapter{
			KBQuerySvc: channelService.KBQuery,
		},
		DocumentSvc:   &mcp.DocumentAdapter{DocSvc: channelService.Document},
		AttachmentSvc: &mcp.AttachmentAdapter{AttachmentSvc: channelService.Attachment},
		IdentitySvc:   &mcp.IdentityAdapter{DB: db},
		PMSvc:         pmService,
		Log:           appLogger,
	})

	// 11d. Agentsys(PR #6')—— 顶级系统 agent runtime。
	//
	// 依赖:LLM chat client(Azure)+ eventbus consumer + agents repo(查 top-orch
	// principal_id)+ 审计/使用 repo + scoped deps(channel/task service facade)。
	//
	// 行为:常驻 goroutine 消费 synapse:channel:events,被 @ 顶级 agent 时调 LLM
	// 产出回复或派 task。跨 org 隔离由 scoped.ScopedServices 绑死 (orgID, channelID,
	// actorPID) 在类型层保证 —— 详见 internal/agentsys/scoped/scoped.go 文件头。
	llmClient, err := llm.New(llm.Config{
		Provider: cfg.LLM.Provider,
		Azure: llm.AzureConfig{
			Endpoint:   cfg.LLM.Azure.Endpoint,
			Deployment: cfg.LLM.Azure.Deployment,
			APIKey:     cfg.LLM.Azure.APIKey,
			APIVersion: cfg.LLM.Azure.APIVersion,
		},
		RequestTimeoutSec: cfg.LLM.RequestTimeoutSec,
	})
	if err != nil {
		appLogger.Fatal("failed to init llm client", err, map[string]any{
			"provider":   cfg.LLM.Provider,
			"endpoint":   cfg.LLM.Azure.Endpoint,
			"deployment": cfg.LLM.Azure.Deployment,
		})
	}
	appLogger.Info("llm client ready", map[string]any{"model": llmClient.Model()})

	agentsysAuditRepo := agentsysrepo.NewAuditRepo(db)
	agentsysUsageRepo := agentsysrepo.NewUsageRepo(db)
	orchestratorCtx, orchestratorCancel := context.WithCancel(context.Background())
	defer orchestratorCancel()
	eventBusConsumer := eventbus.NewRedisConsumerGroup(rdb.GetClient(), appLogger)

	// 可选:启动时清光 channel/task stream 上注册的 consumer group(stale consumer + PEL)。
	// 单实例 / dev 部署下保证每次重启 group 从干净状态开始;**生产 / 多实例部署绝对不要开**,
	// 否则启动晚的实例会把先启动实例的 in-flight 事件清掉。
	if cfg.EventBus.ResetGroupsOnStart {
		appLogger.Info("eventbus: resetting consumer groups on start", map[string]any{
			"channel_stream": cfg.EventBus.ChannelStream,
			"task_stream":    cfg.EventBus.TaskStream,
			"groups":         []string{agentsysruntime.ConsumerGroupName, eventcard.ConsumerGroupName},
		})
		for _, stream := range []string{cfg.EventBus.ChannelStream, cfg.EventBus.TaskStream} {
			for _, group := range []string{agentsysruntime.ConsumerGroupName, eventcard.ConsumerGroupName} {
				if err := rdb.GetClient().XGroupDestroy(ctx, stream, group).Err(); err != nil {
					// NOGROUP 错误 = group 本身不存在(首次启动)→ 不算错
					if !strings.Contains(err.Error(), "NOGROUP") {
						appLogger.Warn("eventbus: reset group failed", map[string]any{
							"stream": stream, "group": group, "err": err.Error(),
						})
					}
				}
			}
		}
		// group 重建由后续 orchestrator / eventcard.Writer 启动期 EnsureGroup 完成,这里不主动建。
	}

	scopedDeps := scoped.Deps{
		Messages: channelService.Message,
		Tasks:    taskService.Task,
		KBQuery:  channelService.KBQuery,
		Members:  channelService.Member,
		PM:       pmService,
		DB:       db,
		Logger:   appLogger,
	}

	orchestrator, err := agentsysruntime.NewOrchestrator(
		ctx, // 用启动期 ctx 查 top-orchestrator principal_id 即可
		agentsysruntime.Config{
			ChannelStream:        cfg.EventBus.ChannelStream,
			DailyBudgetPerOrgUSD: cfg.LLM.DailyBudgetPerOrgUSD,
			Concurrency:          cfg.AgentSys.Concurrency,
			AgentID:              agents.TopOrchestratorAgentID,
			ConsumerGroupName:    "top-orchestrator", // 沿用老常量值
			SystemPrompt:         "",                 // 空 fallback 到 prompts.TopOrchestrator
			AgentDisplayName:     "top-orchestrator",
		},
		eventBusConsumer,
		llmClient,
		agentRepoImpl,
		scopedDeps,
		agentsysAuditRepo,
		agentsysUsageRepo,
		nil, // top-orch 不需要 channel kind filter
		appLogger,
	)
	if err != nil {
		appLogger.Fatal("failed to init orchestrator", err, nil)
	}
	appLogger.Info("agentsys orchestrator ready", map[string]any{
		"top_orchestrator_pid": orchestrator.TopOrchestratorPrincipalID(),
	})
	go func() {
		if err := orchestrator.Run(orchestratorCtx); err != nil && err != context.Canceled {
			appLogger.Error("agentsys orchestrator exited", err, nil)
		}
	}()

	// PR-B B2:Project Architect runtime —— 复用同一个 Orchestrator struct,但
	// 用不同 cfg 区分:
	//   - AgentID:synapse-project-architect
	//   - ConsumerGroupName:独立 consumer group 让两个 agent 同时消费 channel events
	//   - SystemPrompt:Architect 专属 prompt(项目编排哲学)
	//   - ChannelKindFilter:只响应 kind='project_console' 的 channel
	architect, err := agentsysruntime.NewOrchestrator(
		ctx,
		agentsysruntime.Config{
			ChannelStream:        cfg.EventBus.ChannelStream,
			DailyBudgetPerOrgUSD: cfg.LLM.DailyBudgetPerOrgUSD,
			Concurrency:          cfg.AgentSys.Concurrency,
			AgentID:              agents.ProjectArchitectAgentID,
			ConsumerGroupName:    "project-architect",
			SystemPrompt:         prompts.ProjectArchitect,
			AgentDisplayName:     "project-architect",
			ChannelKindFilter:    []string{"project_console"},
		},
		eventBusConsumer,
		llmClient,
		agentRepoImpl,
		scopedDeps,
		agentsysAuditRepo,
		agentsysUsageRepo,
		db, // Architect 需要 db 查 channel.kind 过滤
		appLogger,
	)
	if err != nil {
		appLogger.Fatal("failed to init project architect", err, nil)
	}
	appLogger.Info("agentsys project architect ready", map[string]any{
		"architect_pid": architect.AgentPrincipalID(),
	})
	go func() {
		if err := architect.Run(orchestratorCtx); err != nil && err != context.Canceled {
			appLogger.Error("agentsys project architect exited", err, nil)
		}
	}()

	// Channel 协作时间线 consumer(PR #11'):订阅 channel/task 事件流,把业务事件
	// 转成 kind=system_event 消息卡片落在对应 channel。和 orchestrator 共用同一
	// orchestratorCtx(优雅停机顺序对齐)。
	eventCardWriter := eventcard.New(
		eventcard.Config{
			ChannelStream:             cfg.EventBus.ChannelStream,
			TaskStream:                cfg.EventBus.TaskStream,
			FallbackAuthorPrincipalID: orchestrator.TopOrchestratorPrincipalID(),
		},
		eventBusConsumer,
		channelService.Message,
		appLogger,
	)
	appLogger.Info("eventcard writer ready", map[string]any{
		"channel_stream":  cfg.EventBus.ChannelStream,
		"task_stream":     cfg.EventBus.TaskStream,
		"fallback_author": orchestrator.TopOrchestratorPrincipalID(),
	})
	go func() {
		if err := eventCardWriter.Run(orchestratorCtx); err != nil && err != context.Canceled {
			appLogger.Error("eventcard writer exited", err, nil)
		}
	}()

	// pm 事件 consumer:订阅 synapse:pm:events,在 project.created / workstream.created
	// 等事件触发时建 Console / Workstream channel(把 pm 模块和 channel 解耦)。
	pmEventConsumer, err := pmevent.New(
		pmevent.Config{PMStream: cfg.EventBus.PMStream},
		eventBusConsumer,
		db,
		appLogger,
	)
	if err != nil {
		appLogger.Fatal("failed to init pm event consumer", err, nil)
	}
	appLogger.Info("pmevent consumer ready", map[string]any{
		"pm_stream": cfg.EventBus.PMStream,
	})
	go func() {
		if err := pmEventConsumer.Run(orchestratorCtx); err != nil && err != context.Canceled {
			appLogger.Error("pmevent consumer exited", err, nil)
		}
	}()

	asyncJobH := asynchandler.New(asyncJobSvc, appLogger)
	var docH *dochandler.Handler
	if pgDB != nil {
		documentRepo2 := docrepo.New(pgDB)
		uploadSvc := docservice.NewUploadService(pgDB, documentRepo2, appLogger)
		docH = dochandler.New(
			uploadSvc, documentRepo2, asyncJobSvc, sourceSvc, permJudgeSvc,
			ossClient, cfg.OSS.PathPrefix, cfg.OSS.MaxVersionsPerDocument,
			appLogger,
		)
		// source.DeleteSource 前置守卫:统计该 source 下的 doc 数;docRepo 的 CountBySource
		// 直接对得上 DocumentCounter 接口。PG 缺失时不注入,DeleteSource 视作 0 条 doc 放行。
		sourceSvc.SetDocumentCounter(documentRepo2)
	}

	// 12. Setup Gin router
	gin.SetMode(cfg.Server.Mode)
	r := gin.New()
	// M2.4:显式设置可信代理。空 slice = 不信任任何代理,走 socket IP;生产填 LB/ingress IP 段。
	if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		appLogger.Fatal("failed to set trusted proxies", err, map[string]any{"proxies": cfg.Server.TrustedProxies})
	}
	// M2.7:auth middleware 的 401/403 告警走同一条 SLS / lumberjack 管道。
	middleware.SetLogger(appLogger)
	r.Use(gin.Recovery())
	// RequestID 必须在所有业务中间件之前:logger 的 *Ctx 方法依赖它注入 trace_id。
	r.Use(middleware.RequestID())
	// 全局 body 上限 10MB:兼顾上传场景(document upload 路由本身也会做自己的 10MB 校验)。
	// 普通 API 请求远小于此值;不需要对常规路径加更严的单独限制。
	r.Use(middleware.MaxBodySize(10 << 20))

	// 13a. M4 perm 中间件工厂(避免 org/handler 反向 import permhandler 的循环依赖)
	permCtxMW := permhandler.PermContextMiddleware(permJudgeSvc, appLogger)
	requirePerm := func(perm string) gin.HandlerFunc {
		return permhandler.RequirePerm(perm, appLogger)
	}

	// 13. Register routes(基础设施路径,业务路由随 flow 重建陆续恢复)
	userhandler.RegisterRoutes(r, userH, jwtManager, sessionStore, cfg.JWT.AbsoluteSessionTTL)
	orghandler.RegisterRoutes(r, orgH, jwtManager, sessionStore, orgService, permCtxMW, requirePerm, appLogger)
	permhandler.RegisterRoutes(r, permH, auditH, jwtManager, sessionStore, orgService, permCtxMW, requirePerm, appLogger)
	srchandler.RegisterRoutes(r, sourceH, jwtManager, sessionStore, orgService, permCtxMW, requirePerm, appLogger)
	asynchandler.RegisterRoutes(r, asyncJobH, jwtManager, sessionStore)
	if docH != nil {
		dochandler.RegisterRoutes(r, docH, jwtManager, sessionStore, orgService, permJudgeSvc, appLogger)
	}
	transporthandler.RegisterRoutes(r, transportH)
	agenthandler.RegisterRoutes(r, agentsH, jwtManager, sessionStore, orgService, hubAdapter, appLogger)
	pmhandler.RegisterRoutes(r, pmH, jwtManager, sessionStore)
	channelhandler.RegisterRoutes(r, channelH, jwtManager, sessionStore)
	taskhandler.RegisterRoutes(r, taskH, jwtManager, sessionStore)
	oauthhandler.RegisterRoutes(r, oauthH, jwtManager, sessionStore)

	// MCP Server 挂载 /api/v2/mcp/*(Streamable HTTP)。
	// 用 oauthmw.BearerAuth 中间件验 OAuth access_token 或 PAT;通过后把
	// AgentPrincipalID 注入 request.Context,MCP tool handler 从 ctx 读。
	// AsyncRunner 管理 last_used_at 的异步 DB 更新:DB 抖动时 goroutine 不无界堆积,
	// 容量 64 在 ~千 QPS 鉴权下够用(单次 touch < 2s),超了就 reject(touch 是 best-effort)。
	bearerTouchRunner := async.NewAsyncRunner("bearer-auth-touch", 64, appLogger)
	mcpGroup := r.Group("/api/v2/mcp")
	mcpGroup.Use(oauthmw.BearerAuth(oauthRepoImpl, func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}, bearerTouchRunner, appLogger))
	mcpGroup.Any("", gin.WrapH(mcpServer.HTTPHandler()))
	mcpGroup.Any("/*any", gin.WrapH(mcpServer.HTTPHandler()))

	// SSE 事件流:GET /api/v2/users/me/events
	//
	// 两类 caller 共用同一端点,通过 ?filter= 区分:
	//   - filter=mentions(默认):本机 daemon(cmd/agent-bridge),用 PAT 走 BearerAuth
	//   - filter=channel_activity:浏览器 Synapse Web,用 JWT cookie 走 JWTAuthWithSession
	//
	// 鉴权用 BearerOrJWT 复合中间件:有 Authorization Bearer 头走 PAT/OAuth,否则走 JWT。
	sseHandler := channelhandler.NewSSEHandler(
		rdb.GetClient(),
		cfg.EventBus.ChannelStream,
		func(ctx context.Context, userID uint64) (*usersvc.UserProfile, error) {
			return userSvc.GetProfile(ctx, userID)
		},
		// channel ids lookup:列 caller 所属 channel 的 id 集合(channel_activity filter 用)。
		// 一次拉到 1000 条,实际用户场景远少于这个数;超限了 v0 不分页,直接截断。
		func(ctx context.Context, principalID uint64) ([]uint64, error) {
			rows, err := channelService.Channel.ListByPrincipal(ctx, principalID, 1000, 0)
			if err != nil {
				return nil, err
			}
			ids := make([]uint64, len(rows))
			for i := range rows {
				ids[i] = rows[i].ID
			}
			return ids, nil
		},
		appLogger,
	)
	bearerMW := oauthmw.BearerAuth(oauthRepoImpl, func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}, bearerTouchRunner, appLogger)
	jwtMW := middleware.JWTAuthWithSession(jwtManager, sessionStore)
	sseGroup := r.Group("/api/v2/users/me")
	sseGroup.Use(oauthmw.BearerOrJWT(bearerMW, jwtMW))
	sseGroup.GET("/events", sseHandler.HandleEvents)

	r.GET("/health", func(c *gin.Context) {
		response.Success(c, "ok", nil)
	})

	// 15. Start server with graceful shutdown
	srv := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: r,
		// ReadHeaderTimeout 防 slowloris;对 SSE 安全(只限制 header 读取)。
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.Server.ReadTimeout > 0 {
		srv.ReadTimeout = cfg.Server.ReadTimeout
	}
	if cfg.Server.WriteTimeout > 0 {
		srv.WriteTimeout = cfg.Server.WriteTimeout
	}
	if cfg.Server.IdleTimeout > 0 {
		srv.IdleTimeout = cfg.Server.IdleTimeout
	}

	go func() {
		appLogger.Info("server starting", map[string]interface{}{"port": cfg.Server.Port})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLogger.Fatal("server failed", err, nil)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	appLogger.Info("shutting down server...", map[string]interface{}{
		"shutdown_timeout": cfg.Server.ShutdownTimeout.String(),
	})
	// 先停 orchestrator:ctx cancel → Consume 循环里的 XREADGROUP BLOCK 返回 →
	// goroutine 退出。**顺序重要**:如果先关 Redis 再 cancel,orchestrator 可能
	// 会打一堆 connection refused 日志。
	orchestratorCancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("server forced to shutdown (in-flight requests cut)", err, nil)
	}
	// HTTP 停了之后再关异步 touch runner:此时不会再有新的 last_used_at 更新入队,
	// 只需把 in-flight 的 touch 尽量跑完或按 shutdown ctx 超时放弃。
	if err := bearerTouchRunner.Shutdown(shutdownCtx); err != nil {
		appLogger.Warn("bearer auth touch runner shutdown timed out", map[string]any{"err": err.Error()})
	}
	// HTTP server 关闭后再关 agent hub:避免升级中的 ws 升级半途 abort。
	if err := agentHub.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("agent hub shutdown failed", err, nil)
	}
	appLogger.Info("server stopped", nil)
}

// hubAgentIDAdapter 把 transport.AgentID 弱类型的 agents 模块接口对齐到 *LocalHub 的强类型方法。
// 同时实现 agents/service.AgentDisconnector 和 agents/handler.OnlineChecker。
// 这里的转换零成本 —— AgentID 本就是 string 的类型别名。
type hubAgentIDAdapter struct {
	hub *transportsvc.LocalHub
}

func (a *hubAgentIDAdapter) Disconnect(agentID string, reason string) bool {
	return a.hub.Disconnect(transport.AgentID(agentID), reason)
}

func (a *hubAgentIDAdapter) IsOnline(agentID string) bool {
	return a.hub.IsOnline(transport.AgentID(agentID))
}

// agentsRoleLookupAdapter 实现 agents/service.OrgRoleLookup,从 org repository 读 role slug。
// 与 orgRoleLookupAdapter(permission 模块用)并列,因为各自的返回结构体不同,
// 不能共用一个适配器。逻辑上查的是同一条路径(member → role),未来可统一。
type agentsRoleLookupAdapter struct {
	repo orgrepo.Repository
}

// GetMemberRoleSlug 返回 user 在 org 的系统角色 slug。不是成员 → "",nil(按 forbidden 处理)。
// DB 错误按内部错向上抛;记录破损(有 member 无对应 role)视作 ""(极端边缘情况)。
func (a *agentsRoleLookupAdapter) GetMemberRoleSlug(ctx context.Context, orgID, userID uint64) (string, error) {
	mem, err := a.repo.FindMember(ctx, orgID, userID)
	if err != nil {
		// gorm.ErrRecordNotFound → 非成员,返 ""
		return "", nil //nolint:nilerr // 不存在不是错误
	}
	role, err := a.repo.FindRoleByID(ctx, mem.RoleID)
	if err != nil {
		return "", nil //nolint:nilerr // role 破损按"无角色"处理
	}
	return role.Slug, nil
}

// ─── 跨模块 adapter ──────────────────────────────────────────────────────────

// sourceSubjectValidator 满足 source/service.SubjectValidator 接口:
//   - GroupExistsInOrg:走 permission/repository.FindGroupByID + 校验 OrgID 匹配
//   - UserIsOrgMember:走 orgService.IsMember
type sourceSubjectValidator struct {
	permRepo permrepo.Repository
	isMember func(ctx context.Context, orgID, userID uint64) (bool, error)
}

func (v *sourceSubjectValidator) GroupExistsInOrg(ctx context.Context, orgID, groupID uint64) (bool, error) {
	g, err := v.permRepo.FindGroupByID(ctx, groupID)
	if err != nil {
		// gorm.ErrRecordNotFound → 视为不存在
		return false, nil //nolint:nilerr // 不存在不是错误
	}
	return g.OrgID == orgID, nil
}

func (v *sourceSubjectValidator) UserIsOrgMember(ctx context.Context, orgID, userID uint64) (bool, error) {
	return v.isMember(ctx, orgID, userID)
}

// orgRoleLookupAdapter 满足 permission/service.OrgRoleLookup 接口,
// 给 PermissionService 查 (user, org) → role + permissions 用。
//
// 实现:两次查询(member + role),结果浅拷贝成 MemberRoleInfo。
// 不是成员时返 (nil, nil)(error 也为 nil),让 service 视作"无权限"处理。
type orgRoleLookupAdapter struct {
	repo orgrepo.Repository
}

func (a *orgRoleLookupAdapter) GetMemberRole(ctx context.Context, orgID, userID uint64) (*permsvc.MemberRoleInfo, error) {
	mem, err := a.repo.FindMember(ctx, orgID, userID)
	if err != nil {
		// gorm.ErrRecordNotFound → 不是成员
		return nil, nil //nolint:nilerr // 不存在不是错误
	}
	role, err := a.repo.FindRoleByID(ctx, mem.RoleID)
	if err != nil {
		// role 不存在(数据破损)→ 当成无权限
		return nil, nil //nolint:nilerr // 数据破损不上抛
	}
	return &permsvc.MemberRoleInfo{
		RoleID:      role.ID,
		Slug:        role.Slug,
		IsSystem:    role.IsSystem,
		Permissions: []string(role.Permissions),
	}, nil
}

// sourceLookupAdapter 满足 permission/service.SourceLookup 接口,
// 给 PermissionService 提供 source 元信息(只读)。
type sourceLookupAdapter struct {
	repo srcrepo.Repository
}

func (a *sourceLookupAdapter) GetSource(ctx context.Context, sourceID uint64) (*permsvc.SourceInfo, error) {
	src, err := a.repo.FindSourceByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return &permsvc.SourceInfo{
		ID:          src.ID,
		OrgID:       src.OrgID,
		OwnerUserID: src.OwnerUserID,
		Visibility:  src.Visibility,
	}, nil
}

func (a *sourceLookupAdapter) ListSourceIDsByOwner(ctx context.Context, orgID, ownerUserID uint64) ([]uint64, error) {
	return a.repo.ListSourceIDsByOwner(ctx, orgID, ownerUserID)
}

func (a *sourceLookupAdapter) ListSourceIDsByVisibility(ctx context.Context, orgID uint64, visibility string) ([]uint64, error) {
	return a.repo.ListSourceIDsByVisibility(ctx, orgID, visibility)
}

// asyncJobLookupAdapter 把 asyncjob/repository.Repository 适配成 source.service.AsyncJobLookup。
// model.Job 的字段 → service 关心的精简结构(防 model 包穿透到 source service 接口里)。
type asyncJobLookupAdapter struct {
	repo asyncrepo.Repository
}

func (a asyncJobLookupAdapter) FindLatestByKeyPrefix(ctx context.Context, orgID uint64, kind, keyPrefix string) (*srcsvc.AsyncJobInfo, error) {
	job, err := a.repo.FindLatestByKeyPrefix(ctx, orgID, kind, keyPrefix)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}
	out := &srcsvc.AsyncJobInfo{
		ID:             job.ID,
		Kind:           job.Kind,
		Status:         string(job.Status),
		IdempotencyKey: job.IdempotencyKey,
		Payload:        []byte(job.Payload),
		ProgressDone:   job.ProgressDone,
		ProgressTotal:  job.ProgressTotal,
		ProgressFailed: job.ProgressFailed,
		Error:          job.Error,
	}
	if job.StartedAt != nil {
		out.StartedAt = job.StartedAt.Unix()
	}
	if job.FinishedAt != nil {
		out.FinishedAt = job.FinishedAt.Unix()
	}
	if job.HeartbeatAt != nil {
		out.HeartbeatAt = job.HeartbeatAt.Unix()
	}
	return out, nil
}

// gitlabSyncEnqueuer 把 asyncJobSvc.Schedule 适配成 source.service.GitLabSyncEnqueuer +
// service.GitLabIncrementalEnqueuer 两个接口。
//
// 幂等键策略:
//   - 全量:"gitlab:<source_id>:full"   — 同 source 并发全量复用
//   - 增量:"gitlab:<source_id>:incr:<after_sha>" — 同 push 重发不重跑
//
// ErrDuplicateJob:已有 active 任务 → 视为成功,复用现有 jobID。
type gitlabSyncEnqueuer struct {
	svc *asyncsvc.Service
	log logger.LoggerInterface
}

func (e gitlabSyncEnqueuer) EnqueueFullSync(ctx context.Context, orgID, userID, sourceID uint64) (uint64, error) {
	return e.enqueue(ctx, asyncsvc.ScheduleInput{
		OrgID:          orgID,
		UserID:         userID,
		Kind:           gitlabsync.Kind,
		Payload:        gitlabsync.Input{SourceID: sourceID, Mode: gitlabsync.ModeFull},
		IdempotencyKey: fmt.Sprintf("gitlab:%d:full", sourceID),
	}, sourceID)
}

func (e gitlabSyncEnqueuer) EnqueueIncrementalSync(ctx context.Context, orgID, userID, sourceID uint64, beforeSHA, afterSHA string) (uint64, error) {
	return e.enqueue(ctx, asyncsvc.ScheduleInput{
		OrgID:  orgID,
		UserID: userID,
		Kind:   gitlabsync.Kind,
		Payload: gitlabsync.Input{
			SourceID:  sourceID,
			Mode:      gitlabsync.ModeIncremental,
			BeforeSHA: beforeSHA,
			AfterSHA:  afterSHA,
		},
		IdempotencyKey: fmt.Sprintf("gitlab:%d:incr:%s", sourceID, afterSHA),
	}, sourceID)
}

// enqueue 共用的"调 Schedule + ErrDuplicateJob 翻译"。
func (e gitlabSyncEnqueuer) enqueue(ctx context.Context, in asyncsvc.ScheduleInput, sourceID uint64) (uint64, error) {
	job, err := e.svc.Schedule(ctx, in)
	if err != nil {
		if errors.Is(err, asyncjob.ErrDuplicateJob) && job != nil {
			e.log.InfoCtx(ctx, "gitlabsync: reusing existing active job", map[string]any{
				"source_id": sourceID, "job_id": job.ID, "kind": in.Kind,
				"idempotency_key": in.IdempotencyKey,
			})
			return job.ID, nil
		}
		return 0, err
	}
	return job.ID, nil
}

// ─── OAuth adapters ──────────────────────────────────────────────────────────

// oauthAgentBootstrapperImpl 实现 oauth.service.AgentBootstrapper。
//
// 流程:查 ownerUserID 的第一个 org(通过 orgService.ListUserOrgs 或类似) →
// 调 agents.BootstrapUserAgent(orgID, ownerUserID, displayName) → 返 (agentID, principalID)。
//
// MVP 简化:"第一个 org" = 任选一个 user 属于的 org。多 org 场景下未来可以让 consent
// 页带 org 选项。现在 user 基本只有一个 org,直接取第一个。
type oauthAgentBootstrapperImpl struct {
	orgService   orgsvc.OrgService
	userRepo     userrepo.Repository
	agentService *agentsvc.AgentService
	log          logger.LoggerInterface
}

func (b *oauthAgentBootstrapperImpl) CreateUserAgent(ctx context.Context, ownerUserID uint64, displayName string) (uint64, uint64, error) {
	orgs, err := b.orgService.ListOrgsByUser(ctx, ownerUserID)
	if err != nil {
		return 0, 0, fmt.Errorf("list user orgs: %w", err)
	}
	if len(orgs) == 0 {
		return 0, 0, fmt.Errorf("user has no org membership")
	}
	// 取第一个 org —— 多 org 时未来 consent 页加选择器
	orgID := orgs[0].Org.ID

	// 拼一个可读的 display_name:"{user 人名} 的 {client_name}"。
	// 比单用 client_name("Claude")更能在多用户 + 多 client 的组织里唯一识别。
	// fallback:查不到 user 或 user 没 display_name 时退回原 displayName(clientName)。
	fullName := displayName
	if b.userRepo != nil {
		if u, err := b.userRepo.FindActiveByID(ctx, ownerUserID); err == nil && u != nil {
			userName := u.DisplayName
			if userName == "" {
				userName = u.Email
			}
			if userName != "" && displayName != "" {
				fullName = userName + " 的 " + displayName
			} else if userName != "" {
				fullName = userName + " 的 agent"
			}
		}
	}

	// 查重:同 (org, owner, display_name) 已有 agent → 直接复用,不重复创建。
	// 防止 Claude Desktop 每次重连 DCR(生成新 client_id)都在 agents 表里长新行。
	if existing, err := b.agentService.FindUserAgentByDisplayName(ctx, orgID, ownerUserID, fullName); err == nil && existing != nil {
		b.log.InfoCtx(ctx, "oauth: reusing existing user agent", map[string]any{
			"agent_id": existing.AgentID, "owner_user_id": ownerUserID, "display_name": fullName,
		})
		return existing.ID, existing.PrincipalID, nil
	}

	return b.agentService.BootstrapUserAgent(ctx, orgID, ownerUserID, fullName)
}

// oauthPasswordAuthenticatorImpl 实现 oauth.service.UserAuthenticator。
//
// 路径:查 users 表 FindActiveByEmail + bcrypt.CompareHashAndPassword。
// 不走 user.Login(它强制消费邮箱验证码,对 OAuth consent 不适用)。
// 也不走邮箱验证码 / login_guard 之类 —— 简化 OAuth flow;后续加 CSRF / 2FA 时再补。
type oauthPasswordAuthenticatorImpl struct {
	userRepo userrepo.Repository
	log      logger.LoggerInterface
}

func (a *oauthPasswordAuthenticatorImpl) AuthenticateByPassword(ctx context.Context, email, password, _, _ string) (uint64, error) {
	u, err := a.userRepo.FindActiveByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil || u == nil {
		return 0, fmt.Errorf("user not found")
	}
	if bcryptErr := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); bcryptErr != nil {
		return 0, fmt.Errorf("password mismatch")
	}
	return u.ID, nil
}

// isMarkdownDoc ingestion ChunkerSelector 用:MIMEType + 文件名扩展名兜底判 markdown。
// MIMEType 用 HasPrefix 兼容 "text/markdown; charset=utf-8" 形式。
func isMarkdownDoc(mime, name string) bool {
	if strings.HasPrefix(mime, "text/markdown") {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") ||
		strings.HasSuffix(lower, ".markdown") ||
		strings.HasSuffix(lower, ".mdx")
}
