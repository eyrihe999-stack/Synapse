package model

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/lib/pq"
	"gorm.io/datatypes"
)

// DocumentChunk 一个 chunk 行,带 optional 向量。
//
// 维度约束:embedding 列类型在 migration 期由 cfg.ModelDim 决定,不在 struct 层校验。
//
// 向量编解码:
//
//   - 写入:repository 层把 []float32 格式化成 pgvector 字面量字符串 "[v1,v2,...]",
//     用 Raw SQL 带 ::vector cast 写入。struct 里 Embedding 字段 *不*用于写
//     (gorm INSERT 会把它当 text 写,pgvector 也接受 "[...]"→vector 的隐式转换,
//     但显式 cast 更稳)。
//   - 读取:Layer 1 的 repo 方法目前**不 SELECT embedding 列**(retrieval 模块再加),
//     所以此字段留零值即可。Embedding 指针允许 NULL(embedErr 非致命时的 failed 行)。
//
// 不变量(migration 兜住):
//
//   - (doc_id, chunk_idx) 唯一
//   - doc_id FK → documents(id) ON DELETE CASCADE
type DocumentChunk struct {
	ID            uint64 `gorm:"column:id;primaryKey"`
	DocID         uint64 `gorm:"column:doc_id;not null"`
	OrgID         uint64 `gorm:"column:org_id;not null"` // 冗余,方便按 org 过滤
	ChunkIdx      int    `gorm:"column:chunk_idx;not null"`
	Content       string `gorm:"column:content;type:text;not null"`
	ContentType   string `gorm:"column:content_type;size:16;not null;default:'text'"`
	Level         int16  `gorm:"column:level;not null;default:0"`

	// HeadingPath markdown 层级路径,pq.StringArray 映射 text[]。
	HeadingPath pq.StringArray `gorm:"column:heading_path;type:text[];not null;default:'{}'"`

	TokenCount int `gorm:"column:token_count;not null;default:0"`

	// Embedding 真实的向量列由 repository 层用 Raw SQL 写入(需要 ::vector cast),
	// struct 层打 `-` 让 gorm 完全忽略此字段,避免 SELECT 时撞上 text ↔ vector 类型错配。
	// retrieval 模块后续要读向量时,**也**走自己的 Raw SQL(返回 text 再解析)。
	Embedding *string `gorm:"-"`

	ChunkerVersion string `gorm:"column:chunker_version;size:32;not null;default:''"`
	ParentChunkID  *uint64 `gorm:"column:parent_chunk_id"`

	IndexStatus string `gorm:"column:index_status;size:16;not null"`
	IndexError  string `gorm:"column:index_error;size:255;not null;default:''"`

	Metadata datatypes.JSON `gorm:"column:metadata;type:jsonb"`

	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName 固定表名。
func (DocumentChunk) TableName() string { return document.TableDocumentChunks }
