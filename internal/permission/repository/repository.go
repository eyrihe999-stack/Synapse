// repository.go 权限模块统一的 Repository 接口定义与事务封装。
//
// 设计要点:
//   - 所有 mutation 方法在内部以**同事务**写入 PermissionAuditLog 行(append-only)
//   - actor_user_id 从 ctx 读(middleware 已经把登录用户注入 ctx,见 logger.WithUserID),
//     无 user 上下文(系统/迁移路径)时记 0
//   - service 层完全不接触 audit 表 —— 只调 repo 的领域方法,审计自动跟随
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/permission/model"
	"gorm.io/gorm"
)

// Repository 权限模块的数据访问入口。
//
//sayso-lint:ignore interface-pollution
type Repository interface {
	// ─── 事务 ──────────────────────────────────────────────────────────────

	// WithTx 在事务内执行 fn,事务内所有 repo 调用共享同一个 tx。
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── Group ─────────────────────────────────────────────────────────────

	// CreateGroup 创建一条权限组记录。
	// (org_id, name) 唯一冲突由 uk_perm_groups_org_name 索引保证。
	// 同事务追加一条 group.create audit。
	CreateGroup(ctx context.Context, group *model.Group) error

	// FindGroupByID 按主键查找。不存在时返回 gorm.ErrRecordNotFound。
	FindGroupByID(ctx context.Context, id uint64) (*model.Group, error)

	// FindGroupByOrgAndName 在某 org 内按 name 查找。
	FindGroupByOrgAndName(ctx context.Context, orgID uint64, name string) (*model.Group, error)

	// ListGroupsByOrg 分页列出某 org 的所有组(按 name 字典序)。
	// 返回 (items, total)。
	ListGroupsByOrg(ctx context.Context, orgID uint64, page, size int) ([]*model.Group, int64, error)

	// ListGroupsByUser 列出某用户在某 org 中加入的所有组(JOIN perm_group_members)。
	// 不分页 —— 单用户加入的组天然量级小;前端不需要分页。
	ListGroupsByUser(ctx context.Context, orgID, userID uint64) ([]*model.Group, error)

	// UpdateGroupName 更新组名。同事务追加一条 group.rename audit(含 before/after)。
	UpdateGroupName(ctx context.Context, groupID uint64, newName string) error

	// DeleteGroup 删除组(同事务级联删除所有成员关系)。
	// 同事务追加一条 group.delete audit(after=null)。
	DeleteGroup(ctx context.Context, groupID uint64) error

	// CountGroupsByOrg 统计某 org 的组数(用于上限校验)。
	CountGroupsByOrg(ctx context.Context, orgID uint64) (int64, error)

	// ─── Group Member ──────────────────────────────────────────────────────

	// AddGroupMember 把 user 加入组。
	// (group_id, user_id) 重复由复合主键保证;返回 gorm 的 duplicate key 错误,service 层翻译。
	// 同事务追加一条 group.member_add audit。
	AddGroupMember(ctx context.Context, groupID, userID uint64) error

	// RemoveGroupMember 把 user 从组移除。
	// 不存在时返回 gorm.ErrRecordNotFound 由 service 层翻译;
	// 删除成功(rowsAffected>0)同事务追加一条 group.member_remove audit。
	RemoveGroupMember(ctx context.Context, groupID, userID uint64) error

	// IsGroupMember 判断 user 是否在组中。
	IsGroupMember(ctx context.Context, groupID, userID uint64) (bool, error)

	// CountGroupMembers 统计某组的成员数(用于上限校验)。
	CountGroupMembers(ctx context.Context, groupID uint64) (int64, error)

	// ListGroupMembers 分页列出某组的成员(按 joined_at 升序)。
	// 返回的 GroupMember 不带用户展示信息;handler/service 层若需要 email/display_name
	// 自行 JOIN user 模块或后续加 GroupMemberWithProfile 元组(M1 用不到先不加)。
	ListGroupMembers(ctx context.Context, groupID uint64, page, size int) ([]*model.GroupMember, int64, error)

	// ListGroupIDsByUser 列出某 user 在某 org 中加入的所有 group 的 id(只取 id,不取详情)。
	// PermContextMiddleware 用:每请求查一次塞 ctx,后续判定不重复打 DB。
	ListGroupIDsByUser(ctx context.Context, orgID, userID uint64) ([]uint64, error)

	// ─── Resource ACL ──────────────────────────────────────────────────────

	// GrantACL 给 (resource, subject) 添加一条 ACL 行,同事务写 audit。
	// 唯一约束冲突(已有同 (resource, subject) 行)由调用方先查重给出 ErrACLExists,DB 兜底。
	// auditAction / auditTarget 由调用方传入(各模块自定 action 名)。
	GrantACL(ctx context.Context, acl *model.ResourceACL, auditAction, auditTarget string) error

	// FindACLByID 按主键查找 ACL 行。
	FindACLByID(ctx context.Context, id uint64) (*model.ResourceACL, error)

	// FindACL 按 (resource, subject) 查找 ACL 行,不存在返回 gorm.ErrRecordNotFound。
	FindACL(ctx context.Context, resourceType string, resourceID uint64, subjectType string, subjectID uint64) (*model.ResourceACL, error)

	// ListACLByResource 列出某资源的所有 ACL(按 created_at ASC)。
	ListACLByResource(ctx context.Context, resourceType string, resourceID uint64) ([]*model.ResourceACL, error)

	// UpdateACLPermission 改 permission(read↔write),同事务写 audit。no-op 时不写 audit。
	UpdateACLPermission(ctx context.Context, aclID uint64, newPermission, auditAction, auditTarget string) error

	// RevokeACL 删除一条 ACL 行,同事务写 audit。
	RevokeACL(ctx context.Context, aclID uint64, auditAction, auditTarget string) error

	// BulkRevokeACLsByResource 批量删某资源的所有 ACL 行,同事务为每行写一条 revoke audit。
	// 上层资源(如 source)被删时连带清理挂在其上的 ACL 用。
	// 返回被删行数;无匹配返回 0、nil(幂等)。
	BulkRevokeACLsByResource(ctx context.Context, resourceType string, resourceID uint64, auditAction, auditTarget string) (int64, error)

	// ListVisibleResourceIDsBySubjects 给定 (org, resource_type, subjects) → 命中 ACL 的 resource_id 集合。
	// subjects 是 [(subject_type, subject_id), ...](caller 把"我所在 group + 我自己"摊开传)。
	// minPermission 过滤:'read' → 任何 ACL 都算;'write' → 只算 write 行。
	// 返回 distinct resource_id 列表。空 subjects → 返回空。
	ListVisibleResourceIDsBySubjects(ctx context.Context, orgID uint64, resourceType string, subjects []ACLSubject, minPermission string) ([]uint64, error)

	// ─── Audit Query ────────────────────────────────────────────────────────

	// ListAuditLogByOrg 列某 org 的审计日志。filter 控制多维过滤,keyset 分页(by id DESC)。
	// 返回 (rows, hasMore)。hasMore=true 表示还有更早的记录,前端拿 rows 最后一条 id 作为 BeforeID 翻页。
	ListAuditLogByOrg(ctx context.Context, orgID uint64, filter AuditFilter) ([]*model.PermissionAuditLog, bool, error)
}

// AuditFilter 审计查询过滤器。空字段表示不过滤;BeforeID=0 表示从头开始。
type AuditFilter struct {
	ActorUserID  uint64 // 0 = 不过滤
	TargetType   string // "" = 不过滤
	TargetID     uint64 // 0 = 不过滤(target_id 本身可能是 0,这里为空过滤的语义保留)
	Action       string // "" = 不过滤;精确匹配
	ActionPrefix string // "" = 不过滤;LIKE 'prefix%'(适合按 "member.*"/"role.*" 聚合查询)
	BeforeID     uint64 // 0 = 第一页;非 0 = id < BeforeID 的行
	Limit        int    // 0 = 默认 20,上限 100
}

// ACLSubject 是 ListVisibleResourceIDsBySubjects 的入参条目。
type ACLSubject struct {
	Type string // 'group' | 'user'
	ID   uint64
}

// gormRepository 基于 GORM 的统一实现。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务并在其中执行 fn。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	//sayso-lint:ignore log-coverage
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
