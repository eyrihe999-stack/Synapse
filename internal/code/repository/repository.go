// Package repository code 模块 Postgres 数据访问层。
//
// 所有表都在 PG 一个库,一个 *gorm.DB 实例贯穿所有 repo。
// 三个 sub-repo 按表边界拆文件(repo.go / file.go / chunk.go),聚合接口 Repository
// 把它们组合成单一注入点供 service 层使用。
package repository

import (
	"gorm.io/gorm"
)

// Repository 聚合接口 —— service 层通过这一个接口拿到代码模块的全部 CRUD 能力。
//
// 拆三个 sub-repo 的理由:每张表的生命周期和操作集差异很大
// (repo 元信息稀疏更新 / file 批量 upsert + 删 / chunk 批量 swap + 状态机)。
// 接口按表分开,服务层按需调用,mock 时也只 mock 会用到的那组方法。
type Repository interface {
	Repos() CodeRepositoryRepo
	Files() CodeFileRepo
	Chunks() CodeChunkRepo
}

type repository struct {
	repos  CodeRepositoryRepo
	files  CodeFileRepo
	chunks CodeChunkRepo
}

// New 构造聚合 Repository。pgDB 必须非 nil —— 调用方(main 装配)负责:未启用 PG 时整个 code 模块不装配。
func New(pgDB *gorm.DB) Repository {
	return &repository{
		repos:  NewCodeRepositoryRepo(pgDB),
		files:  NewCodeFileRepo(pgDB),
		chunks: NewCodeChunkRepo(pgDB),
	}
}

func (r *repository) Repos() CodeRepositoryRepo { return r.repos }
func (r *repository) Files() CodeFileRepo       { return r.files }
func (r *repository) Chunks() CodeChunkRepo     { return r.chunks }
