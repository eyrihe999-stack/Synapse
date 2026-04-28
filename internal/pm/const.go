// Package pm 项目管理模块。承载"项目的形状":Project / Initiative / Version /
// Workstream / ProjectKBRef 五张表的关系层 + CRUD。
//
// 设计依据见 docs/collaboration-design.md(PR-A 新增章节)。
//
// 与 channel 模块的关系:channel 是协作载体(消息、共享文档、reaction、attachment),
// pm 是项目管理结构层。Project / Version 从 channel 物理迁移到 pm,channel 通过
// workstream_id 反向引用 pm 实体。依赖方向 channel ← pm(pm 调 channel.CreateChannel
// 创建 workstream channel),不循环。
package pm

import "time"

// ─── Initiative 状态:一个 initiative 是一个长期主题(为什么做) ────────────────

const (
	InitiativeStatusPlanned   = "planned"
	InitiativeStatusActive    = "active"
	InitiativeStatusCompleted = "completed"
	InitiativeStatusCancelled = "cancelled"
)

// IsValidInitiativeStatus 校验 initiative.status 是否合法。
func IsValidInitiativeStatus(s string) bool {
	return s == InitiativeStatusPlanned || s == InitiativeStatusActive ||
		s == InitiativeStatusCompleted || s == InitiativeStatusCancelled
}

// ─── Version 状态:一个 version 是一个发版窗口(什么时候交付) ────────────────
//
// 命名相对 channel 模块老定义有调整(对齐 initiative / workstream):
//
//	old "planned"     → new "planning"
//	old "in_progress" → new "active"
//	"released"        保持
//	"cancelled"       保持
//
// 数据迁移时由 pm/migration.go 里的 ensureVersionStatusBackfill 一次性 UPDATE。
const (
	VersionStatusPlanning  = "planning"
	VersionStatusActive    = "active"
	VersionStatusReleased  = "released"
	VersionStatusCancelled = "cancelled"
)

// IsValidVersionStatus 校验 version.status 是否合法。
func IsValidVersionStatus(s string) bool {
	return s == VersionStatusPlanning || s == VersionStatusActive ||
		s == VersionStatusReleased || s == VersionStatusCancelled
}

// ─── Workstream 状态:workstream 是 initiative 下的工作切片(怎么做) ────────

const (
	WorkstreamStatusDraft     = "draft"
	WorkstreamStatusActive    = "active"
	WorkstreamStatusBlocked   = "blocked"
	WorkstreamStatusDone      = "done"
	WorkstreamStatusCancelled = "cancelled"
)

// IsValidWorkstreamStatus 校验 workstream.status 是否合法。
func IsValidWorkstreamStatus(s string) bool {
	return s == WorkstreamStatusDraft || s == WorkstreamStatusActive ||
		s == WorkstreamStatusBlocked || s == WorkstreamStatusDone ||
		s == WorkstreamStatusCancelled
}

// ─── 字段长度限制,集中声明,model tag 和 service 校验共用 ─────────────────

const (
	ProjectNameMaxLen        = 128
	ProjectDescriptionMaxLen = 512

	InitiativeNameMaxLen        = 128
	InitiativeDescriptionMaxLen = 1024
	InitiativeOutcomeMaxLen     = 4096

	VersionNameMaxLen = 64

	WorkstreamNameMaxLen        = 128
	WorkstreamDescriptionMaxLen = 4096
)

// ─── 列表默认分页(对齐 channel 模块) ───────────────────────────────────────

const (
	ListDefaultLimit = 50
	ListMaxLimit     = 200
)

// ─── Default seed 命名 ─────────────────────────────────────────────────────
//
// 每个 project(包括迁移期回填的存量 project)都会自动获得一份:
//   - 1 个 Default Initiative(is_system=true)
//   - 1 个 Backlog Version(is_system=true)
//   - 1 个 Project Console channel(kind=project_console,channel 模块管)
//
// 这三件 seed 资源不可删 / 改名 / 直接 archive,由 service 层守护。
const (
	DefaultInitiativeName        = "Default"
	DefaultInitiativeDescription = "Auto-created default initiative — groups workstreams that haven't been split into themed initiatives yet."
	BacklogVersionName           = "Backlog"
)

// MinInitiativeArchiveGrace 归档后清理宽限时长。占位,Phase 2 用。
const MinInitiativeArchiveGrace = 7 * 24 * time.Hour
