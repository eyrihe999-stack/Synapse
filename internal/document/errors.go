// errors.go 文档持久层 sentinel 错误。
//
// 错误码格式 HHHSSCCCC:SS=15 为 document 模块预留段。
// 本层职责只有持久化,不做业务校验 → 错误类别非常少:not found / 冲突 / 内部。
package document

import "errors"

// ─── 错误码 ──────────────────────────────────────────────────────────────────

const (
	// CodeDocumentInvalidInput 参数校验失败(空文件名 / OrgID=0 / 空内容等)。
	CodeDocumentInvalidInput = 400150000

	// CodeDocumentNotFound 文档不存在。
	CodeDocumentNotFound = 404150020

	// CodeDocumentInternal 底层存储错误(PG 不可达 / SQL 错)。
	CodeDocumentInternal = 500150000

	// CodeDocumentDimMismatch 启动期维度校验失败。
	CodeDocumentDimMismatch = 500150001
)

// ─── Sentinel ────────────────────────────────────────────────────────────────

var (
	// ErrDocumentInvalidInput 参数校验失败,handler 层据此返 400。
	ErrDocumentInvalidInput = errors.New("document: invalid input")

	// ErrDocumentNotFound 资源未找到,service 层据此返 404。
	ErrDocumentNotFound = errors.New("document: not found")

	// ErrDocumentInternal 底层存储故障,service 层据此返 500。
	ErrDocumentInternal = errors.New("document: internal error")

	// ErrDimMismatch vector 列维度与 cfg.Embedding.Text.ModelDim 不一致。
	// 装配层捕到此错必须 fatal:继续跑会静默写脏数据。
	ErrDimMismatch = errors.New("document: embedding dim mismatch")
)
