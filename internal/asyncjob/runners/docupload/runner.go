// Package docupload asyncjob runner for single-file document upload.
//
// Kind = "document.upload",实现 ConcurrentRunner 允许同用户同时排多个任务(多文件拖拽场景)。
//
// Flow:
//
//	handler  → OSS PutObject → asyncjob.Schedule(Kind="document.upload", Payload=Input{OSSKey,...})
//	runner   → 反序列化 Input → upload.Fetcher(持有 OSS client)→ pipeline.Run
//	fetcher  → OSS GetObject(key) → emit NormalizedDoc
//	pipeline → chunker → embedder → persister → PG
//
// Payload 不再带 file body —— 改走 OSS key,async_jobs.payload 体积稳定 ~300B。
package docupload

import (
	"context"
	"encoding/json"
	"fmt"

	asyncsvc "github.com/eyrihe999-stack/Synapse/internal/asyncjob/service"

	"github.com/eyrihe999-stack/Synapse/internal/asyncjob/model"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion/source/upload"
)

// Kind 常量。和 asyncjob/model.Kind* 常量并列(但为了让 docupload 独立,Kind 字面量在本包声明)。
const Kind = "document.upload"

// Input runner 的 payload 契约。handler 把它 JSON 序列化塞进 Job.Payload。
// OSSKey 是 handler 上传 OSS 成功后拿到的 object key;runner 只认它,不直接持有字节。
type Input struct {
	OrgID             uint64 `json:"org_id"`
	UploaderID        uint64 `json:"uploader_id"`
	DocID             uint64 `json:"doc_id"`
	KnowledgeSourceID uint64 `json:"knowledge_source_id"` // 权限承载的 source.id;handler 调 source.EnsureManualUpload 提前拿
	FileName          string `json:"file_name"`
	Title             string `json:"title,omitempty"`
	MIMEType          string `json:"mime_type,omitempty"`
	ContentHash       string `json:"content_hash"`
	OSSKey            string `json:"oss_key"`
}

// Result 终态结果摘要。asyncjob 会把它 marshal 后写 Job.Result,再被 JobResponse.Result 原样透传给前端。
// DocID 走 `,string` tag(snowflake uint64 超 JS Number 精度)。
type Result struct {
	DocID      uint64 `json:"doc_id,string"`
	ChunkCount int    `json:"chunk_count"` // 不在本 runner 统计 —— 先留 0,后续查 DB 补
}

// Runner 实现 asyncjob.service.ConcurrentRunner。
type Runner struct {
	pipeline *ingestion.Pipeline
	oss      ossupload.Client
	log      logger.LoggerInterface
}

// New 构造。pipeline / oss / log 均不可为 nil;调用方(cmd/synapse)在装配时保证。
// 返 error 让上层在启动期 fatal,避免运行期崩在 Schedule 路径上。
// error 前不打 log:log 本身是参数,可能 nil,由 caller 负责落日志。
func New(pipeline *ingestion.Pipeline, oss ossupload.Client, log logger.LoggerInterface) (*Runner, error) {
	if pipeline == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("docupload: nil pipeline")
	}
	if oss == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("docupload: nil oss client")
	}
	if log == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("docupload: nil log")
	}
	return &Runner{pipeline: pipeline, oss: oss, log: log}, nil
}

// Kind 见 asyncjob.service.Runner。
func (*Runner) Kind() string { return Kind }

// AllowConcurrent 见 asyncjob.service.ConcurrentRunner。
// upload 天然并行,不做 (user,kind) 防重 —— 用户多文件拖拽要并发生效。
func (*Runner) AllowConcurrent() bool { return true }

// Run 反序列化 payload → 构造 upload.Fetcher(持有 OSS client)→ pipeline.Run。
//
// 可能返回:
//   - payload JSON 解析失败
//   - payload 缺必填字段(doc_id / org_id / oss_key / content_hash)
//   - pipeline 本身返回的 chunk / embed / persist 错误(含 OSS GetObject 失败)
func (r *Runner) Run(ctx context.Context, job *model.Job, reporter asyncsvc.ProgressReporter) (any, error) {
	var in Input
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		r.log.ErrorCtx(ctx, "docupload: unmarshal payload failed", err, map[string]any{"job_id": job.ID})
		return nil, fmt.Errorf("docupload: unmarshal payload: %w", err)
	}
	if in.DocID == 0 || in.OrgID == 0 || in.OSSKey == "" || in.ContentHash == "" {
		r.log.WarnCtx(ctx, "docupload: payload missing fields", map[string]any{
			"job_id": job.ID, "doc_id": in.DocID, "org_id": in.OrgID,
			"has_oss_key": in.OSSKey != "", "has_hash": in.ContentHash != "",
		})
		return nil, fmt.Errorf("docupload: payload missing required fields")
	}

	// 不再在 runner 层 SetTotal —— pipeline.Prepare 切块后会按 chunk 数 SetTotal,
	// embedChunks 每批 Inc,前端能看到 0/N → N/N 连续推进,不再卡 0% 到突变。
	fetcher := upload.New(upload.Input{
		OrgID:             in.OrgID,
		UploaderID:        in.UploaderID,
		DocID:             in.DocID,
		KnowledgeSourceID: in.KnowledgeSourceID,
		FileName:          in.FileName,
		Title:             in.Title,
		MIMEType:          in.MIMEType,
		OSSKey:            in.OSSKey,
		ContentHash:       in.ContentHash,
	}, r.oss)

	if err := r.pipeline.Run(ctx, fetcher, reporter); err != nil {
		return nil, fmt.Errorf("docupload: pipeline: %w", err)
	}
	return Result{DocID: in.DocID}, nil
}
