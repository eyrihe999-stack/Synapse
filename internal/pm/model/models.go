// Package model pm 模块数据模型 —— Project / Initiative / Version / Workstream /
// ProjectKBRef 五张表。
//
// 设计依据:docs/collaboration-design.md PR-A 章节(Initiative + Version 平行结构)。
// ID 类型统一 BIGINT UNSIGNED(对齐 users / orgs / principals 的 autoIncrement)。
//
// 当前文件包含 3 张全新表的 model(T2 范围):
//   - Initiative   主题轴(为什么做)
//   - Workstream   工作切片(怎么做)
//   - ProjectKBRef 项目级 KB 挂载
//
// Project / Version 在 T3 / T4 任务里从 channel/model 物理迁过来后追加到本文件。
package model

import "time"

// Initiative 项目下的"长期主题",回答"为什么做"。
//
// 和 Version 是正交的两个维度:Initiative 是主题分组(可跨多个 version 持续推进),
// Version 是发版窗口(包含来自不同 initiative 的产出)。两者交点是 Workstream。
//
// 字段:
//   - ProjectID:所属 project;权限、审计边界
//   - Name:initiative 名,一个 project 内活跃 initiative(archived_at IS NULL)唯一
//     —— 通过 name_active 生成列 + (project_id, name_active) 唯一索引实现
//   - TargetOutcome:目标产出 / 成功标准,自由文本(给人和 agent 看,LLM 拆解任务参考)
//   - Status:planned / active / completed / cancelled,见 const.go 枚举
//   - IsSystem:true 表示系统自动生成的 default initiative,不允许用户改名/删/archive;
//     由 service 层守护
//   - CreatedBy:创建者 principal_id;FK principals.id
//   - ArchivedAt:非空表示已归档;通过 name_active 生成列实现"归档后释放名字"
//     (见 migration.go 的 ensureInitiativeNameActiveColumn)
//
// 索引:
//   - idx_initiatives_project_status:列 project 下所有 active initiative
//   - uk_initiatives_project_name_active(project_id, name_active):活跃 name 唯一
//     (在 migration 里用 raw DDL 建,因为依赖生成列)
type Initiative struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID     uint64     `gorm:"not null;index:idx_initiatives_project_status,priority:1" json:"project_id"`
	Name          string     `gorm:"size:128;not null" json:"name"`
	Description   string     `gorm:"size:1024" json:"description,omitempty"`
	TargetOutcome string     `gorm:"type:text" json:"target_outcome,omitempty"`
	Status        string     `gorm:"size:16;not null;index:idx_initiatives_project_status,priority:2" json:"status"`
	IsSystem      bool       `gorm:"column:is_system;not null;default:false" json:"is_system,omitempty"`
	CreatedBy     uint64     `gorm:"not null" json:"created_by"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"not null" json:"updated_at"`
	ArchivedAt    *time.Time `gorm:"index:idx_initiatives_archived" json:"archived_at,omitempty"`
}

// TableName 固定表名。
func (Initiative) TableName() string { return "initiatives" }

// Workstream 工作切片,initiative 下的具体执行单元(比 task 大、比 initiative 小)。
//
// 工作切片的颗粒约束:**一个 workstream 应能在一个 version 内交付完**。塞不进就拆。
// 这条约束在 service / agent 拆解逻辑里反复用,model 层只暴露字段,不强制。
//
// 字段:
//   - InitiativeID:必属于一个 initiative —— 工作有意图主体
//   - VersionID:可空 = backlog(已规划归属主题但尚未排期到具体版本)
//   - ProjectID:冗余字段,加速"by-project 查所有 workstream"避免 join initiative
//   - ChannelID:lazy-create —— workstream 协作 channel,可空(只在真有协作需要时建,
//     建好后回填这里);channel 反向也有 workstream_id 字段
//   - Status:见 const.go 枚举(draft / active / blocked / done / cancelled)
//
// 索引:
//   - idx_workstreams_initiative_status:列 initiative 下的活跃 workstream
//   - idx_workstreams_version_status:列 version 下交付物
//   - idx_workstreams_project_status:project 全局视图(roadmap)
//   - idx_workstreams_channel:channel 反查 workstream(workstream_id 落 channel 表
//     之外另一条路径)
type Workstream struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	InitiativeID  uint64     `gorm:"not null;index:idx_workstreams_initiative_status,priority:1" json:"initiative_id"`
	VersionID     *uint64    `gorm:"index:idx_workstreams_version_status,priority:1" json:"version_id,omitempty"`
	ProjectID     uint64     `gorm:"not null;index:idx_workstreams_project_status,priority:1" json:"project_id"`
	Name          string     `gorm:"size:128;not null" json:"name"`
	Description   string     `gorm:"type:text" json:"description,omitempty"`
	Status        string     `gorm:"size:16;not null;index:idx_workstreams_initiative_status,priority:2;index:idx_workstreams_version_status,priority:2;index:idx_workstreams_project_status,priority:2" json:"status"`
	ChannelID     *uint64    `gorm:"index:idx_workstreams_channel" json:"channel_id,omitempty"`
	CreatedBy     uint64     `gorm:"not null" json:"created_by"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"not null" json:"updated_at"`
	ArchivedAt    *time.Time `gorm:"index:idx_workstreams_archived" json:"archived_at,omitempty"`
}

// TableName 固定表名。
func (Workstream) TableName() string { return "workstreams" }

// ProjectKBRef 项目级 KB 挂载。代替原 channel_kb_refs(channel 维度太细),所有 KB
// 挂载统一上升到 project 维度 —— project 内任何成员通过任何 channel 都能访问。
//
// 粒度:允许挂整个 knowledge_sources 一行(source 级整批)或单独挂 documents 一行
// (文档级精细);二选一靠应用层 + UNIQUE 约束保证。
//
// 字段:
//   - KBSourceID:挂整个 source(0 = 不挂);和 KBDocumentID 二选一
//   - KBDocumentID:挂单文档(0 = 不挂);和 KBSourceID 二选一
//   - AttachedBy:挂载操作人 principal_id(审计用)
//
// 索引:
//   - idx_project_kb_refs_project:列 project 下所有挂的 KB
//   - uk_project_kb_refs_uniq(project_id, kb_source_id, kb_document_id):防重复挂
//     (二选一时另一个为 0,组合仍能去重)
type ProjectKBRef struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID    uint64    `gorm:"not null;index:idx_project_kb_refs_project;uniqueIndex:uk_project_kb_refs_uniq,priority:1" json:"project_id"`
	KBSourceID   uint64    `gorm:"column:kb_source_id;not null;default:0;uniqueIndex:uk_project_kb_refs_uniq,priority:2" json:"kb_source_id,omitempty"`
	KBDocumentID uint64    `gorm:"column:kb_document_id;not null;default:0;uniqueIndex:uk_project_kb_refs_uniq,priority:3" json:"kb_document_id,omitempty"`
	AttachedBy   uint64    `gorm:"not null" json:"attached_by"`
	AttachedAt   time.Time `gorm:"not null" json:"attached_at"`
}

// TableName 固定表名。
func (ProjectKBRef) TableName() string { return "project_kb_refs" }

// ─── 从 channel 模块物理迁过来的 Project / Version(T3 任务) ───────────────────
//
// schema 字段保持兼容(继续用同名 `projects` / `versions` 表),Version 顺带扩展
// ReleasedAt / IsSystem / CreatedBy / UpdatedAt 几个新字段(T4 任务范围),
// 数据迁移由 pm/migration.go 的 ensureVersionStatusBackfill 等步骤负责。

// Project 项目:org 下的产品 / 项目单元。
//
// 字段:
//   - OrgID:所属 org;权限、审计边界
//   - Name:项目名,一个 org 内活跃项目(archived_at IS NULL)唯一
//   - CreatedBy:创建者 user_id(注意:历史数据是 user_id 不是 principal_id;
//     新建数据为对齐 channel/task 等模块也保持 user_id 语义)
//   - ArchivedAt:非空表示已归档;通过 name_active 生成列实现"归档后释放名字"
//     (见 pm/migration.go 的 ensureProjectNameActiveColumn,代码从 channel 迁来)
//
// 索引:
//   - idx_projects_org:org 下列表
//   - uk_projects_org_name_active(org_id, name_active):活跃项目名唯一
//     (在 migration 里用 raw DDL 建,因为依赖生成列)
type Project struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrgID       uint64     `gorm:"not null;index:idx_projects_org" json:"org_id"`
	Name        string     `gorm:"size:128;not null" json:"name"`
	Description string     `gorm:"size:512" json:"description,omitempty"`
	CreatedBy   uint64     `gorm:"not null" json:"created_by"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null" json:"updated_at"`
	ArchivedAt  *time.Time `gorm:"index:idx_projects_archived" json:"archived_at,omitempty"`
}

// TableName 固定表名。
func (Project) TableName() string { return "projects" }

// Version 项目下的发版窗口。回答"什么时候交付"。
//
// 和 Initiative 是正交的两个维度:Initiative 是主题分组,Version 是时间窗口。
// Workstream 是两者的交点(workstream.initiative_id NOT NULL,workstream.version_id
// NULLABLE = backlog)。
//
// 字段(从 channel 老 model 兼容继承,加新字段):
//   - ProjectID:所属 project
//   - Name:版本名(v1.0、2026 Q3、Backlog 等);(project_id, name) UNIQUE
//   - Status:planning / active / released / cancelled —— 命名相对 channel 老
//     枚举调整,数据迁移走 ensureVersionStatusBackfill
//   - TargetDate:计划发布日(可空);UI 排序、roadmap 视图按这个
//   - ReleasedAt:实际发布时间(可空);填了表示"该 version 已经发出去"
//   - IsSystem:Backlog version 的标志(每 project 自动建一个 IsSystem=true
//     的 Backlog),不允许删 / 改名
//   - CreatedBy:创建者 user_id;0 表示系统自动建(seed 阶段无 actor)
//
// 索引:
//   - uk_versions_project_name(project_id, name) UNIQUE:同 project 下版本名唯一
//   - idx_versions_project_status:project 下按状态过滤(roadmap UI 用)
//   - idx_versions_target_date:按计划发布日排序
type Version struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectID  uint64     `gorm:"not null;uniqueIndex:uk_versions_project_name,priority:1;index:idx_versions_project_status,priority:1" json:"project_id"`
	Name       string     `gorm:"size:64;not null;uniqueIndex:uk_versions_project_name,priority:2" json:"name"`
	Status     string     `gorm:"size:16;not null;index:idx_versions_project_status,priority:2" json:"status"`
	TargetDate *time.Time `gorm:"index:idx_versions_target_date" json:"target_date,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	IsSystem   bool       `gorm:"column:is_system;not null;default:false" json:"is_system,omitempty"`
	CreatedBy  uint64     `gorm:"not null;default:0" json:"created_by,omitempty"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
	// UpdatedAt 老 versions 表没这个字段;ALTER 加 NULLABLE,GORM autoUpdateTime
	// 在 Create / Update 时自动填,老行保持 NULL 直到第一次被改。这样避免
	// MySQL 严格模式下"datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP"
	// 精度不匹配的 1067 错误。
	UpdatedAt  *time.Time `gorm:"autoUpdateTime" json:"updated_at,omitempty"`
}

// TableName 固定表名。
func (Version) TableName() string { return "versions" }
