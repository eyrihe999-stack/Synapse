// Package model agents 模块数据模型 —— agents 表(原 agent_registry)。
//
// 表承载"agent 身份 + 凭证 + 管理元信息"。一条记录 = 一个可接入 Synapse 的 agent。
//
// PR #1 (collaboration-design §3.5) 起 agents 表作为 principal 的子表:
//   - 表从 agent_registry 重命名为 agents
//   - 增加 principal_id 列,关联到 principals 表的身份根
//
// 索引策略:
//   - agent_id unique:handshake 查询的唯一入口,必须 O(1) 定位
//   - (org_id, created_at desc):管理 UI 按 org 列表
//   - principal_id unique:作为 principal 子表,每条 agent 恰好对应一条 principal
package model

import (
	"time"

	"gorm.io/gorm"

	principalmodel "github.com/eyrihe999-stack/Synapse/internal/principal/model"
)

// Agent agent 注册记录。
//
// 字段语义:
//
//   - AgentID                    握手时的 X-Agent-ID,全局唯一。系统生成 agt_<base64url>
//   - OrgID                      所属 org;**0 = 全局内嵌 agent(顶级系统 agent)**,其余为 org-scoped。
//                                orgs.id autoincrement 从 1 开始,0 不是合法值,作为 sentinel 语义清晰
//   - Kind                       分类:"system"(服务身份)/ "user"(代表 user 身份,PR #4' 起落地)。
//                                两种 kind 都用 apikey 鉴权,区别在于 user kind 要绑 owner_user_id
//   - OwnerUserID                `kind='user'` 时指向所属 user;`system` 时 NULL。
//                                PR #4' 加的字段,个人 agent 的归属表达
//   - AutoIncludeInNewChannels   TRUE 时新建 channel 自动把本 agent 加为 member。
//                                典型:全局顶级 agent TRUE;专项 agent / 个人 agent 通常 FALSE
//   - APIKey                     明文 key(V1 先不做 bcrypt,和 yaml 同等姿态)。格式 sk_<base64url>
//   - DisplayName                给管理员看,UI 展示用
//   - Enabled                    一键禁用;false 时 handshake 拒 + 在线连接被踢
//   - LastSeenAt                 最近一次 handshake 成功时间,监控用
//   - RotatedAt                  最近一次 rotate-key 时间,审计用
//   - CreatedByUID               哪个 user 创建的;系统 seed 的顶级 agent 此列为 0
type Agent struct {
	ID      uint64 `gorm:"primaryKey;autoIncrement"`
	AgentID string `gorm:"size:64;not null;uniqueIndex:uk_agent_id"`
	// PrincipalID 指向 principals 表的身份根。
	//   - PR #1 过渡期:default 0 占位;AutoMigrate 给存量 agent 加列时落 0,
	//     agents.RunMigrations 的 backfill 负责补齐为真实 principal id
	//   - 新建 agent 时由 Agent.BeforeCreate hook 自动建 principal 行并回填
	//   - 唯一约束通过 agents.RunMigrations 在 backfill 之后建(同 users.principal_id)
	PrincipalID              uint64  `gorm:"column:principal_id;not null;default:0"`
	OrgID                    uint64  `gorm:"not null;index:idx_agent_org_created,priority:1;index:idx_agent_org_kind,priority:1"`
	Kind                     string  `gorm:"size:16;not null;default:system;index:idx_agent_org_kind,priority:2"`
	OwnerUserID              *uint64 `gorm:"column:owner_user_id;index:idx_agent_owner_user"`
	AutoIncludeInNewChannels bool    `gorm:"column:auto_include_in_new_channels;not null;default:false;index:idx_agent_auto_include"`
	APIKey                   string  `gorm:"size:64;not null"`
	DisplayName              string  `gorm:"size:128;not null"`
	Enabled                  bool    `gorm:"not null;default:true"`
	LastSeenAt               *time.Time
	RotatedAt                *time.Time
	CreatedByUID             uint64    `gorm:"not null"`
	CreatedAt                time.Time `gorm:"not null;index:idx_agent_org_created,priority:2,sort:desc"`
	UpdatedAt                time.Time `gorm:"not null"`
}

// TableName 固定表名。PR #1 起从 agent_registry 重命名为 agents;重命名由
// migration.go 的一次性 DDL 完成,此处只声明最终名。
func (Agent) TableName() string { return "agents" }

// BeforeCreate GORM hook:新建 agent 时若 PrincipalID 未设置,自动插入一条
// principals(kind='agent') 行并回填 PrincipalID。事务性保证。
//
// Status 映射规则:Enabled=true → StatusActive(1);Enabled=false → StatusDisabled(2)。
func (a *Agent) BeforeCreate(tx *gorm.DB) error {
	if a.PrincipalID != 0 {
		return nil
	}
	status := principalmodel.StatusActive
	if !a.Enabled {
		status = principalmodel.StatusDisabled
	}
	p := &principalmodel.Principal{
		Kind:        principalmodel.KindAgent,
		DisplayName: a.DisplayName,
		Status:      status,
	}
	if err := tx.Create(p).Error; err != nil {
		return err
	}
	a.PrincipalID = p.ID
	return nil
}
