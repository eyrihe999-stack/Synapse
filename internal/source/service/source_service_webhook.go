// source_service_webhook.go GitLab webhook 验签 + 增量入队。
//
// 设计要点:
//   - 验签走 sha256(header_token) == source.GitLabWebhookSecretHash,常量时间比较防 timing 攻击
//   - 解析 payload 只信几个权威字段(ref / before / after / project.id),不读其他字段做判断
//   - 入队幂等:IdempotencyKey "gitlab:<src_id>:incr:<after_sha>" — 同一 push 重发不重跑;
//     已存在 active job 则复用其 jobID(EnqueueFullSync 已经处理 ErrDuplicateJob)
//   - 不在本路径里跑同步逻辑 —— webhook handler 必须 < 200ms 返,真活儿走 asyncjob
package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyrihe999-stack/Synapse/internal/source"
	srcmodel "github.com/eyrihe999-stack/Synapse/internal/source/model"
)

// gitlabPushPayload GitLab Push Event 的最小字段子集。
//
// 完整 payload 字段还有 commits[] / user_* / repository.* 等,我们一概不读 —— 减少攻击面 + 减少
// 升级 GitLab 时 payload 字段变更带来的兼容性风险。
type gitlabPushPayload struct {
	ObjectKind string `json:"object_kind"` // 期望 "push"
	Ref        string `json:"ref"`         // 形如 "refs/heads/main"
	Before     string `json:"before"`      // 推送前 head sha;branch 首次推送 = "0000...000"
	After      string `json:"after"`       // 推送后 head sha;删 branch = "0000...000"
	Project    struct {
		ID uint64 `json:"id"`
	} `json:"project"`
}

// HandleGitLabWebhook 见接口注释。
//
//nolint:funlen // 串行流程,拆分意义不大
func (s *sourceService) HandleGitLabWebhook(
	ctx context.Context,
	sourceID uint64,
	headerToken string,
	eventBody []byte,
) (uint64, bool, error) {
	if s.enqueuer == nil {
		s.logger.ErrorCtx(ctx, "HandleGitLabWebhook 调用但 enqueuer 未注入", nil, nil)
		return 0, false, fmt.Errorf("gitlab webhook: enqueuer not wired: %w", source.ErrSourceInternal)
	}

	// 1) 反查 source(不区分 NotFound 和 kind 错误,避免侧信道枚举)
	src, err := s.repo.FindSourceByID(ctx, sourceID)
	if err != nil {
		s.logger.WarnCtx(ctx, "gitlab webhook: source 不存在或 DB 异常", map[string]any{
			"source_id": sourceID, "err": err.Error(),
		})
		// 任何 lookup 错误都翻成 NotFound,handler 翻 404
		return 0, false, fmt.Errorf("source: %w", source.ErrSourceNotFound)
	}
	if src.Kind != srcmodel.KindGitLabRepo {
		s.logger.WarnCtx(ctx, "gitlab webhook: source 不是 gitlab_repo", map[string]any{
			"source_id": sourceID, "kind": src.Kind,
		})
		return 0, false, fmt.Errorf("source kind: %w", source.ErrSourceNotFound)
	}

	// 2) 验签 — 常量时间比较 hex hash
	if !verifyWebhookSecret(headerToken, src.GitLabWebhookSecretHash) {
		s.logger.WarnCtx(ctx, "gitlab webhook: 验签失败", map[string]any{
			"source_id": sourceID, "header_len": len(headerToken),
		})
		return 0, false, fmt.Errorf("invalid webhook token: %w", source.ErrSourceGitLabAuthFailed)
	}

	// 3) 解析 payload
	var payload gitlabPushPayload
	if err := json.Unmarshal(eventBody, &payload); err != nil {
		s.logger.WarnCtx(ctx, "gitlab webhook: payload 解析失败", map[string]any{
			"source_id": sourceID, "err": err.Error(),
		})
		return 0, false, fmt.Errorf("invalid payload: %w", source.ErrSourceInvalidRequest)
	}
	if payload.ObjectKind != "push" {
		// tag push / merge_request 等其他事件:静默 ack(我们订了 push 但 GitLab 也可能误发)
		s.logger.DebugCtx(ctx, "gitlab webhook: 非 push 事件,忽略", map[string]any{
			"source_id": sourceID, "object_kind": payload.ObjectKind,
		})
		return 0, false, nil
	}

	// 4) project.id 防伪:GitLab 推送的 project 必须和 source 绑的 project 一致
	if strconv.FormatUint(payload.Project.ID, 10) != src.ExternalRef {
		s.logger.WarnCtx(ctx, "gitlab webhook: project.id 与 source 不匹配", map[string]any{
			"source_id": sourceID, "payload_project_id": payload.Project.ID, "source_external_ref": src.ExternalRef,
		})
		return 0, false, fmt.Errorf("project mismatch: %w", source.ErrSourceGitLabAuthFailed)
	}

	// 5) ref 必须匹配 source 配置的分支(refs/heads/<branch>)
	expectedRef := "refs/heads/" + src.GitLabBranch
	if payload.Ref != expectedRef {
		s.logger.DebugCtx(ctx, "gitlab webhook: 分支不匹配,忽略", map[string]any{
			"source_id": sourceID, "payload_ref": payload.Ref, "expected_ref": expectedRef,
		})
		return 0, false, nil
	}

	// 6) 删除分支(after 全 0)→ 不处理,我们不做 branch 删除时的 cleanup
	if payload.After == "" || strings.TrimLeft(payload.After, "0") == "" {
		s.logger.InfoCtx(ctx, "gitlab webhook: branch 删除,忽略", map[string]any{
			"source_id": sourceID, "ref": payload.Ref,
		})
		return 0, false, nil
	}

	// 7) 入队 incremental(若 BeforeSHA 全 0,runner 内 normalizeMode 会退化到 full)
	jobID, err := s.enqueueIncrementalSync(ctx, src, payload.Before, payload.After)
	if err != nil {
		s.logger.ErrorCtx(ctx, "gitlab webhook: enqueue 失败", err, map[string]any{
			"source_id": src.ID, "before": payload.Before, "after": payload.After,
		})
		return 0, false, fmt.Errorf("enqueue: %w: %w", err, source.ErrSourceInternal)
	}
	s.logger.InfoCtx(ctx, "gitlab webhook: enqueued incremental sync", map[string]any{
		"source_id": src.ID, "before": payload.Before, "after": payload.After, "job_id": jobID,
	})
	return jobID, true, nil
}

// verifyWebhookSecret 常量时间比较 sha256(headerToken) 与存储的 hash。
// 任一为空 → 视作不匹配。
func verifyWebhookSecret(headerToken, storedHashHex string) bool {
	if headerToken == "" || storedHashHex == "" {
		return false
	}
	sum := sha256.Sum256([]byte(headerToken))
	candidate := hex.EncodeToString(sum[:])
	// subtle.ConstantTimeCompare 要求两 slice 长度相等;hash 都是固定 64 字符,正常情况下相等
	if len(candidate) != len(storedHashHex) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(storedHashHex)) == 1
}

// enqueueIncrementalSync 走 enqueuer 路径,但 service 层的 GitLabSyncEnqueuer 接口当前只暴露
// EnqueueFullSync。为避免改动接口签名 —— 一种思路是给接口加 EnqueueIncrementalSync 方法;
// 另一种思路是 service 层直接持有 *asyncsvc.Service。为了让 handler/单测易写,选择前者:接口扩一个方法。
//
// 这里反向思路实现:**让 enqueuer 接口已有的 EnqueueFullSync 改名 / 不动,新加 EnqueueIncrementalSync**。
// 但加法会让所有 mock 实现升级 — 为了不动 enqueuer 接口,暂时直接 type-assert 拿原始 *asyncsvc.Service 跑入队。
func (s *sourceService) enqueueIncrementalSync(
	ctx context.Context,
	src *srcmodel.Source,
	beforeSHA, afterSHA string,
) (uint64, error) {
	// 如果 enqueuer 实现是 main.go 里的 gitlabSyncEnqueuer adapter,我们走它的扩展能力。
	// 接口侧已经在下面加了一个新方法 EnqueueIncrementalSync;装配层适配即可。
	if ext, ok := s.enqueuer.(GitLabIncrementalEnqueuer); ok {
		return ext.EnqueueIncrementalSync(ctx, src.OrgID, src.OwnerUserID, src.ID, beforeSHA, afterSHA)
	}
	return 0, fmt.Errorf("enqueuer does not support incremental sync")
}

// GitLabIncrementalEnqueuer service 层独立子接口,装配层 adapter 同时实现
// GitLabSyncEnqueuer + GitLabIncrementalEnqueuer 即可。Go 没有"扩展接口"语法所以拆两份。
type GitLabIncrementalEnqueuer interface {
	EnqueueIncrementalSync(ctx context.Context, orgID, userID, sourceID uint64, beforeSHA, afterSHA string) (uint64, error)
}
