// indexes.go agent 模块索引确保。
package model

import "gorm.io/gorm"

// EnsureAgentIndexes 确保需要手动创建的复合索引。
// 大多数索引已通过 struct tag 定义,此处处理需要额外逻辑的情况。
// 当前无需额外索引时返回 nil。
func EnsureAgentIndexes(_ *gorm.DB) error {
	// 当前所有索引均通过 struct tag 定义,无需额外操作。
	return nil
}
