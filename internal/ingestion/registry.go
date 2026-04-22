package ingestion

import "fmt"

// Registry 按 source_type / doc 属性路由到 chunker 和 persister。
//
// 不对称设计:
//
//   - chunker 路由更精细(同一 source_type 可能有多种 chunker,按 MIMEType/Language 选):
//     走 ChunkerSelector 函数,装配层写 if-else 决定
//   - persister 是 source_type → 一对一:简单 map 查
//
// 这样 Registry 自己只做查表,不引入任何 source type 知识(装配期外部注入)。
type Registry struct {
	chunkerSelector ChunkerSelector
	persisters      map[string]Persister
}

// ChunkerSelector 按 NormalizedDoc 的字段选 Chunker。装配侧典型实现:
//
//	func(doc *ingestion.NormalizedDoc) ingestion.Chunker {
//	    if doc.Language != "" { return codeChunker }
//	    if strings.HasPrefix(doc.MIMEType, "text/markdown") { return markdownChunker }
//	    return plainChunker
//	}
//
// 返 nil 会让 pipeline 报 "no chunker available" 错(未知内容类型 / 装配漏注册)。
type ChunkerSelector func(doc *NormalizedDoc) Chunker

// NewRegistry 构造。chunkerSelector 不得为 nil;至少注册一个 persister。
//
// 同一 SourceType 重复注册 persister 视为配置错,返 error(防止静默后注册覆盖先注册)。
// 所有 error 场景都是装配期配置错,调用方应在 main fatal 掉;此处不打日志避免与 fatal 打重。
func NewRegistry(chunkerSelector ChunkerSelector, persisters ...Persister) (*Registry, error) {
	if chunkerSelector == nil {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion: chunker selector is nil")
	}
	if len(persisters) == 0 {
		//sayso-lint:ignore log-coverage
		return nil, fmt.Errorf("ingestion: at least one persister required")
	}
	pMap := make(map[string]Persister, len(persisters))
	for _, p := range persisters {
		st := p.SourceType()
		if st == "" {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("ingestion: persister %T returned empty SourceType", p)
		}
		if _, dup := pMap[st]; dup {
			//sayso-lint:ignore log-coverage
			return nil, fmt.Errorf("ingestion: duplicate persister for source_type %q", st)
		}
		pMap[st] = p
	}
	return &Registry{chunkerSelector: chunkerSelector, persisters: pMap}, nil
}

// PickChunker 选 chunker。返 nil 表示 selector 判断没有合适策略。
func (r *Registry) PickChunker(doc *NormalizedDoc) Chunker {
	return r.chunkerSelector(doc)
}

// PickPersister 按 source_type 拿 persister。未注册返 nil。
func (r *Registry) PickPersister(sourceType string) Persister {
	return r.persisters[sourceType]
}
