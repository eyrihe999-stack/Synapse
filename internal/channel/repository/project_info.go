package repository

import (
	"context"
	"time"
)

// ChannelProjectInfo channel 模块需要的 project 最小信息(避免反向引入 pm/model)。
//
// channel 模块在新建 channel / 加成员 / 归档 channel 等流程里要查 project 的
// org_id(权限校验)和 archived_at(已归档不让改)。Project 实体本身已迁到 pm 模块,
// 这里只用一个本地 struct 接 SQL 列;字段没列在外都是 channel 用不到的。
type ChannelProjectInfo struct {
	ID         uint64
	OrgID      uint64
	ArchivedAt *time.Time
}

// FindProjectInfo 按 id 查 project 的最小信息(org_id + archived_at);
// 查无返 gorm.ErrRecordNotFound。
//
// 走原始 projects 表(列名沿用 pm 模块定义),不通过 pm/model.Project 类型,
// 保持 channel 模块对 pm 零静态依赖。
func (r *gormRepository) FindProjectInfo(ctx context.Context, projectID uint64) (*ChannelProjectInfo, error) {
	var info ChannelProjectInfo
	err := r.db.WithContext(ctx).
		Table("projects").
		Select("id, org_id, archived_at").
		Where("id = ?", projectID).
		Take(&info).Error
	if err != nil {
		return nil, err
	}
	return &info, nil
}
