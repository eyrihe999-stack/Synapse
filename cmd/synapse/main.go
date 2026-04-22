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
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob"
	asynchandler "github.com/eyrihe999-stack/Synapse/internal/asyncjob/handler"
	asyncrepo "github.com/eyrihe999-stack/Synapse/internal/asyncjob/repository"
	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/runners/docupload"
	asyncsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"
	"github.com/eyrihe999-stack/Synapse/internal/document"
	dochandler "github.com/eyrihe999-stack/Synapse/internal/document/handler"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	docservice "github.com/eyrihe999-stack/Synapse/internal/document/service"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	mdchunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/markdown"
	plainchunker "github.com/eyrihe999-stack/Synapse/internal/ingestion/chunker/plaintext"
	docpersister "github.com/eyrihe999-stack/Synapse/internal/ingestion/persister/document"
	"github.com/eyrihe999-stack/Synapse/internal/common/middleware"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/organization"
	orghandler "github.com/eyrihe999-stack/Synapse/internal/organization/handler"
	orgrepo "github.com/eyrihe999-stack/Synapse/internal/organization/repository"
	orgsvc "github.com/eyrihe999-stack/Synapse/internal/organization/service"
	"github.com/eyrihe999-stack/Synapse/internal/permission"
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
	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/email"
	"github.com/eyrihe999-stack/Synapse/internal/common/embedding"
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
	ctx := context.Background()
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
	var pipeline *ingestion.Pipeline
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
		embedder, err := embedding.New(embedCfg)
		if err != nil {
			appLogger.Fatal("failed to build embedder", err, nil)
		}
		mdCk := mdchunker.New(0)
		plainCk := plainchunker.New(0)
		selector := func(d *ingestion.NormalizedDoc) ingestion.Chunker {
			if d.SourceType != ingestion.SourceTypeDocument {
				return nil
			}
			if isMarkdownDoc(d.MIMEType, d.FileName) {
				return mdCk
			}
			return plainCk
		}
		documentRepo := docrepo.New(pgDB)
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
	var runners []asyncsvc.Runner
	if pipeline != nil {
		uploadRunner, err := docupload.New(pipeline, ossClient, appLogger)
		if err != nil {
			appLogger.Fatal("docupload runner init failed", err, nil)
		}
		runners = append(runners, uploadRunner)
	}
	asyncJobSvc := asyncsvc.NewService(asyncsvc.Config{}, asyncJobRepo, runners, appLogger)
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

	// asyncjob + document handlers(Layer 3)。document 需要 PG;缺失时路由不挂,HTTP 触不到。
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
	srchandler.RegisterRoutes(r, sourceH, jwtManager, sessionStore, orgService, appLogger)
	asynchandler.RegisterRoutes(r, asyncJobH, jwtManager, sessionStore)
	if docH != nil {
		dochandler.RegisterRoutes(r, docH, jwtManager, sessionStore, orgService, permJudgeSvc, appLogger)
	}

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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("server forced to shutdown (in-flight requests cut)", err, nil)
	}
	appLogger.Info("server stopped", nil)
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
