// version.go document_versions 表的 gorm 映射。
//
// 每次 upload(新文件 / 覆盖)成功写 OSS 后插一行,记录"文档 X 的第 N 版本在 OSS 的哪个 key"。
// handler 依赖它做版本裁剪(MaxVersionsPerDocument)+ 未来的版本历史 / 回滚 UI。
package model

import (
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/document"
)

// DocumentVersion 单次版本记录。一条 documents 行可对应多条 document_versions 行(历史版本)。
//
// 不变量:
//
//	doc_id → documents.id,ON DELETE CASCADE(doc 消失 → 版本记录一并清;OSS 对象由 handler 侧显式删)
//	version_hash = documents.version 同步(sha256 hex),便于按 hash 反查
type DocumentVersion struct {
	ID          uint64    `gorm:"column:id;primaryKey"`
	DocID       uint64    `gorm:"column:doc_id;not null"`
	OrgID       uint64    `gorm:"column:org_id;not null"`
	OSSKey      string    `gorm:"column:oss_key;type:text;not null"`
	VersionHash string    `gorm:"column:version_hash;size:128;not null"`
	FileSize    int       `gorm:"column:file_size;not null;default:0"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName 固定表名。
func (DocumentVersion) TableName() string { return document.TableDocumentVersions }
