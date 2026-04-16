// repository.go 组织模块统一的 Repository 接口定义与事务封装。
//
// 设计说明:
//   - 整个模块共享一个 Repository 接口,方法按资源分组(org / member / role /
//     invitation / role_history),每组方法的实现放在同名文件里。
//   - WithTx 是事务入口:service 层通过 WithTx 在一个事务内跨多张表写入。
//   - 事务内返回的 tx-bound Repository 拥有和外层完全相同的方法集,方便
//     service 层无感知地切换事务/非事务调用。
//   - 读方法(Find / List / Count 等)同样可以在事务内走 tx,保证读自己写。
package repository

import (
	"context"

	"github.com/eyrihe999-stack/Synapse/internal/organization/model"
	"gorm.io/gorm"
)

// Repository 组织模块的数据访问入口。
//
// 方法按资源分组,跨资源的事务通过 WithTx 获得一个绑定到同一个 tx 的
// Repository 实例。
//
//sayso-lint:ignore interface-pollution
type Repository interface {
	// ─── 事务 ──────────────────────────────────────────────────────────────

	// WithTx 在事务内执行 fn,事务内所有 repo 调用共享同一个 tx。
	// fn 返回 nil 则提交,返回 error 则回滚。
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
	// CountOwnedOrgsByUser 统计某用户作为 owner 的 org 数量(含已解散需要传 false 排除)。
	CountOwnedOrgsByUser(ctx context.Context, userID uint64, includeDissolved bool) (int64, error)
	// ListOrgsByUser 列出某用户所属的所有 org(JOIN members),返回 org 列表和对应的 member 行。
	// 实现会一次性取回 org 和 member 方便 handler 返回 "org + 我的角色" 的视图。
	ListOrgsByUser(ctx context.Context, userID uint64) ([]OrgWithMember, error)

	// ─── Role ─────────────────────────────────────────────────────────────

	// CreateRole 创建一个角色(预设或自定义)。
	CreateRole(ctx context.Context, role *model.OrgRole) error
	// CreateRolesBatch 批量创建角色(org 创建时种预设用)。
	CreateRolesBatch(ctx context.Context, roles []*model.OrgRole) error
	// FindRoleByID 按 ID 查找。
	FindRoleByID(ctx context.Context, id uint64) (*model.OrgRole, error)
	// FindRoleByOrgName 按 (org_id, name) 查找。
	FindRoleByOrgName(ctx context.Context, orgID uint64, name string) (*model.OrgRole, error)
	// ListRolesByOrg 列出某 org 的所有角色。
	ListRolesByOrg(ctx context.Context, orgID uint64) ([]*model.OrgRole, error)
	// UpdateRoleFields 部分更新角色。
	UpdateRoleFields(ctx context.Context, id uint64, updates map[string]any) error
	// DeleteRole 删除角色(service 层需确保非预设且无成员引用)。
	DeleteRole(ctx context.Context, id uint64) error
	// CountMembersByRoleID 统计某角色有多少成员在用,用于删除前检查。
	CountMembersByRoleID(ctx context.Context, roleID uint64) (int64, error)

	// ─── Member ───────────────────────────────────────────────────────────

	// CreateMember 创建一条成员关系。
	CreateMember(ctx context.Context, member *model.OrgMember) error
	// FindMember 按 (org_id, user_id) 查找。
	FindMember(ctx context.Context, orgID, userID uint64) (*model.OrgMember, error)
	// FindMemberWithRole 查找成员并携带其角色(JOIN roles)。
	FindMemberWithRole(ctx context.Context, orgID, userID uint64) (*MemberWithRole, error)
	// ListMembersByOrg 分页列出 org 的所有成员(JOIN roles)。
	ListMembersByOrg(ctx context.Context, orgID uint64, page, size int) ([]*MemberWithRole, int64, error)
	// CountMembersByUser 统计某用户加入的 org 数。
	CountMembersByUser(ctx context.Context, userID uint64) (int64, error)
	// UpdateMemberRole 变更成员角色。
	UpdateMemberRole(ctx context.Context, orgID, userID, roleID uint64) error
	// DeleteMember 删除一条成员关系(踢出/退出)。
	DeleteMember(ctx context.Context, orgID, userID uint64) error
	// DeleteMembersByOrg 删除 org 下所有成员(解散时级联)。
	DeleteMembersByOrg(ctx context.Context, orgID uint64) error

	// ─── Invitation ───────────────────────────────────────────────────────

	// CreateInvitation 创建一条邀请。
	CreateInvitation(ctx context.Context, inv *model.OrgInvitation) error
	// FindInvitationByID 按 ID 查找。
	FindInvitationByID(ctx context.Context, id uint64) (*model.OrgInvitation, error)
	// FindPendingByOrgInvitee 按 (org_id, invitee_user_id, status=pending) 查找唯一记录。
	FindPendingByOrgInvitee(ctx context.Context, orgID, inviteeUserID uint64) (*model.OrgInvitation, error)
	// ListPendingByInvitee 列出某用户收到的 pending 邀请。
	ListPendingByInvitee(ctx context.Context, inviteeUserID uint64, page, size int) ([]*model.OrgInvitation, int64, error)
	// ListPendingByOrg 列出某 org 的 pending 邀请。
	ListPendingByOrg(ctx context.Context, orgID uint64, page, size int) ([]*model.OrgInvitation, int64, error)
	// UpdateInvitationStatus 更新邀请状态并记录 responded_at。
	UpdateInvitationStatus(ctx context.Context, id uint64, status string) error
	// ExpirePendingInvitations 将所有 expires_at < now 的 pending 邀请标记为 expired,返回受影响行数。
	ExpirePendingInvitations(ctx context.Context) (int64, error)

	// ─── Role History ────────────────────────────────────────────────────

	// AppendRoleHistory 追加一条角色变更历史。
	AppendRoleHistory(ctx context.Context, entry *model.OrgMemberRoleHistory) error
	// ListRoleHistoryByMember 列出某成员的角色变更历史(按时间倒序)。
	ListRoleHistoryByMember(ctx context.Context, orgID, userID uint64, limit int) ([]*model.OrgMemberRoleHistory, error)

	// ─── User Lookup(只读查询 users,供邀请候选人查找使用) ────────────

	// FindUserProfileByID 按 user_id 查找,不存在返回 nil。
	FindUserProfileByID(ctx context.Context, userID uint64) (*UserProfile, error)
	// FindUserProfileByEmail 按邮箱精确查找。
	FindUserProfileByEmail(ctx context.Context, email string) (*UserProfile, error)
	// SearchUserProfilesByDisplayName 按昵称精确查找(昵称不唯一,返回候选列表)。
	SearchUserProfilesByDisplayName(ctx context.Context, name string, limit int) ([]*UserProfile, error)
}

// OrgWithMember 是 ListOrgsByUser 的结果元组,把 org 和当前查询用户的 member 行
// 打包一起返回,避免 handler 侧再跑一次查询拿角色。
type OrgWithMember struct {
	Org    *model.Org
	Member *model.OrgMember
	Role   *model.OrgRole
}

// MemberWithRole 是 ListMembersByOrg 的结果元组,携带成员的角色展示信息。
type MemberWithRole struct {
	Member *model.OrgMember
	Role   *model.OrgRole
}

// gormRepository 基于 GORM 的统一实现。
// 所有 resource 分组的方法都定义在同包的其他文件里(org.go / member.go / ...)。
type gormRepository struct {
	db *gorm.DB
}

// New 构造一个 Repository 实例。
// 传入的 db 必须是业务连接池(已设置 logger / slow-query / pooling 等)。
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// WithTx 开启事务并在其中执行 fn。
// fn 接收的 tx Repository 走同一个 tx,对调用方来说看起来和普通 repo 一模一样。
func (r *gormRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	//sayso-lint:ignore log-coverage
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormRepository{db: tx})
	})
}
