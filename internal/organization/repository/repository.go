// repository.go 组织模块统一的 Repository 接口定义与事务封装。
package repository

import (
	"context"
	"time"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"gorm.io/gorm"
)

// Repository 组织模块的数据访问入口。
//
//sayso-lint:ignore interface-pollution
type Repository interface {
	// ─── 事务 ──────────────────────────────────────────────────────────────

	// WithTx 在事务内执行 fn,事务内所有 repo 调用共享同一个 tx。
	WithTx(ctx context.Context, fn func(tx Repository) error) error

	// ─── Org ──────────────────────────────────────────────────────────────

	// CreateOrg 创建一条 org 记录。
	CreateOrg(ctx context.Context, org *model.Org) error
	// FindOrgByID 按 ID 查找 org,不存在时返回 gorm.ErrRecordNotFound。
	FindOrgByID(ctx context.Context, id uint64) (*model.Org, error)
	// FindOrgBySlug 按 slug 查找 org。
	FindOrgBySlug(ctx context.Context, slug string) (*model.Org, error)
	// UpdateOrgFields 部分更新 org 字段(只更新 updates map 里的列)。
	UpdateOrgFields(ctx context.Context, id uint64, updates map[string]any) error
	// CountOwnedOrgsByUser 统计某用户作为 owner 的 org 数量。
	CountOwnedOrgsByUser(ctx context.Context, userID uint64, includeDissolved bool) (int64, error)
	// ListActiveOrgsOwnedBy 列出某用户作为 owner 的所有 active org(按 slug 字典序)。
	ListActiveOrgsOwnedBy(ctx context.Context, userID uint64) ([]*model.Org, error)
	// ListOrgsByUser 列出某用户所属的所有 active org(JOIN members)。
	ListOrgsByUser(ctx context.Context, userID uint64) ([]OrgWithMember, error)

	// ─── Role ─────────────────────────────────────────────────────────────

	// CreateRole 创建一条角色记录(系统 seed 或自定义角色均经此方法)。
	CreateRole(ctx context.Context, role *model.OrgRole) error
	// FindRoleByID 按主键查找角色,不存在返回 gorm.ErrRecordNotFound。
	FindRoleByID(ctx context.Context, id uint64) (*model.OrgRole, error)
	// FindRoleByOrgAndSlug 在某 org 内按 slug 查找角色。
	FindRoleByOrgAndSlug(ctx context.Context, orgID uint64, slug string) (*model.OrgRole, error)
	// ListRolesByOrg 列出某 org 的所有角色,系统角色在前(owner→admin→member),自定义角色按 slug 字典序。
	ListRolesByOrg(ctx context.Context, orgID uint64) ([]*model.OrgRole, error)
	// UpdateRoleDisplayName 只更新 display_name 字段。
	UpdateRoleDisplayName(ctx context.Context, id uint64, displayName string) error
	// UpdateRolePermissions 替换 permissions 字段为给定集合。同事务写 audit。
	UpdateRolePermissions(ctx context.Context, id uint64, perms []string) error
	// DeleteRole 硬删除一条角色。调用方须先保证无成员挂该角色。
	DeleteRole(ctx context.Context, id uint64) error
	// CountCustomRolesByOrg 统计某 org 的自定义角色数(is_system=false)。
	CountCustomRolesByOrg(ctx context.Context, orgID uint64) (int64, error)
	// CountMembersByRole 统计某 role 下的成员数,删除前用来判断是否有成员占用。
	CountMembersByRole(ctx context.Context, roleID uint64) (int64, error)
	// SeedSystemRolesForOrg 给一个 org 幂等插入三条系统角色,返回 slug→OrgRole 的映射。
	// CreateOrg 事务内调用;migration 路径有自己的 batch 版本不复用这个。
	SeedSystemRolesForOrg(ctx context.Context, orgID uint64) (map[string]*model.OrgRole, error)

	// ─── Member ───────────────────────────────────────────────────────────

	// CreateMember 创建一条成员关系。
	CreateMember(ctx context.Context, member *model.OrgMember) error
	// FindMember 按 (org_id, user_id) 查找。
	FindMember(ctx context.Context, orgID, userID uint64) (*model.OrgMember, error)
	// ListMembersByOrg 分页列出 org 的所有成员,JOIN users + org_roles 回填展示字段。
	// users 记录缺失时字段为空串;role 记录缺失时(数据回填遗漏等极端情况)也为空串,service 层不过滤。
	ListMembersByOrg(ctx context.Context, orgID uint64, page, size int) ([]*MemberWithProfile, int64, error)
	// CountMembersByOrg 统计某 org 的成员数。
	CountMembersByOrg(ctx context.Context, orgID uint64) (int64, error)
	// CountMembersByUser 统计某用户加入的 active org 数。
	CountMembersByUser(ctx context.Context, userID uint64) (int64, error)
	// UpdateMemberRole 更新 (org_id, user_id) 成员的 role_id。
	UpdateMemberRole(ctx context.Context, orgID, userID, roleID uint64) error
	// DeleteMember 删除一条成员关系(踢出/退出)。
	DeleteMember(ctx context.Context, orgID, userID uint64) error
	// DeleteMembersByOrg 删除 org 下所有成员(解散时级联)。
	DeleteMembersByOrg(ctx context.Context, orgID uint64) error

	// ─── Invitation ────────────────────────────────────────────────────────

	// CreateInvitation 创建一条邀请记录。
	CreateInvitation(ctx context.Context, inv *model.OrgInvitation) error
	// FindInvitationByID 按主键查找邀请。不存在时返回 gorm.ErrRecordNotFound。
	FindInvitationByID(ctx context.Context, id uint64) (*model.OrgInvitation, error)
	// FindInvitationByTokenHash 按 token_hash 查找邀请。
	FindInvitationByTokenHash(ctx context.Context, tokenHash string) (*model.OrgInvitation, error)
	// FindPendingInvitation 查找 (org_id, email) 下 status=pending 的邀请(最多一条)。
	// 不存在返回 gorm.ErrRecordNotFound。
	FindPendingInvitation(ctx context.Context, orgID uint64, email string) (*model.OrgInvitation, error)
	// ListInvitationsByOrg 列出某 org 下所有邀请(默认按 created_at DESC)。
	// statusFilter 为空时返回全部状态;非空只返回指定状态。
	ListInvitationsByOrg(ctx context.Context, orgID uint64, statusFilter string, page, size int) ([]*InvitationWithRole, int64, error)
	// UpdateInvitationFields 部分更新邀请字段(status / token_hash / expires_at / accepted_at / accepted_user_id)。
	UpdateInvitationFields(ctx context.Context, id uint64, updates map[string]any) error

	// IsEmailMemberOfOrg 检查某 email 是否已是 org 的成员。
	// email 大小写不敏感比较(JOIN users 表按 LOWER 比较)。
	IsEmailMemberOfOrg(ctx context.Context, orgID uint64, email string) (bool, error)

	// ListInvitationsByEmail 列出某 email 收到的邀请(被邀请人收件箱视图)。
	// email 大小写不敏感比较。statusFilter 为空时返所有状态;非空只返指定状态。
	// JOIN orgs + org_roles 一次性回填展示字段,省去 service 再 foreach 查。
	ListInvitationsByEmail(ctx context.Context, email, statusFilter string) ([]*MyInvitationRow, error)

	// ListInvitationsByInviter 列出某用户作为 inviter 发出的邀请(发件箱视图)。
	// 跨 org 聚合,只返 orgs.status='active' 的行;statusFilter 语义同上。
	ListInvitationsByInviter(ctx context.Context, inviterUserID uint64, statusFilter string) ([]*SentInvitationRow, error)

	// SearchInviteCandidates 按 (type, query) 在 users 表中搜候选邀请对象。
	// type:
	//   - "email"   → LOWER(email) = LOWER(?)，精确,limit 1
	//   - "user_id" → id = ?，精确,limit 1
	//   - "name"    → display_name LIKE '%q%'，模糊
	// 只返 users.status=active(=1) 的行。
	// 每条带 IsMember / HasPendingInvite 标记(LEFT JOIN + EXISTS 子查询)。
	SearchInviteCandidates(ctx context.Context, orgID uint64, searchType, query string, limit int) ([]*InviteCandidate, error)
}

// OrgWithMember 是 ListOrgsByUser 的结果元组。
type OrgWithMember struct {
	Org    *model.Org
	Member *model.OrgMember
}

// MemberWithProfile 是 ListMembersByOrg 的结果元组,JOIN users + org_roles 把展示字段带回。
// Email / DisplayName / AvatarURL / Status / EmailVerifiedAt / LastLoginAt / PrincipalID
// 来自 users 表,Role* 来自 org_roles;任一 JOIN 记录缺失时对应字段为空/零值/nil。
//
// PrincipalID 是前端 @mention 定位 channel_members 用的;org_members 本身只有
// user_id,但 channel_members / task 等按 principal_id 存,前端需要该映射把"用户"
// 匹配到 channel 里的身份根。
type MemberWithProfile struct {
	Member          *model.OrgMember
	PrincipalID     uint64
	Email           string
	DisplayName     string
	AvatarURL       string
	Status          int32
	EmailVerifiedAt *time.Time
	LastLoginAt     *time.Time
	RoleSlug        string
	RoleDisplayName string
	RoleIsSystem    bool
}

// InvitationWithRole 是 ListInvitationsByOrg 的结果元组,JOIN org_roles 带回角色展示字段。
// JOIN 失败(角色被删等极端情况)时 Role* 为零值。
type InvitationWithRole struct {
	Invitation      *model.OrgInvitation
	RoleSlug        string
	RoleDisplayName string
	RoleIsSystem    bool
}

// InviteCandidate SearchInviteCandidates 的单条返回。
// IsMember / HasPendingInvite 由 SQL 直接打标,前端据此灰掉不可点的条目。
type InviteCandidate struct {
	UserID           uint64
	Email            string
	DisplayName      string
	AvatarURL        string
	IsMember         bool
	HasPendingInvite bool
}

// MyInvitationRow ListInvitationsByEmail 的结果元组。
// invitation 本体 + JOIN orgs 带回的 org 展示字段 + JOIN org_roles 带回的角色字段。
// 角色 JOIN 失败(role 被删等极端)时 Role* 为零值。
type MyInvitationRow struct {
	Invitation      *model.OrgInvitation
	OrgSlug         string
	OrgDisplayName  string
	RoleSlug        string
	RoleDisplayName string
	RoleIsSystem    bool
}

// SentInvitationRow ListInvitationsByInviter 的结果元组。
// 形状和 MyInvitationRow 一致,语义不同 —— 这里看的是"我发出的",email 字段需带给前端展示。
type SentInvitationRow struct {
	Invitation      *model.OrgInvitation
	OrgSlug         string
	OrgDisplayName  string
	RoleSlug        string
	RoleDisplayName string
	RoleIsSystem    bool
}

// 搜索类型枚举,供 repository 和 service 共用。
const (
	InviteSearchTypeEmail  = "email"
	InviteSearchTypeUserID = "user_id"
	InviteSearchTypeName   = "name"
)

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
