// source_service_gitlab.go SourceService 的 GitLab 集成方法实现。
//
// 端点鉴权(integration.gitlab.manage perm)在 router 层 RequirePerm 拦,本文件不重复;
// 业务校验(visibility / project_id 取值 / GitLab 凭据有效性)集中在这里。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/source"
	"github.com/eyrihe999-stack/Synapse/internal/source/dto"
	"github.com/eyrihe999-stack/Synapse/internal/source/model"
	"github.com/eyrihe999-stack/Synapse/internal/user_integration"
	uimodel "github.com/eyrihe999-stack/Synapse/internal/user_integration/model"
	"gorm.io/gorm"
)

// CreateGitLabSource 见接口注释。
//
//nolint:funlen // 全流程 6 步串行,拆出去会让逻辑顺序更难看
func (s *sourceService) CreateGitLabSource(ctx context.Context, orgID, callerUserID uint64, req dto.CreateGitLabSourceRequest) (*dto.CreateGitLabSourceResponse, error) {
	if !s.gitLabReady() {
		s.logger.ErrorCtx(ctx, "CreateGitLabSource 调用但 GitLab 依赖未注入", nil, map[string]any{
			"has_ui_store": s.uiStore != nil, "has_enqueuer": s.enqueuer != nil, "has_factory": s.gitLabFactory != nil,
		})
		return nil, fmt.Errorf("gitlab deps not wired: %w", source.ErrSourceInternal)
	}

	// ── 1. 取值校验 ────────────────────────────────────────────────────────
	visibility := req.Visibility
	if visibility == "" {
		visibility = model.VisibilityOrg
	}
	if !model.IsValidVisibility(visibility) {
		s.logger.WarnCtx(ctx, "visibility 取值非法", map[string]any{"value": visibility})
		return nil, fmt.Errorf("invalid visibility: %w", source.ErrSourceInvalidVisibility)
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = source.DefaultGitLabBranch
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id required: %w", source.ErrSourceInvalidRequest)
	}
	pat := strings.TrimSpace(req.PAT)
	if pat == "" {
		return nil, fmt.Errorf("pat required: %w", source.ErrSourceInvalidRequest)
	}
	baseURL := strings.TrimSpace(req.BaseURL)

	// ── 2. 验 PAT + 拿 GitLab user id ─────────────────────────────────────
	cli := s.gitLabFactory(baseURL, pat)
	gitUser, err := cli.VerifyToken(ctx)
	if err != nil {
		s.logger.WarnCtx(ctx, "GitLab PAT 验证失败", map[string]any{"caller": callerUserID, "err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// ── 3. upsert user_integrations ────────────────────────────────────────
	externalAccountID := strconv.FormatUint(gitUser.ID, 10)
	ui := &uimodel.UserIntegration{
		UserID:            callerUserID,
		OrgID:             orgID,
		Provider:          user_integration.ProviderGitLab,
		ExternalAccountID: externalAccountID,
		AccountName:       gitUser.Username,
		AccountEmail:      gitUser.Email,
		AccessToken:       pat, // 明文存:决策见 internal/user_integration/const.go 安全备注
		Status:            user_integration.StatusActive,
	}
	if baseURL != "" {
		// provider_meta 走 jsonb,字段约定:base_url 给自托管实例用
		raw, mErr := jsonObj(map[string]string{"base_url": baseURL})
		if mErr != nil {
			return nil, fmt.Errorf("marshal provider_meta: %w: %w", mErr, source.ErrSourceInternal)
		}
		ui.ProviderMeta = raw
	}
	if err := s.uiStore.Upsert(ctx, ui); err != nil {
		s.logger.ErrorCtx(ctx, "upsert user_integration 失败", err, map[string]any{
			"caller": callerUserID, "external_account_id": externalAccountID,
		})
		return nil, fmt.Errorf("upsert user_integration: %w: %w", err, source.ErrSourceInternal)
	}
	// 回读拿 id —— Upsert 在冲突路径下并不一定回填 ID(GORM 不同版本行为差),显式 GetByUserProvider 兜稳。
	uiPersisted, err := s.uiStore.GetByUserProvider(ctx, callerUserID, user_integration.ProviderGitLab, externalAccountID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "回读 user_integration 失败", err, map[string]any{
			"caller": callerUserID, "external_account_id": externalAccountID,
		})
		return nil, fmt.Errorf("reload user_integration: %w: %w", err, source.ErrSourceInternal)
	}

	// ── 4. 校验 project 可读 + 取 path_with_namespace ──────────────────────
	proj, err := cli.GetProject(ctx, projectID)
	if err != nil {
		s.logger.WarnCtx(ctx, "GetProject 失败", map[string]any{"project_id": projectID, "err": err.Error()})
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}

	// ── 5. 生成 webhook secret 明文 + hash ──────────────────────────────────
	secretPlain, secretHash, err := generateWebhookSecret()
	if err != nil {
		s.logger.ErrorCtx(ctx, "生成 webhook secret 失败", err, nil)
		return nil, fmt.Errorf("gen webhook secret: %w: %w", err, source.ErrSourceInternal)
	}

	// ── 6. 创建 source 行 ──────────────────────────────────────────────────
	// name 选 path_with_namespace —— 比裸 project_id 可读;DB 端 uk_sources_owner_name 兜重名(同 owner 同 name 拒)。
	srcModel := &model.Source{
		OrgID:                   orgID,
		Kind:                    model.KindGitLabRepo,
		OwnerUserID:             callerUserID,
		ExternalRef:             projectID,
		Name:                    proj.PathWithNamespace,
		Visibility:              visibility,
		GitLabIntegrationID:     uiPersisted.ID,
		GitLabBranch:            branch,
		GitLabWebhookSecretHash: secretHash,
		LastSyncStatus:          model.SyncStatusNever,
	}

	// 重名预检 — uk_sources_owner_name 兜底,但提前预检让错误信息友好(409 vs 500)。
	if existing, findErr := s.repo.FindSourceByOwnerAndName(ctx, orgID, callerUserID, proj.PathWithNamespace); findErr == nil && existing != nil {
		s.logger.WarnCtx(ctx, "gitlab repo 同名 source 已存在", map[string]any{
			"owner": callerUserID, "name": proj.PathWithNamespace, "existing_id": existing.ID,
		})
		return nil, fmt.Errorf("name taken: %w", source.ErrSourceNameExists)
	} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		s.logger.ErrorCtx(ctx, "查重名 source 失败", findErr, nil)
		return nil, fmt.Errorf("check name: %w: %w", findErr, source.ErrSourceInternal)
	}

	if err := s.repo.CreateSource(ctx, srcModel); err != nil {
		s.logger.ErrorCtx(ctx, "create gitlab_repo source 失败", err, map[string]any{
			"owner": callerUserID, "project_id": projectID,
		})
		return nil, fmt.Errorf("create source: %w: %w", err, source.ErrSourceInternal)
	}

	// ── 7. 入队全量 sync ───────────────────────────────────────────────────
	jobID, err := s.enqueuer.EnqueueFullSync(ctx, orgID, callerUserID, srcModel.ID)
	if err != nil {
		// source 已经建好了 — sync 失败不回滚,owner 可调 TriggerGitLabResync 重试。日志即可。
		s.logger.WarnCtx(ctx, "首次 enqueue full sync 失败,owner 可手动重试", map[string]any{
			"source_id": srcModel.ID, "err": err.Error(),
		})
	}

	s.logger.InfoCtx(ctx, "gitlab_repo source 创建成功", map[string]any{
		"source_id": srcModel.ID, "owner": callerUserID, "project_id": projectID, "branch": branch, "job_id": jobID,
	})

	resp := &dto.CreateGitLabSourceResponse{
		Source:        sourceToDTO(srcModel),
		WebhookSecret: secretPlain,
		JobID:         jobID,
	}
	if s.publicBaseURL != "" {
		// 服务端有公网根 URL → 直接拼完整 webhook URL,owner 复制即可粘 GitLab。
		// 没配 → 留空,前端 fallback 到 window.location.origin 并显示 localhost 警告。
		resp.WebhookURL = fmt.Sprintf("%s/api/v2/webhooks/gitlab/%d", s.publicBaseURL, srcModel.ID)
	}
	return resp, nil
}

// DeleteGitLabSource 见接口注释。perm gate 在 router 层。
func (s *sourceService) DeleteGitLabSource(ctx context.Context, orgID, sourceID, callerUserID uint64) error {
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return err
	}
	if src.Kind != model.KindGitLabRepo {
		// 防止 owner 用 GitLab 端点误删 manual_upload 行(虽然 perm 守住了,但语义上要拒)
		s.logger.WarnCtx(ctx, "DeleteGitLabSource 调用但 source 非 gitlab_repo", map[string]any{
			"source_id": sourceID, "kind": src.Kind,
		})
		return fmt.Errorf("not a gitlab source: %w", source.ErrSourceNotFound)
	}
	// 复用现有 DeleteSource 逻辑(doc 计数 + ACL 清理 + 写 audit)
	// callerUserID 必须是 source.OwnerUserID — 即"perm 持有人 + source 持有人"双校验。
	// owner 在过去转让又没转 source → DeleteGitLabSource 会 403,owner 必须先重新 grant 凭据再删。
	if src.OwnerUserID != callerUserID {
		s.logger.WarnCtx(ctx, "DeleteGitLabSource 非 source owner", map[string]any{
			"source_id": sourceID, "caller": callerUserID, "owner": src.OwnerUserID,
		})
		return fmt.Errorf("only source owner: %w", source.ErrSourceForbidden)
	}
	return s.DeleteSource(ctx, orgID, sourceID, callerUserID)
}

// GetGitLabSyncStatus 见接口注释。
//
// 没注入 jobLookup → 返 ErrSourceInternal(装配错)。
// source 不存在 / 非 gitlab_repo → ErrSourceNotFound。
// 没找到任何匹配 job → 返 Status="never"。
func (s *sourceService) GetGitLabSyncStatus(
	ctx context.Context,
	orgID, sourceID, _ uint64,
) (*dto.GitLabSyncStatusResponse, error) {
	if s.jobLookup == nil {
		return nil, fmt.Errorf("gitlab sync status: jobLookup not wired: %w", source.ErrSourceInternal)
	}
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if src.Kind != model.KindGitLabRepo {
		return nil, fmt.Errorf("not a gitlab source: %w", source.ErrSourceNotFound)
	}

	prefix := fmt.Sprintf("gitlab:%d:", src.ID)
	job, err := s.jobLookup.FindLatestByKeyPrefix(ctx, orgID, "integration.sync.gitlab", prefix)
	if err != nil {
		s.logger.ErrorCtx(ctx, "gitlab sync status: lookup failed", err, map[string]any{"source_id": src.ID})
		return nil, fmt.Errorf("lookup async job: %w: %w", err, source.ErrSourceInternal)
	}
	if job == nil {
		return &dto.GitLabSyncStatusResponse{Status: "never"}, nil
	}

	// 从 IdempotencyKey 解 mode:"gitlab:<id>:full" / "gitlab:<id>:incr:<sha>"
	mode := ""
	if rest, ok := strings.CutPrefix(job.IdempotencyKey, prefix); ok {
		if strings.HasPrefix(rest, "incr:") {
			mode = "incremental"
		} else if rest == "full" {
			mode = "full"
		}
	}

	return &dto.GitLabSyncStatusResponse{
		JobID:          job.ID,
		Status:         job.Status,
		Mode:           mode,
		ProgressDone:   job.ProgressDone,
		ProgressTotal:  job.ProgressTotal,
		ProgressFailed: job.ProgressFailed,
		StartedAt:      job.StartedAt,
		FinishedAt:     job.FinishedAt,
		HeartbeatAt:    job.HeartbeatAt,
		Error:          job.Error,
	}, nil
}

// TriggerGitLabResync 见接口注释。
func (s *sourceService) TriggerGitLabResync(ctx context.Context, orgID, sourceID, callerUserID uint64) (*dto.TriggerResyncResponse, error) {
	if s.enqueuer == nil {
		return nil, fmt.Errorf("gitlab enqueuer not wired: %w", source.ErrSourceInternal)
	}
	src, err := s.loadSource(ctx, orgID, sourceID)
	if err != nil {
		//sayso-lint:ignore sentinel-wrap
		return nil, err
	}
	if src.Kind != model.KindGitLabRepo {
		return nil, fmt.Errorf("not a gitlab source: %w", source.ErrSourceNotFound)
	}
	jobID, err := s.enqueuer.EnqueueFullSync(ctx, orgID, callerUserID, src.ID)
	if err != nil {
		s.logger.ErrorCtx(ctx, "TriggerGitLabResync enqueue 失败", err, map[string]any{"source_id": src.ID})
		return nil, fmt.Errorf("enqueue full sync: %w: %w", err, source.ErrSourceInternal)
	}
	return &dto.TriggerResyncResponse{JobID: jobID}, nil
}

// ─── 私有辅助 ────────────────────────────────────────────────────────────────

// gitLabReady GitLab 三方法都需要的依赖检查。
func (s *sourceService) gitLabReady() bool {
	return s.uiStore != nil && s.enqueuer != nil && s.gitLabFactory != nil
}

// generateWebhookSecret 32 字节 random → base64 url-safe 明文,SHA-256 hex hash。
// 明文 ~ 43 字符可粘进 GitLab UI;hash 64 字符存 DB(varchar 64 对齐)。
func generateWebhookSecret() (plain, hashHex string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		//sayso-lint:ignore log-coverage
		return "", "", fmt.Errorf("rand: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	hashHex = hex.EncodeToString(sum[:])
	return plain, hashHex, nil
}

// jsonObj marshal map[string]string into datatypes.JSON via json package without
// importing it twice (避免循环 / 包间不一致)。
func jsonObj(m map[string]string) ([]byte, error) {
	// 简单 map 用标准库 json 即可,但为避免 source.service 直接 import "encoding/json" 重复
	// (本文件其他逻辑也不需要),内联一个最小 marshaler。键值都是 string,无嵌套。
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	var sb strings.Builder
	sb.WriteByte('{')
	first := true
	for k, v := range m {
		if !first {
			sb.WriteByte(',')
		}
		first = false
		sb.WriteString(strconv.Quote(k))
		sb.WriteByte(':')
		sb.WriteString(strconv.Quote(v))
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

