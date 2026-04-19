package code

import "errors"

// Sentinel 错误。handler 层按 errors.Is 分派到 HTTP 响应码,service 层的内部错用 ErrCodeInternal 包一层。
// 目前只暴露最小集,随新端点加入再补。
var (
	// ErrRepositoryNotFound 按 ID / external ID 查 code_repositories 未命中。
	ErrRepositoryNotFound = errors.New("code: repository not found")

	// ErrFileNotFound 按 ID / (repo, path) 查 code_files 未命中。
	ErrFileNotFound = errors.New("code: file not found")

	// ErrCodeInternal 内部错误的包装标签,handler 统一转 500。
	// 具体 root cause 走 %w 链,日志能看到,HTTP 响应不暴露。
	ErrCodeInternal = errors.New("code: internal error")
)
