// Package repository document 模块数据访问层。
//
// 拆成两个独立接口:
//   - Repository      :MySQL 侧 documents 主表 CRUD。
//   - ChunkRepository :Postgres(pgvector)侧 document_chunks 切片/向量 CRUD + ANN 检索。
//
// 两个接口独立是因为底层是不同的 DB:未启用 pgvector 时 service 层只持有 Repository,
// ChunkRepository 为 nil,所有向量相关操作自然跳过,无需 stub 假实现。
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/document/model"
	"gorm.io/gorm"
)

// Repository document 模块 MySQL 数据访问入口。
//
// 一致性约定:
//   - List    :Count + Find 包在一个事务里,拿到一致快照,避免分页漂移;
//   - Update  :走 UpdateDocumentFieldsAtomic,tx + SELECT FOR UPDATE,消除 lost update;
//   - Delete  :走 DeleteDocumentAtomic,tx + SELECT FOR UPDATE,并返回已删除行供下游清理;
//   - Overwrite:走 OverwriteDocumentContent,tx + SELECT FOR UPDATE 包裹调用方的 OSS/PG 副作用 + metadata UPDATE;
//   - Create  :单条 INSERT 语句本身原子;ID 由 service 层用 snowflake 预分配。
//
// FindByContentHash / FindByFileName 是 Upload 查重/覆盖更新路径的 soft-lookup:找不到返回 (nil, nil),
// 不映射为 ErrDocumentNotFound —— 找不到在这两个方法里是"常规情况,不是异常"。
//
// DeleteDocumentByID 是 Upload 补偿路径的专用出口,跳过 org 校验,调用方必须自证"正在删自己刚插的行"。
type Repository interface {
	CreateDocument(ctx context.Context, doc *model.Document) error
	FindDocumentByID(ctx context.Context, orgID, docID uint64) (*model.Document, error)
	// FindDocumentsByIDs 批量按 (org_id, id) 取文档快照;缺失的 id 静默丢弃。
	// 供 SemanticSearch 用:向量库命中的 doc_id 可能对应 MySQL 已删的行(孤儿 chunk),
	// 这里返回的是 MySQL 认可的那部分,天然过滤孤儿。
	FindDocumentsByIDs(ctx context.Context, orgID uint64, ids []uint64) ([]*model.Document, error)
	FindByContentHash(ctx context.Context, orgID uint64, contentHash string) (*model.Document, error)
	// FindBySourceRef 按 (org_id, source_type, source_ref) 精确匹配找已存在的 doc。
	// 供 pull-based adapter(git / jira / feishu)的 ingestion 编排用:第二次看到同一 ref 时
	// 走 update(overwrite)而不是 create —— 否则每次 Sync 都会插一条新 doc。
	// 找不到返回 (nil, nil);source_type 空或 sourceRef 空返 (nil, nil)而不是 error(调用方直接走 create 路径)。
	FindBySourceRef(ctx context.Context, orgID uint64, sourceType string, sourceRef []byte) (*model.Document, error)
	// FindAllByFileName 返回同 org 下所有同名文档,按 updated_at DESC 排序(最新在前)。
	// 找不到返回空切片 + nil,不视作错误。用于 Precheck 给前端列出"同名候选"让用户选覆盖目标。
	FindAllByFileName(ctx context.Context, orgID uint64, fileName string) ([]*model.Document, error)
	ListDocumentsByOrg(ctx context.Context, orgID uint64, query string, page, size int) ([]*model.Document, int64, error)
	UpdateDocumentFieldsAtomic(ctx context.Context, orgID, docID uint64, buildUpdates func(current *model.Document) (map[string]any, error)) (*model.Document, error)
	// OverwriteDocumentContent 在 MySQL tx 内 SELECT FOR UPDATE 锁住目标行,
	// 调 sideEffects(持锁期间执行 OSS PUT + PG chunk swap 等跨库副作用);
	// sideEffects 返回 nil → 应用 newFields 并 COMMIT;返回 error → ROLLBACK metadata,副作用可能已部分应用。
	//
	// 重要:sideEffects 需设计为幂等,以便失败后重试能自愈(retry 同内容会走相同代码路径产生相同效果)。
	// 持锁时长 = OSS PUT + PG 操作 ≈ 0.5-2s,这期间并发 Update/Delete 同一行会阻塞。
	OverwriteDocumentContent(
		ctx context.Context,
		orgID, docID uint64,
		sideEffects func(current *model.Document) error,
		newFields map[string]any,
	) (*model.Document, error)
	DeleteDocumentAtomic(ctx context.Context, orgID, docID uint64) (*model.Document, error)
	DeleteDocumentByID(ctx context.Context, docID uint64) error
}

type gormRepository struct {
	db *gorm.DB
}

// New 构造 Repository。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}
