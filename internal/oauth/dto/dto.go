// Package dto OAuth 模块 HTTP 请求 / 响应 schema。
package dto

import "time"

// ─── Client 管理(admin REST API)──────────────────────────────────────

// CreateClientRequest 用户后台建 OAuth client 的请求。
// GrantTypes / TokenAuthMethod 可缺省,服务端填默认值(authorization_code+refresh_token / client_secret_post)。
type CreateClientRequest struct {
	ClientName      string   `json:"client_name" binding:"required"`
	RedirectURIs    []string `json:"redirect_uris" binding:"required"`
	GrantTypes      []string `json:"grant_types,omitempty"`
	TokenAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// CreateClientResponse 建成功返响应(**明文 client_secret 只此一次**)。
type CreateClientResponse struct {
	ClientID                string    `json:"client_id"`
	ClientSecret            string    `json:"client_secret"` // 明文,只此一次
	ClientName              string    `json:"client_name"`
	RedirectURIs            []string  `json:"redirect_uris"`
	GrantTypes              []string  `json:"grant_types"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	RegisteredVia           string    `json:"registered_via"`
	CreatedAt               time.Time `json:"created_at"`
}

// ListClientResponse 列表响应(不含 secret)。
type ListClientResponse struct {
	ID                      uint64    `json:"id"`
	ClientID                string    `json:"client_id"`
	ClientName              string    `json:"client_name"`
	RedirectURIs            []string  `json:"redirect_uris"`
	GrantTypes              []string  `json:"grant_types"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	RegisteredVia           string    `json:"registered_via"`
	Disabled                bool      `json:"disabled"`
	CreatedAt               time.Time `json:"created_at"`
}
