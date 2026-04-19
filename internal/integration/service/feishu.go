// Package service integration 模块的 OAuth 编排层,按 provider 分文件。
//
// feishu.go: 飞书 OAuth 代码交换 + refresh_token 持久化编排。HTTP handler 不在本包,
// 由 web 层(internal/.../handler)调这里的 FeishuService 完成业务逻辑。
//
// 多租户:应用凭证(app_id / app_secret)**per org** 存在 org_feishu_configs 表,
// FeishuService 本身不再持有凭证 —— 每次调用按 orgID 查出当前 org 的凭证用。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"gorm.io/datatypes"

	"github.com/eyrihe999-stack/Synapse/internal/integration"
	intgmodel "github.com/eyrihe999-stack/Synapse/internal/integration/model"
	intgrepo "github.com/eyrihe999-stack/Synapse/internal/integration/repository"
	"github.com/eyrihe999-stack/Synapse/pkg/logger"
)

// ErrAppNotConfigured org 没有配置飞书应用(org_feishu_configs 里没有行)。
// 上层 handler 应返 412 Precondition Required + 引导 admin 去填配置。
var ErrAppNotConfigured = errors.New("feishu: org app not configured")

// FeishuService 飞书 OAuth 编排。
//
// 职责:
//   - 构造 OAuth 授权 URL(给前端用)
//   - 接收回调 code → 调飞书 access_token API → 拿 refresh_token → 存库(Upsert)
//   - 给 sync worker 提供"取当前有效 user_access_token + 自动刷新 + 回写"的接口
//
// 不负责:
//   - HTTP endpoint(由 handler 层调这里)
//   - Webhook 接收(未来独立 module)
//   - 真正的内容拉取(走 pkg/sourceadapter/feishu.Adapter)
//
// 关键差异(相对早期单租户版本):构造不再注入 AppID/AppSecret;
// 每个操作按 orgID 查 configRepo 拿凭证,未配置 → ErrAppNotConfigured。
type FeishuService struct {
	baseURL string
	// OAuth redirect_uri(前端拿用户同意后飞书 302 回这个 URL)。
	// 部署级配置,所有 org 共享同一个回调端点(user_id / org_id 通过 state 自包含还原);
	// org admin 在飞书开放平台配置自家 App 时需要把这个 URL 加到白名单。
	redirectURI string

	repo       intgrepo.Repository
	configRepo intgrepo.FeishuConfigRepository
	http       *http.Client
	log        logger.LoggerInterface
}

// FeishuConfig 构造参数。
type FeishuConfig struct {
	BaseURL     string // 空 = 飞书中国区域名
	RedirectURI string // 用户授权后回跳地址(部署级,所有 org 共享)
	HTTPTimeout time.Duration
}

// NewFeishuService 构造。失败返 err(只做参数校验)。
func NewFeishuService(
	cfg FeishuConfig,
	repo intgrepo.Repository,
	configRepo intgrepo.FeishuConfigRepository,
	log logger.LoggerInterface,
) (*FeishuService, error) {
	if cfg.RedirectURI == "" {
		return nil, fmt.Errorf("feishu service: RedirectURI required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	return &FeishuService{
		baseURL:     cfg.BaseURL,
		redirectURI: cfg.RedirectURI,
		repo:        repo,
		configRepo:  configRepo,
		http:        &http.Client{Timeout: cfg.HTTPTimeout},
		log:         log,
	}, nil
}

// RedirectURI 暴露给 handler 用于向 admin 展示"请把这个 URL 加到飞书应用白名单"。
func (s *FeishuService) RedirectURI() string { return s.redirectURI }

// BuildAuthURL 构造前端引导用户点击的授权链接。state 用于 CSRF 防护(回调时比对);
// 生成策略由上层(handler)决定 —— 通常是随机 token 存 session。
//
// orgID 用于查 app_id。未配置 → ErrAppNotConfigured。
//
// scope 传入的 scopes 会拼到 URL,飞书要求和开发者后台开的 scope 是子集关系。空 = 走默认(app 后台配置)。
func (s *FeishuService) BuildAuthURL(ctx context.Context, orgID uint64, state string, scopes []string) (string, error) {
	cfg, err := s.configRepo.GetByOrg(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("feishu auth url: load org config: %w", err)
	}
	if cfg == nil {
		return "", ErrAppNotConfigured
	}
	q := url.Values{}
	q.Set("app_id", cfg.AppID)
	q.Set("redirect_uri", s.redirectURI)
	q.Set("state", state)
	if len(scopes) > 0 {
		q.Set("scope", joinScopes(scopes))
	}
	return s.baseURL + "/open-apis/authen/v1/authorize?" + q.Encode(), nil
}

// ExchangeCode 接收 OAuth 回调带的 code,换 access_token + refresh_token,存库。
// 典型调用点:OAuth callback 的 handler。
//
// userID / orgID 来自 state(HMAC 校验过);凭证按 orgID 查 org_feishu_configs。
// 返回 Integration 记录(带 ID)方便 handler 响应体用。
func (s *FeishuService) ExchangeCode(ctx context.Context, userID, orgID uint64, code string) (*intgmodel.UserIntegration, error) {
	if userID == 0 || orgID == 0 {
		return nil, fmt.Errorf("feishu exchange: userID + orgID required")
	}
	if code == "" {
		return nil, fmt.Errorf("feishu exchange: code required")
	}

	cfg, err := s.configRepo.GetByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("feishu exchange: load org config: %w", err)
	}
	if cfg == nil {
		return nil, ErrAppNotConfigured
	}

	// Step 1: 用 app 凭证换 app_access_token(飞书要求:exchange code 必须带 app_access_token,不是直接用 secret)。
	appToken, err := s.fetchAppAccessToken(ctx, cfg.AppID, cfg.AppSecret)
	if err != nil {
		return nil, fmt.Errorf("feishu exchange: %w", err)
	}

	// Step 2: 用 code + app_access_token 换 user_access_token。
	tokens, userInfo, err := s.exchangeUserToken(ctx, appToken, code)
	if err != nil {
		return nil, fmt.Errorf("feishu exchange: %w", err)
	}

	// Step 3: 序列化 metadata(open_id / name / email),upsert 到 user_integrations。
	metaBytes, _ := json.Marshal(userInfo)
	refreshExp := time.Now().Add(time.Duration(tokens.RefreshExpiresIn) * time.Second)
	accessExp := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	intg := &intgmodel.UserIntegration{
		UserID:                userID,
		OrgID:                 orgID,
		Provider:              integration.ProviderFeishu,
		RefreshToken:          tokens.RefreshToken,
		RefreshTokenExpiresAt: &refreshExp,
		AccessToken:           tokens.AccessToken,
		AccessTokenExpiresAt:  &accessExp,
		Metadata:              datatypes.JSON(metaBytes),
	}
	saved, err := s.repo.Upsert(ctx, intg)
	if err != nil {
		return nil, err
	}
	s.log.InfoCtx(ctx, "feishu oauth: user authorized", map[string]any{
		"user_id": userID, "org_id": orgID, "feishu_open_id": userInfo.OpenID,
	})
	return saved, nil
}

// RefreshViaIntegration worker 直接传入完整 Integration 行,刷新 access_token 并回写库。
//
// 凭证按 intg.OrgID 查 org_feishu_configs。如果 org 把配置删了,刷新也会失败(ErrAppNotConfigured)。
//
// 逻辑:
//  1. Integration.AccessToken 还没到期 → 直接返,不刷新
//  2. 到期了 → 用 Integration.RefreshToken 刷新 → 存新 access_token + 可能轮换的 refresh_token
//  3. 刷新失败(refresh_token 也失效了)→ 返 error,worker 应跳过这条 Integration 继续其他用户
func (s *FeishuService) RefreshViaIntegration(ctx context.Context, intg *intgmodel.UserIntegration) (string, error) {
	// 留 60s 余量:快到期就主动刷,避免长 Sync 跑到一半 token 失效。
	// ExpiresAt nil(飞书不该出现,留着防御其他 provider 永不过期的 access_token)视为"不过期",直接用缓存。
	if intg.AccessToken != "" && (intg.AccessTokenExpiresAt == nil || time.Now().Add(60*time.Second).Before(*intg.AccessTokenExpiresAt)) {
		return intg.AccessToken, nil
	}
	if intg.RefreshToken == "" {
		return "", fmt.Errorf("feishu refresh: no refresh_token (user revoked?)")
	}
	cfg, err := s.configRepo.GetByOrg(ctx, intg.OrgID)
	if err != nil {
		return "", fmt.Errorf("feishu refresh: load org config: %w", err)
	}
	if cfg == nil {
		return "", ErrAppNotConfigured
	}
	tokens, err := s.refreshUserToken(ctx, cfg.AppID, cfg.AppSecret, intg.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("feishu refresh: %w", err)
	}
	// 回写。refresh_token 可能轮换,repo.UpdateTokens 里空串则不覆盖。
	accessExp := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	var refreshExp *time.Time
	if tokens.RefreshExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokens.RefreshExpiresIn) * time.Second)
		refreshExp = &t
	}
	if err := s.repo.UpdateTokens(ctx, intg.ID, tokens.AccessToken, &accessExp, tokens.RefreshToken, refreshExp); err != nil {
		return "", fmt.Errorf("feishu refresh: persist: %w", err)
	}
	// 本地 struct 也同步,让调用方复用不掉队。
	intg.AccessToken = tokens.AccessToken
	intg.AccessTokenExpiresAt = &accessExp
	if tokens.RefreshToken != "" {
		intg.RefreshToken = tokens.RefreshToken
		if refreshExp != nil {
			intg.RefreshTokenExpiresAt = refreshExp
		}
	}
	return tokens.AccessToken, nil
}

// GetOrgAppCreds 给 adapter 层用 —— 按 orgID 拿 app_id / app_secret 构造 Adapter。
// 未配置 → ErrAppNotConfigured。
func (s *FeishuService) GetOrgAppCreds(ctx context.Context, orgID uint64) (appID, appSecret string, err error) {
	cfg, err := s.configRepo.GetByOrg(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("get org app creds: %w", err)
	}
	if cfg == nil {
		return "", "", ErrAppNotConfigured
	}
	return cfg.AppID, cfg.AppSecret, nil
}

// Revoke 撤销授权。前端用户点"断开飞书"时调;或 refresh_token 失效时自动调。
// 硬删;不保留"曾经授权过" 的历史(审计需求未来再说)。
func (s *FeishuService) Revoke(ctx context.Context, userID uint64) error {
	return s.repo.Delete(ctx, userID, integration.ProviderFeishu)
}

// ─── 飞书 HTTP 调用实现 ────────────────────────────────────────────────────────

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
}

type userInfoResponse struct {
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// fetchAppAccessToken 拿 app_access_token(7200s 有效)。此 token 只用作换 user_token 的跳板,不落库。
func (s *FeishuService) fetchAppAccessToken(ctx context.Context, appID, appSecret string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/auth/v3/app_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	var env struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if env.Code != 0 {
		return "", fmt.Errorf("feishu app_access_token: code=%d msg=%s", env.Code, env.Msg)
	}
	return env.AppAccessToken, nil
}

// exchangeUserToken 用 OAuth code 换 user_access_token + refresh_token + 用户基本信息。
func (s *FeishuService) exchangeUserToken(ctx context.Context, appToken, code string) (*tokenResponse, *userInfoResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/authen/v1/access_token", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+appToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			tokenResponse
			userInfoResponse
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("decode: %w (body=%s)", err, truncate(raw, 200))
	}
	if env.Code != 0 {
		return nil, nil, fmt.Errorf("feishu access_token: code=%d msg=%s", env.Code, env.Msg)
	}
	return &env.Data.tokenResponse, &env.Data.userInfoResponse, nil
}

// refreshUserToken 用 refresh_token 刷新 user_access_token。
func (s *FeishuService) refreshUserToken(ctx context.Context, appID, appSecret, refreshToken string) (*tokenResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"app_id":        appID,
		"app_secret":    appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/open-apis/authen/v1/refresh_access_token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Code int           `json:"code"`
		Msg  string        `json:"msg"`
		Data tokenResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode: %w (body=%s)", err, truncate(raw, 200))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("feishu refresh: code=%d msg=%s", env.Code, env.Msg)
	}
	return &env.Data, nil
}

func joinScopes(scopes []string) string {
	// 飞书 scope 用空格分隔(OAuth2 标准)。url.Values.Set 会自动 URL-encode 空格。
	out := ""
	for i, s := range scopes {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
