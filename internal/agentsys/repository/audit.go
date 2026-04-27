// Package repository agentsys 持久化层。
//
// AuditRepo 只暴露 Insert —— 审计表当前 PR 没有读取路径(查询走 ad-hoc SQL);
// 未来 admin UI 要翻审计记录时再扩 List / ListByOrg 等。
package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/eyrihe999-stack/Synapse/internal/agentsys/model"
)

// AuditRepo audit_events 表操作接口。
type AuditRepo interface {
	Insert(ctx context.Context, ev *model.AuditEvent) error
}

type auditRepo struct {
	db *gorm.DB
}

// NewAuditRepo 构造 AuditRepo 实例。
func NewAuditRepo(db *gorm.DB) AuditRepo {
	return &auditRepo{db: db}
}

// Insert 落库一条审计。
// 失败场景:DB 错误(唯一约束/连接/语法)全部 wrap 后返回;调用方(orchestrator
// handler)一般记 warn 继续,不让"写审计失败"挡住用户的正常响应 —— 但如果 DB
// 层面挂了,后续业务操作也会挂,自然降级成 PEL 重放。
func (r *auditRepo) Insert(ctx context.Context, ev *model.AuditEvent) error {
	if err := r.db.WithContext(ctx).Create(ev).Error; err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// DetailJSON 用于 handler 传给 Insert 之前构造 JSON detail 字段(可为空)。
// 对 map[string]any 做一次 json.Marshal,失败就退化为 nil(JSON 列可为空)。
// 把这个辅助函数放在 repo 包:减少 handler 直接触 datatypes.JSON 的耦合。
func DetailJSON(m map[string]any) datatypes.JSON {
	if len(m) == 0 {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return datatypes.JSON(raw)
}
