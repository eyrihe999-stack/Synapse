package service

import (
	"errors"

	"github.com/go-sql-driver/mysql"
)

// isUniqueViolation 判断是否是 MySQL 唯一索引冲突(errno 1062)。
//
// 用途:INSERT / UPDATE 撞到唯一索引时,把底层 driver 错误翻译成模块的哨兵错误。
// 参见 user/service/mysql_err.go 同样的套路。
func isUniqueViolation(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}
