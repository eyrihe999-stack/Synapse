// Package upload 是"单文件上传"的 Fetcher 实现。
//
// 和其他 source 的差别:upload 的原文由 handler 上传 OSS 后,
// Fetcher 内部按 OSS key 拉回字节,再 emit 给 pipeline。
//
// 使用场景:
//
//	handler 读 multipart → OSS PutObject → asyncjob.Schedule(payload 只带 OSS key)
//	→ docupload runner 构造 Fetcher(持有 OSS client)→ pipeline.Run
//	→ Fetcher.Fetch 里 GetObject(key) → emit NormalizedDoc
//	→ pipeline 走 chunk → embed → persist
//
// 生命周期:handler 构造 payload → runner 构造 Fetcher → GC。不跨请求复用。
package upload

import (
	"context"
	"fmt"

	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	docpersister "github.com/eyrihe999-stack/Synapse/internal/ingestion/persister/document"
)

// Input 构造 Fetcher 所需的全部元数据。Content 不再走 Input —— 由 OSSKey + OSS client 按需拉。
type Input struct {
	OrgID             uint64
	UploaderID        uint64
	DocID             uint64 // 预分配的雪花 id(覆盖模式下是原 doc 的 id,新上传则是新 id)
	KnowledgeSourceID uint64 // 权限承载的 source.id;handler 通过 source.EnsureManualUpload 提前确保
	FileName          string
	Title             string
	MIMEType          string // 浏览器填的,空就靠 chunker selector 的扩展名兜底
	OSSKey            string // OSS object key,handler 上传成功后塞进 asyncjob payload
	ContentHash       string // 调用方算好的 sha256 hex,作为 documents.version 写入
}

// Fetcher 见 ingestion.Fetcher。持有 OSS client 做按 key 拉原文。
type Fetcher struct {
	in  Input
	oss ossupload.Client
}

// New 构造。oss 为 nil 会在 Fetch 时返 error,不在构造期校验让调用方语义更松。
func New(in Input, oss ossupload.Client) *Fetcher {
	return &Fetcher{in: in, oss: oss}
}

// SourceType 恒为 document(所有文档类 source 共用 persister)。
func (f *Fetcher) SourceType() string { return ingestion.SourceTypeDocument }

// Fetch 从 OSS 拉原文字节,一次性 emit 一个 NormalizedDoc。
//
// source_id 约定:"upload:<docID>" —— docID 一对一锁定 documents 行,
// 覆盖重传同一个 docID → persister 的 GetVersion 命中已有 doc,走原地更新。
//
// 错误场景:
//   - DocID / OrgID / OSSKey / ContentHash 任一缺失 → handler 组装错(调用方责任)
//   - OSS client 为 nil 或 GetObject 失败 → 返 error,pipeline 上抛终止本轮
//   - emit 回调返 error → pipeline 内部已打日志,这里直通上抛
func (f *Fetcher) Fetch(ctx context.Context, emit ingestion.Emit) error {
	if f.in.DocID == 0 {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: DocID is zero")
	}
	if f.in.OrgID == 0 {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: OrgID is zero")
	}
	if f.in.OSSKey == "" {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: OSSKey is empty (handler must upload OSS first)")
	}
	if f.in.ContentHash == "" {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: ContentHash is empty (handler must precompute)")
	}
	if f.oss == nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: OSS client is nil")
	}

	content, err := f.oss.GetObject(ctx, f.in.OSSKey)
	if err != nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("upload fetcher: get oss object %s: %w", f.in.OSSKey, err)
	}

	// Title 兜底:缺失时用 file_name(不带扩展名由前端决定;这里不剥)
	title := f.in.Title
	if title == "" {
		title = f.in.FileName
	}

	doc := &ingestion.NormalizedDoc{
		OrgID:             f.in.OrgID,
		DocID:             f.in.DocID, // 预分配 id,persister 必须落此 id
		SourceType:        ingestion.SourceTypeDocument,
		SourceID:          fmt.Sprintf("upload:%d", f.in.DocID),
		Version:           f.in.ContentHash,
		Title:             title,
		MIMEType:          f.in.MIMEType,
		FileName:          f.in.FileName,
		Content:           content,
		UploaderID:        f.in.UploaderID,
		KnowledgeSourceID: f.in.KnowledgeSourceID,
		ExternalRef: ingestion.ExternalRef{
			Kind:   "upload",
			OSSKey: f.in.OSSKey,
			// URI 留空:bucket 私读场景下展示链走服务端代下载接口,不暴露 OSS 直链
		},
		Payload: &docpersister.Payload{
			Provider: "upload",
		},
	}
	//sayso-lint:ignore log-coverage
	return emit(ctx, doc)
}
