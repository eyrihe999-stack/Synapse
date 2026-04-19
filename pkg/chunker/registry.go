// registry.go 按 mime_type / 文件扩展名路由到合适的 Profile。
//
// Registry 是无状态的查表,一次构造常驻服务内。Pick 不会返回 nil —— 不匹配时落到 fallback。
// 这样的设计让 index_pipeline 里不用写一堆 "if md then ... else ..." 的分支。
package chunker

import "strings"

// Registry 维护 mime_type / 扩展名到 Profile 的映射。
//
// byMimeType 优先级高于 byExtension:
//   - mime_type 是用户上传时声明的(或 HTTP 探测的),语义最明确。
//   - 扩展名是 fallback —— 很多工具不设 mime_type 只给文件名,比如 re-ingest 本地文件。
//
// 两者都 miss 时落到 fallback(默认 plain_text),保证任何输入都有 profile 可用。
type Registry struct {
	byMimeType  map[string]Profile
	byExtension map[string]Profile
	fallback    Profile
}

// Pick 按 mime_type 优先,扩展名其次,最后 fallback 返回 Profile。永不 nil。
//
// mime_type 匹配是大小写无关的精确匹配(不做 `text/markdown; charset=utf-8` 这种参数拆分,
// 约定调用方传入时去掉参数部分 —— 上传链路已经在 MIME 校验阶段 normalize 过)。
func (r *Registry) Pick(mimeType, fileName string) Profile {
	if mimeType != "" {
		if p, ok := r.byMimeType[strings.ToLower(mimeType)]; ok {
			return p
		}
	}
	if idx := strings.LastIndex(fileName, "."); idx >= 0 {
		ext := strings.ToLower(fileName[idx:])
		if p, ok := r.byExtension[ext]; ok {
			return p
		}
	}
	return r.fallback
}

// Fallback 暴露兜底 profile,某些场景(如 ingestion pipeline 未拿到 mime/filename)直接用。
func (r *Registry) Fallback() Profile {
	return r.fallback
}

// DefaultRegistry 默认路由:markdown 走 markdown_structured,其他文本走 plain_text。
//
// 将来接入 code / bug / image 源时,在这里追加注册即可(或引入 Option 化构造)。
// 当前只区分 markdown 和 plain,因为除这两类外还没有内容进 Synapse。
func DefaultRegistry(cfg Config) *Registry {
	plain := NewPlainText(cfg)
	md := NewMarkdownStructured(cfg)
	return &Registry{
		byMimeType: map[string]Profile{
			"text/markdown":   md,
			"text/x-markdown": md,
			"text/plain":      plain,
		},
		byExtension: map[string]Profile{
			".md":       md,
			".markdown": md,
			".mdx":      md,
			".txt":      plain,
			".text":     plain,
		},
		fallback: plain,
	}
}
