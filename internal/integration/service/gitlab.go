// gitlab.go GitLab PAT 集成的 service 层。
//
// 和 feishu.go 的差异:
//   - PAT 模式没有 OAuth 跳转、没有 state 签名、没有 refresh_token 轮转
//   - App 凭证不存在(PAT 自身就是长期凭证);取而代之的是实例级配置(base_url)
//   - 只暴露两个动作:Connect(存 token + 验证) / Revoke(删)
//
// 实例配置 per org 存在 org_gitlab_configs 表(由 admin 在前端填),和 OrgFeishuConfig 对称。
// Connect 时先按 orgID 查配置构造客户端,再调 /user 验证 PAT。
//
// 错误分层:
//   - 未配置实例 → ErrGitLabNotConfigured,handler 转 412 引导 admin
//   - PAT 401   → ErrInvalidGitLabToken,handler 转 400 引导用户重贴
//   - 其他网络错误 → fmt.Errorf 包装,handler 转 502
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/integration"
	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/gitlab"
)

// ErrInvalidGitLabToken PAT 被 GitLab 拒绝(401)。由 handler 转 400 + 引导用户重贴 token。
var ErrInvalidGitLabToken = errors.New("gitlab: invalid or revoked token")

// ErrGitLabNotConfigured org 尚未在前端设置 GitLab 实例地址。handler 转 412 引导 admin 去配置页。
var ErrGitLabNotConfigured = errors.New("gitlab: instance not configured for this org")

// GitLabClientFactory 根据 gitlab.Config 构造客户端。抽出来便于测试时替换。
// 生产用 gitlab.NewClient。
type GitLabClientFactory func(cfg gitlab.Config) (gitlab.ClientAPI, error)

// GitLabService GitLab PAT 模式的集成编排。
//
// 不持有单例 client —— 每次调用按 orgID 动态构建(BaseURL 是 per-org 的)。
// 短生命周期 client 成本可接受:只有 Connect 会走到 GitLab HTTP,每次请求一次。
// 后续 Sync runner 走 adapter 层会自己持有 client,不复用这里。
type GitLabService struct {
	configRepo    intgrepo.GitLabConfigRepository
	clientFactory GitLabClientFactory
	repo          intgrepo.Repository
	log           logger.LoggerInterface
}

// NewGitLabService 构造。clientFactory 传 nil 时默认用 gitlab.NewClient。
func NewGitLabService(
	configRepo intgrepo.GitLabConfigRepository,
	clientFactory GitLabClientFactory,
	repo intgrepo.Repository,
	log logger.LoggerInterface,
) *GitLabService {
	if clientFactory == nil {
		clientFactory = gitlab.NewClient
	}
	return &GitLabService{
		configRepo:    configRepo,
		clientFactory: clientFactory,
		repo:          repo,
		log:           log,
	}
}

// BuildClientForOrg 暴露给其他模块(如 asyncjob runner)按 orgID 动态构造 GitLab 客户端。
// 只是 buildClient 的公开包装 —— 逻辑完全一致,单独起名让外部调用者不直接访问私有方法。
func (s *GitLabService) BuildClientForOrg(ctx context.Context, orgID uint64) (gitlab.ClientAPI, error) {
	return s.buildClient(ctx, orgID)
}

// buildClient 按 orgID 查 OrgGitLabConfig 构造一个一次性客户端。未配置返 ErrGitLabNotConfigured。
func (s *GitLabService) buildClient(ctx context.Context, orgID uint64) (gitlab.ClientAPI, error) {
	cfg, err := s.configRepo.GetByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("gitlab: load org config: %w", err)
	}
	if cfg == nil {
		return nil, ErrGitLabNotConfigured
	}
	client, err := s.clientFactory(gitlab.Config{
		BaseURL:            cfg.BaseURL,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: build client: %w", err)
	}
	return client, nil
}

// GitLabConnectResult Connect 成功后返给 handler 用于构造响应 + 日志。
type GitLabConnectResult struct {
	Integration *intgmodel.UserIntegration
	User        *gitlab.CurrentUser
}

// Connect 验证 PAT + 存库。流程:
//  1. 调 GitLab /user 用此 PAT 登录,401 → ErrInvalidGitLabToken
//  2. 把 token 存 UserIntegration.AccessToken(PAT 无过期,复用此字段最省事;RefreshToken 留空)
//  3. 用户信息(username/email/avatar_url/web_url)序列化进 Metadata,供前端展示
//
// 同一用户再次调用 Connect(换 token / 重新贴)会走 Upsert 覆盖,不新建行。
func (s *GitLabService) Connect(ctx context.Context, userID, orgID uint64, token string) (*GitLabConnectResult, error) {
	if userID == 0 || orgID == 0 {
		return nil, fmt.Errorf("gitlab connect: userID + orgID required")
	}
	if token == "" {
		return nil, ErrInvalidGitLabToken
	}

	client, err := s.buildClient(ctx, orgID)
	if err != nil {
		return nil, err
	}
	user, err := client.GetCurrentUser(ctx, token)
	if err != nil {
		if errors.Is(err, gitlab.ErrInvalidToken) {
			return nil, ErrInvalidGitLabToken
		}
		return nil, fmt.Errorf("gitlab connect: validate token: %w", err)
	}

	metaBytes, _ := json.Marshal(gitlabMetadata{
		UserID:    user.ID,
		Username:  user.Username,
		Name:      user.Name,
		Email:     user.Email,
		AvatarURL: user.AvatarURL,
		WebURL:    user.WebURL,
	})

	intg := &intgmodel.UserIntegration{
		UserID:      userID,
		OrgID:       orgID,
		Provider:    integration.ProviderGitLab,
		AccessToken: token,
		// RefreshToken 保持空(PAT 无 refresh 概念;repo.ListByProvider 的"refresh_token != ''"过滤
		// 对 GitLab 不适用,runner 后续要按 AccessToken 过滤,Slice 3 再处理)。
		Metadata: datatypes.JSON(metaBytes),
	}
	saved, err := s.repo.Upsert(ctx, intg)
	if err != nil {
		return nil, fmt.Errorf("gitlab connect: persist: %w", err)
	}

	s.log.InfoCtx(ctx, "gitlab connect: user authorized", map[string]any{
		"user_id":          userID,
		"org_id":           orgID,
		"gitlab_user_id":   user.ID,
		"gitlab_username":  user.Username,
	})
	return &GitLabConnectResult{Integration: saved, User: user}, nil
}

// Revoke 撤销。硬删(和 feishu 一致)。幂等 —— 没有记录也返 nil。
func (s *GitLabService) Revoke(ctx context.Context, userID uint64) error {
	return s.repo.Delete(ctx, userID, integration.ProviderGitLab)
}

// gitlabMetadata UserIntegration.Metadata jsonb 的解析 shape。Connect 时写入,handler 里 Status 端点读出。
type gitlabMetadata struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	WebURL    string `json:"web_url"`
}
