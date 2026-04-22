// mysql_err.go MySQL 驱动错误分类:把 driver 层错误码映射回业务 sentinel。
package service

import (
	"errors"

	"github.com/go-sql-driver/mysql"
)

// mysqlErrDupEntry MySQL 唯一索引冲突错误码。Register / ChangeEmail / OAuth 并发竞争时,
// 先行查询没命中但 CreateUser/UpdateFields 撞 unique 索引,需要把它映射回 ErrEmailAlreadyRegistered。
const mysqlErrDupEntry = 1062

// isDupEntryErr 判断是否为 MySQL 唯一索引冲突。
func isDupEntryErr(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDupEntry
}
