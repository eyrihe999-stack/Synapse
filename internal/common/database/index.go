package database

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"
)

// IndexSpec describes a database index to be created idempotently.
type IndexSpec struct {
	Table      string
	Name       string
	Columns    []string
	Expression string // 函数索引表达式,设置后忽略 Columns
	Unique     bool
}

func indexExists(db *gorm.DB, table, indexName string) (bool, error) {
	var n int
	err := db.Raw(
		"SELECT 1 FROM information_schema.STATISTICS WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1",
		table, indexName,
	).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func EnsureIndex(db *gorm.DB, spec IndexSpec) error {
	exists, err := indexExists(db, spec.Table, spec.Name)
	if err != nil {
		return fmt.Errorf("check index %s.%s: %w", spec.Table, spec.Name, err)
	}
	if exists {
		return nil
	}
	cols := ""
	for i, c := range spec.Columns {
		if i > 0 {
			cols += ", "
		}
		if idx := strings.IndexByte(c, ' '); idx > 0 {
			cols += "`" + c[:idx] + "` " + c[idx+1:]
		} else {
			cols += "`" + c + "`"
		}
	}
	kind := "INDEX"
	if spec.Unique {
		kind = "UNIQUE INDEX"
	}
	if spec.Expression != "" {
		cols = spec.Expression
	}
	sql := fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, "`"+spec.Name+"`", "`"+spec.Table+"`", cols)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("create index %s on %s: %w", spec.Name, spec.Table, err)
	}
	log.Printf("[dbutil] created index %s on %s", spec.Name, spec.Table)
	return nil
}

//sayso-lint:ignore unused-export,godoc-error-undoc
func DropIndex(db *gorm.DB, table, indexName string) error {
	exists, err := indexExists(db, table, indexName)
	if err != nil {
		return fmt.Errorf("check index %s.%s: %w", table, indexName, err)
	}
	if !exists {
		return nil
	}
	sql := fmt.Sprintf("DROP INDEX `%s` ON `%s`", indexName, table)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("drop index %s on %s: %w", indexName, table, err)
	}
	log.Printf("[dbutil] dropped index %s on %s", indexName, table)
	return nil
}

func EnsureIndexes(db *gorm.DB, specs []IndexSpec) error {
	if db == nil {
		return nil
	}
	for _, spec := range specs {
		if err := EnsureIndex(db, spec); err != nil {
			return err
		}
	}
	return nil
}
