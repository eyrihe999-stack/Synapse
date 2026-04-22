// persister.go 实现 ingestion.Persister,把 NormalizedDoc + chunks + vecs 落到 PG。
//
// 关键语义(和 ingestion.Persister 契约对齐):
//
//   - embedErr == nil          → chunks 全部 indexed,embedding 填好
//   - embedErr != nil (非致命) → chunks 全部 failed,embedding=NULL,index_error 写摘要,
//                                 persister 返 nil(pipeline 视为成功,后台补偿)
//   - 致命 embed 错已被 pipeline 拦截,不会进 persister
//   - 自身 DB/IO 错 → 返 error,pipeline 上抛终止整轮
package document

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"unicode/utf8"

	doc "github.com/eyrihe999-stack/Synapse/internal/document"
	docmodel "github.com/eyrihe999-stack/Synapse/internal/document/model"
	docrepo "github.com/eyrihe999-stack/Synapse/internal/document/repository"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/idgen"
	"github.com/lib/pq"
	"gorm.io/datatypes"
)

// Persister 实现 ingestion.Persister,绑定 source_type="document"。
type Persister struct {
	repo docrepo.Repository
	log  logger.LoggerInterface
}

// New 构造。repo + log 都必填。repo 或 log 为 nil 返 error(装配期配置错,调用方应 fatal)。
// 此处不打日志:log 可能就是 nil,另一条 repo==nil 也只在 bootstrap 触发,main 会用 appLogger fatal。
func New(repo docrepo.Repository, log logger.LoggerInterface) (*Persister, error) {
	if repo == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion/persister/document: nil repo")
	}
	if log == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion/persister/document: nil log")
	}
	return &Persister{repo: repo, log: log}, nil
}

// SourceType 见 ingestion.Persister。
func (p *Persister) SourceType() string { return ingestion.SourceTypeDocument }

// Persist 见 ingestion.Persister。
//
// 错误场景:
//   - nd 为 nil → 参数校验失败
//   - repo.GetVersion / UpsertWithChunks → DB 不可达 / schema 错(fatal,pipeline 会上抛终止整轮)
//   - idgen.GenerateID 失败 → snowflake 节点没初始化
//   - toDBDoc / buildChunkWithVecs 的 JSON marshal 失败 → payload / metadata 结构异常
func (p *Persister) Persist(
	ctx context.Context,
	nd *ingestion.NormalizedDoc,
	chunks []ingestion.IngestedChunk,
	vecs [][]float32,
	embedErr error,
) error {
	if nd == nil {
		//sayso-lint:ignore err-shadow
		err := fmt.Errorf("document persister: nil normalized doc")
		p.log.ErrorCtx(ctx, "document persister: nil normalized doc", err, nil)
		return err
	}

	// 1. 决定 docID。优先级:
	//      (a) fetcher 预分配(nd.DocID != 0)—— upload 类路径,handler 已生成 snowflake 并返给客户端
	//      (b) 源端幂等键已存在 —— 复用旧 id(保持 FK 稳定、外部链接不断)
	//      (c) 生成新 snowflake —— sync 类路径,首次摄入
	v, err := p.repo.GetVersion(ctx, nd.OrgID, nd.SourceType, nd.SourceID)
	if err != nil {
		p.log.ErrorCtx(ctx, "document persister: get version", err, map[string]any{
			"org_id":      nd.OrgID,
			"source_type": nd.SourceType,
			"source_id":   nd.SourceID,
		})
		return fmt.Errorf("document persister: get version: %w", err)
	}
	var docID uint64
	switch {
	case nd.DocID != 0:
		docID = nd.DocID
	case v.Exists:
		docID = v.DocID
	default:
		//sayso-lint:ignore err-shadow
		id, err := idgen.GenerateID()
		if err != nil {
			p.log.ErrorCtx(ctx, "document persister: generate doc id", err, map[string]any{
				"org_id":    nd.OrgID,
				"source_id": nd.SourceID,
			})
			return fmt.Errorf("document persister: generate doc id: %w", err)
		}
		docID = uint64(id)
	}

	// 2. NormalizedDoc → model.Document
	dbDoc, err := toDBDoc(nd, docID, len(chunks), len(nd.Content))
	if err != nil {
		p.log.ErrorCtx(ctx, "document persister: build db doc", err, map[string]any{
			"org_id":    nd.OrgID,
			"source_id": nd.SourceID,
			"doc_id":    docID,
		})
		return fmt.Errorf("document persister: build db doc: %w", err)
	}

	// 3. ingestion.IngestedChunk → repository.ChunkWithVec
	//    - 给每个 chunk 分配 snowflake id
	//    - 按 IngestedChunk.ParentIndex 查映射表回填 ParentChunkID
	//    - embedErr 非致命 → vec 全部传 nil(repo 层自动置 index_status="failed"),同时写 IndexError 摘要
	errSummary := ""
	if embedErr != nil {
		errSummary = truncateToBytes(embedErr.Error(), 255)
	}

	cws, err := buildChunkWithVecs(chunks, vecs, embedErr != nil, errSummary, docID, nd.OrgID)
	if err != nil {
		p.log.ErrorCtx(ctx, "document persister: build chunks", err, map[string]any{
			"org_id":      nd.OrgID,
			"source_id":   nd.SourceID,
			"doc_id":      docID,
			"chunk_count": len(chunks),
		})
		return fmt.Errorf("document persister: build chunks: %w", err)
	}

	// 4. 落库
	//sayso-lint:ignore err-swallow
	if _, err := p.repo.UpsertWithChunks(ctx, dbDoc, cws); err != nil {
		p.log.ErrorCtx(ctx, "document persister: upsert", err, map[string]any{
			"org_id":      nd.OrgID,
			"source_id":   nd.SourceID,
			"doc_id":      docID,
			"chunk_count": len(chunks),
		})
		return fmt.Errorf("document persister: upsert: %w", err)
	}

	if embedErr != nil {
		p.log.WarnCtx(ctx, "document persister: embed non-fatal, chunks saved as failed", map[string]any{
			"org_id":      nd.OrgID,
			"source_id":   nd.SourceID,
			"doc_id":      docID,
			"chunk_count": len(chunks),
			"err":         errSummary,
		})
	}
	return nil
}

// ─── 转换辅助 ───────────────────────────────────────────────────────────────

// toDBDoc 把 NormalizedDoc + 已知 docID / chunkCount / contentBytes 拼成 DB 行。
func toDBDoc(nd *ingestion.NormalizedDoc, docID uint64, chunkCount, contentBytes int) (*docmodel.Document, error) {
	provider := ""
	externalRefExtra := datatypes.JSON(nil)
	if nd.Payload != nil {
		// 同时接受 *Payload 和 Payload(value),调用方两种姿势都 OK
		var p *Payload
		switch v := nd.Payload.(type) {
		case *Payload:
			p = v
		case Payload:
			p = &v
		}
		if p != nil {
			provider = p.Provider
			if len(p.ProviderMeta) > 0 || p.ExternalOwnerID != "" {
				extra := make(map[string]string, len(p.ProviderMeta)+1)
				maps.Copy(extra, p.ProviderMeta)
				if p.ExternalOwnerID != "" {
					extra["external_owner_id"] = p.ExternalOwnerID
				}
				raw, err := json.Marshal(extra)
				if err != nil {
					//sayso-lint:ignore log-coverage
					return nil, fmt.Errorf("marshal external_ref_extra: %w", err)
				}
				externalRefExtra = datatypes.JSON(raw)
			}
		}
	}

	// ExternalRef 其余字段直接从 NormalizedDoc 拷。
	// OSS key 复用 ExternalRef.OSSKey 约定——upload fetcher 在 Fetch 里 set,这里直接落库列。
	doc := &docmodel.Document{
		ID:               docID,
		OrgID:            nd.OrgID,
		SourceType:       nd.SourceType,
		Provider:         provider,
		SourceID:         nd.SourceID,
		Title:            nd.Title,
		MIMEType:         nd.MIMEType,
		FileName:         nd.FileName,
		Version:          nd.Version,
		OSSKey:           nd.ExternalRef.OSSKey,
		ExternalRefKind:  nd.ExternalRef.Kind,
		ExternalRefURI:   nd.ExternalRef.URI,
		ExternalRefExtra: externalRefExtra,
		UploaderID:        nd.UploaderID,
		KnowledgeSourceID: nd.KnowledgeSourceID,
		ACLGroupIDs:       toInt64Array(nd.ACL.GroupIDs),
		ChunkCount:       chunkCount,
		ContentByteSize:  contentBytes,
		LastSyncedAt:     nil, // 由 sync runner 单独填,persister 不碰
	}
	return doc, nil
}

// buildChunkWithVecs 构造 repo 层需要的 ChunkWithVec 列表。
//
// 难点:ingestion.IngestedChunk.ParentIndex 指的是"同批次里 parent chunk 的 Index(0-based 序号)",
// 落库时 model.DocumentChunk.ParentChunkID 是"parent chunk 的真实 snowflake id"。
// 所以必须先为每个 chunk 分配 id,再建 index→id 映射回填 parent 列。
func buildChunkWithVecs(
	chunks []ingestion.IngestedChunk,
	vecs [][]float32,
	embedFailed bool,
	errSummary string,
	docID, orgID uint64,
) ([]docrepo.ChunkWithVec, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	// 一次性分配所有 id
	idsInt, err := idgen.GenerateIDs(len(chunks))
	if err != nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("generate chunk ids: %w", err)
	}
	ids := make([]uint64, len(idsInt))
	for i, x := range idsInt {
		ids[i] = uint64(x)
	}

	out := make([]docrepo.ChunkWithVec, 0, len(chunks))
	for i, c := range chunks {
		dbc := docmodel.DocumentChunk{
			ID:             ids[i],
			DocID:          docID,
			OrgID:          orgID,
			ChunkIdx:       c.Index,
			Content:        c.Content,
			ContentType:    nonEmpty(c.ContentType, "text"),
			Level:          c.Level,
			HeadingPath:    pq.StringArray(append([]string(nil), c.HeadingPath...)),
			TokenCount:     c.TokenCount,
			ChunkerVersion: c.ChunkerVersion,
		}
		if c.ParentIndex != nil {
			pi := *c.ParentIndex
			if pi >= 0 && pi < len(ids) {
				parentID := ids[pi]
				dbc.ParentChunkID = &parentID
			}
		}
		// metadata:ingestion chunk 的 Metadata 是 map[string]any,转成 jsonb
		if len(c.Metadata) > 0 {
			//sayso-lint:ignore err-shadow
			raw, err := json.Marshal(c.Metadata)
			if err != nil {
				//sayso-lint:ignore log-coverage
				return nil, fmt.Errorf("marshal chunk metadata idx=%d: %w", i, err)
			}
			dbc.Metadata = datatypes.JSON(raw)
		}

		cw := docrepo.ChunkWithVec{Chunk: dbc}
		if embedFailed {
			cw.Vec = nil
			cw.Chunk.IndexError = errSummary
			cw.Chunk.IndexStatus = doc.ChunkIndexStatusFailed
		} else if i < len(vecs) {
			cw.Vec = vecs[i]
		}
		out = append(out, cw)
	}
	return out, nil
}

// nonEmpty s 为空串时返 def。
func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// toInt64Array []uint64 → pq.Int64Array。nil 返空非 nil 数组,避免 NULL 写入 NOT NULL 列。
func toInt64Array(src []uint64) pq.Int64Array {
	out := make(pq.Int64Array, 0, len(src))
	for _, v := range src {
		out = append(out, int64(v))
	}
	return out
}

// truncateToBytes 按 rune 边界把 s 截到不超过 maxBytes。用于写 index_error varchar(255)。
func truncateToBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
