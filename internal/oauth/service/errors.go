// errors.go service 层 sentinel 错误,按 RFC 6749 §5.2 / §4.1.2.1 错误码分类。
//
// handler 把这些错误映射到 HTTP 响应:
//   - /token 端点返 { "error": <code>, "error_description": ... } + 400/401
//   - /authorize 端点做 302 到 redirect_uri 带 error query param
//   - /register 端点返 400 + JSON body
package service

import "errors"

// OAuth 协议层错误(error code 对应 oauth.ErrCode* 常量,见 internal/oauth/const.go)。
// 每个错误都附一个短 description,handler 可直接透传给 client。

// 请求格式 / 必填参数类错误 — 对应 "invalid_request"。
var ErrInvalidRequest = errors.New("oauth: invalid request")

// 客户端身份相关 — 对应 "invalid_client"。
var ErrInvalidClient = errors.New("oauth: invalid client")

// grant 错误(auth code 失效 / redirect 不匹配 / PKCE 校验失败 / refresh reuse)— 对应 "invalid_grant"。
var ErrInvalidGrant = errors.New("oauth: invalid grant")

// DCR 元数据问题(redirect_uri 不合法 / grant_type 不支持等)— 对应 "invalid_client_metadata"。
var ErrInvalidClientMetadata = errors.New("oauth: invalid client metadata")

// redirect_uri 不匹配已注册的 — 对应 "invalid_redirect_uri"。
var ErrInvalidRedirectURI = errors.New("oauth: invalid redirect_uri")

// scope 不被 client 允许 / 不是支持的 scope — 对应 "invalid_scope"。
var ErrInvalidScope = errors.New("oauth: invalid scope")

// response_type 不支持 — 对应 "unsupported_response_type"。
var ErrUnsupportedResponseType = errors.New("oauth: unsupported response_type")

// grant_type 不支持 — 对应 "unsupported_grant_type"。
var ErrUnsupportedGrantType = errors.New("oauth: unsupported grant_type")

// 用户在 consent 页拒绝 — 对应 "access_denied"(/authorize 路径)。
var ErrAccessDenied = errors.New("oauth: access denied by user")

// 服务器自身故障 — 对应 "server_error"。
var ErrServerError = errors.New("oauth: server error")
