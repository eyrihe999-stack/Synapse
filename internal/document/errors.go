// errors.go document 模块错误码与 sentinel 错误。
//
// 错误码格式 HHHSSCCCC:HHH=HTTP,SS=14(document 模块号),CCCC=业务码。
package document

import "errors"

// ─── 400 段:请求/业务校验 ────────────────────────────────────────────────────

const (
	// CodeDocumentInvalidRequest 通用请求参数非法。
	CodeDocumentInvalidRequest = 400140010
	// CodeDocumentTitleInvalid 标题不合法(空 / 过长)。
	CodeDocumentTitleInvalid = 400140011
	// CodeDocumentMIMETypeUnsupported 上传文件 MIME 类型不在允许清单。
	CodeDocumentMIMETypeUnsupported = 400140013
	// CodeDocumentFileTooLarge 文件超过大小上限。
	CodeDocumentFileTooLarge = 400140014
	// CodeDocumentEmpty 文件为空。
	CodeDocumentEmpty = 400140015
)

// ─── 403 段:权限 ─────────────────────────────────────────────────────────────

// CodeDocumentPermissionDenied 用户无权执行此操作。
const CodeDocumentPermissionDenied = 403140010

// ─── 404 段:资源不存在 ──────────────────────────────────────────────────────

// CodeDocumentNotFound 文档不存在或不属于当前 org。
const CodeDocumentNotFound = 404140010

// ─── 500 / 503 段:内部/上游 ─────────────────────────────────────────────────

const (
	// CodeDocumentInternal 内部错误。
	CodeDocumentInternal = 500140000
	// CodeDocumentStorageFailed 对象存储读写失败。
	CodeDocumentStorageFailed = 503140020
	// CodeDocumentIndexFailed 向量索引链路失败(embedder 配置错/API 拒绝/PG 不可达),
	// 导致上传被整体回滚。和 StorageFailed 分开一个码,便于 operator 定位是索引侧还是 OSS 侧。
	CodeDocumentIndexFailed = 503140021
)

// ─── Sentinel Errors ────────────────────────────────────────────────────────

var (
	// ErrDocumentInvalidRequest 请求参数非法(缺 orgID / uploaderID 等)。
	ErrDocumentInvalidRequest = errors.New("document: invalid request")
	// ErrDocumentTitleInvalid 标题不合法(为空、只含空白、或超过 MaxTitleLength)。
	ErrDocumentTitleInvalid = errors.New("document: title invalid")
	// ErrDocumentMIMETypeUnsupported 上传的 MIME 类型不在 AllowedMIMETypes 白名单。
	ErrDocumentMIMETypeUnsupported = errors.New("document: mime type unsupported")
	// ErrDocumentFileTooLarge 文件字节数超过 MaxFileSizeBytes。
	ErrDocumentFileTooLarge = errors.New("document: file too large")
	// ErrDocumentEmpty 上传内容为空(Content 和 ContentReader 都没给,或读出 0 字节)。
	ErrDocumentEmpty = errors.New("document: empty content")
	// ErrDocumentPermissionDenied 当前成员没有所需权限点。
	ErrDocumentPermissionDenied = errors.New("document: permission denied")
	// ErrDocumentNotFound 文档不存在,或不属于当前 org(跨租户访问一律回这个错,不泄露存在性)。
	ErrDocumentNotFound = errors.New("document: not found")
	// ErrDocumentInternal 内部错误(DB 读写、构造器参数非法等)。
	ErrDocumentInternal = errors.New("document: internal error")
	// ErrDocumentStorageFailed OSS 读写失败(上游不可用或凭据失效)。
	ErrDocumentStorageFailed = errors.New("document: storage failed")
	// ErrDocumentIndexFailed 向量索引链路不可用(embedder 配置/鉴权错,或 PG 失败)。
	// 上传整体被回滚(MySQL 行 + OSS 对象一并补偿清理),用户侧等同于"上传失败"。
	ErrDocumentIndexFailed = errors.New("document: indexing failed")
)
