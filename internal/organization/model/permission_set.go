// permission_set.go OrgRole.Permissions 字段的 GORM 自定义类型。
//
// 存储:MySQL JSON 列(["org.update", "member.invite", ...]),
// 应用层暴露成 []string 直接用。
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// PermissionSet 是 OrgRole.Permissions 的应用层类型。
//
// 序列化:JSON 数组字符串(["org.update","member.invite",...]),空集存 "[]" 而不是 NULL。
// nil 视为空集;Scan 出空字符串 / NULL 都还原为 nil。
type PermissionSet []string

// Value 实现 driver.Valuer。nil → "[]";其他 → JSON encode。
func (ps PermissionSet) Value() (driver.Value, error) {
	if ps == nil {
		return "[]", nil
	}
	b, err := json.Marshal([]string(ps))
	if err != nil {
		return nil, fmt.Errorf("PermissionSet marshal: %w", err)
	}
	return string(b), nil
}

// Scan 实现 sql.Scanner。
func (ps *PermissionSet) Scan(value any) error {
	if value == nil {
		*ps = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("PermissionSet: unsupported scan type %T", value)
	}
	if len(raw) == 0 {
		*ps = nil
		return nil
	}
	var s []string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("PermissionSet unmarshal %q: %w", string(raw), err)
	}
	*ps = PermissionSet(s)
	return nil
}

// GormDataType 让 gorm 在 AutoMigrate 时把列建成 JSON。
func (PermissionSet) GormDataType() string { return "json" }

// Has 检查是否包含某 perm。
func (ps PermissionSet) Has(perm string) bool {
	for _, p := range ps {
		if p == perm {
			return true
		}
	}
	return false
}
