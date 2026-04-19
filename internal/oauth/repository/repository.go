// Package repository oauth 模块 MySQL 数据访问层。
//
// 三个 sub-repo 按表边界拆文件(client.go / auth_code.go / refresh_token.go),
// 聚合接口 Repository 提供单一注入点。
package repository

import (
	"gorm.io/gorm"
)

// Repository 聚合 oauth 相关全部 CRUD 能力。
type Repository interface {
	Clients() ClientRepo
	AuthCodes() AuthCodeRepo
	RefreshTokens() RefreshTokenRepo
}

type repository struct {
	clients       ClientRepo
	authCodes     AuthCodeRepo
	refreshTokens RefreshTokenRepo
}

// New 构造。db 必须非 nil —— 调用方(main 装配)负责 MySQL 未配置时整个 oauth 模块不装配。
func New(db *gorm.DB) Repository {
	return &repository{
		clients:       NewClientRepo(db),
		authCodes:     NewAuthCodeRepo(db),
		refreshTokens: NewRefreshTokenRepo(db),
	}
}

func (r *repository) Clients() ClientRepo             { return r.clients }
func (r *repository) AuthCodes() AuthCodeRepo         { return r.authCodes }
func (r *repository) RefreshTokens() RefreshTokenRepo { return r.refreshTokens }
