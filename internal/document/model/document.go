// Package model document 模块 gorm 映射。
//
// 表 DDL 不走 AutoMigrate —— pgvector 的 `vector` 列类型 gorm 不识别。
// 所有 CREATE TABLE 都由 internal/document/migration.go 用原生 SQL 下发。
// 这里的 struct 只作 ORM 读写映射;TableName() 决定映射到哪张表。
package model

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/document"
	"github.com/lib/pq"
	"gorm.io/datatypes"
)

// Document 单篇文档的元数据行,和 ingestion.NormalizedDoc 一一对应。
//
// 不变量(migration 的 UNIQUE 兜住):
//
//	(org_id, source_type, source_id) 唯一 → 源端幂等键
type Document struct {
	ID          uint64 `gorm:"column:id;primaryKey"`
	OrgID       uint64 `gorm:"column:org_id;not null"`
	SourceType  string `gorm:"column:source_type;size:32;not null"`
	Provider    string `gorm:"column:provider;size:32;not null"`
	SourceID    string `gorm:"column:source_id;size:255;not null"`
	Title       string `gorm:"column:title;size:512;not null;default:''"`
	MIMEType    string `gorm:"column:mime_type;size:64;not null;default:''"`
	FileName    string `gorm:"column:file_name;size:512;not null;default:''"`
	Version     string `gorm:"column:version;size:128;not null"`

	// OSSKey 最新版本在 OSS 上的 object key(synapse/{orgID}/{docID}/{versionHash}.ext)。
	// 空串表示未上 OSS(理论上新流程所有 upload 都会有值;历史数据留空兼容)。
	// 完整历史版本走 document_versions 表,这里只冗余一份"最新"方便读。
	OSSKey string `gorm:"column:oss_key;type:text;not null;default:''"`

	// ExternalRef* 回源锚点,对应 NormalizedDoc.ExternalRef。Extra 放不固定字段(jsonb)。
	ExternalRefKind  string         `gorm:"column:external_ref_kind;size:32;not null;default:''"`
	ExternalRefURI   string         `gorm:"column:external_ref_uri;type:text;not null;default:''"`
	ExternalRefExtra datatypes.JSON `gorm:"column:external_ref_extra;type:jsonb"`

	UploaderID uint64 `gorm:"column:uploader_id;not null"`

	// KnowledgeSourceID 指向 source 模块的 sources.id —— 权限模型里 doc 所属的"知识源"。
	// 命名带 knowledge_ 前缀仅为消歧(避免与上面的 SourceID string"外部 source 标识符"混淆)。
	// 0 = 未关联(M2 backfill 之前的历史 doc;迁移完成后所有 doc 都 > 0)。
	// M3 引入 ACL 表后,doc 列表/检索按本字段 JOIN sources 做权限过滤。
	KnowledgeSourceID uint64 `gorm:"column:knowledge_source_id;not null;default:0"`

	// ACLGroupIDs M4.2 预留,当前永远为空数组。
	// 用 lib/pq.Int64Array:实现 database/sql 的 Scanner/Valuer,可直接和 PG bigint[] 对应。
	ACLGroupIDs pq.Int64Array `gorm:"column:acl_group_ids;type:bigint[];not null;default:'{}'"`

	ChunkCount      int `gorm:"column:chunk_count;not null;default:0"`
	ContentByteSize int `gorm:"column:content_byte_size;not null;default:0"`

	LastSyncedAt *time.Time `gorm:"column:last_synced_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName 固定表名,避免 gorm 复数化奇观。
func (Document) TableName() string { return document.TableDocuments }
