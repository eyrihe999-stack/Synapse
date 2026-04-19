package retrieval

import "encoding/json"

// CodeFilter ModalityCode 的 Filter JSON 反序列化目标。
// 对齐 internal/code/repository.ChunkSearchFilter + service.SearchOptions。
type CodeFilter struct {
	Languages       []string `json:"languages,omitempty"`        // e.g. ["go","ts"]
	RepoIDs         []uint64 `json:"repo_ids,omitempty"`         // 限定只搜这几个仓库
	ChunkKinds      []string `json:"chunk_kinds,omitempty"`      // function / method / class / preamble / unparsed
	SymbolName      string   `json:"symbol_name,omitempty"`      // Mode=symbol 时作为主查询;其他模式可作为 re-score 提示
	IncludeSiblings bool     `json:"include_siblings,omitempty"` // 命中 chunk 时同文件邻近 chunk 一并返(→ Hit.Related)
	SiblingWindow   int      `json:"sibling_window,omitempty"`   // 同文件 chunks 超过此阈值时只取邻近 N 条;<=0 走服务默认
}

// DocumentFilter ModalityDocument 的 Filter JSON。
// 对齐 internal/document/repository.ChunkSearchFilter + service.SearchChunksOptions(MaxPerDoc / 阈值门)。
type DocumentFilter struct {
	DocIDs              []uint64 `json:"doc_ids,omitempty"`               // 限定只搜这几个文档
	HeadingPathContains []string `json:"heading_path_contains,omitempty"` // metadata.heading_path 数组必须包含这些元素(AND),走 GIN 索引
	MaxPerDoc           int      `json:"max_per_doc,omitempty"`           // 同 doc 限流;0 = 不限
	MinSimilarity       *float32 `json:"min_similarity,omitempty"`        // vector 通路置信度门
	MinRerankScore      *float32 `json:"min_rerank_score,omitempty"`      // rerank 通路置信度门
	ReturnParents       bool     `json:"return_parents,omitempty"`        // 命中 leaf 时连父 chunk 一起返(→ Hit.Related)
}

// CodeFilterSchema 返回用于 MCP tool input_schema 的 JSON Schema 片段。
// 描述写得具体一点 —— schema 即 prompt,LLM 调用准确率直接看这里。
func CodeFilterSchema() json.RawMessage {
	return mustMarshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"languages": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Restrict to these languages, e.g. [\"go\",\"ts\"]. Omit to search all.",
			},
			"repo_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Limit to specific repository IDs. Use list_repositories-style tools first if unknown.",
			},
			"chunk_kinds": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{"function", "method", "class", "preamble", "unparsed"},
				},
				"description": "Narrow to certain structural kinds. Use [\"preamble\"] for imports/file-level comments.",
			},
			"symbol_name": map[string]any{
				"type":        "string",
				"description": "Exact-ish identifier (camelCase-aware ILIKE match). Required when top-level mode=\"symbol\".",
			},
			"include_siblings": map[string]any{
				"type":        "boolean",
				"description": "When a chunk hits, also return same-file neighboring chunks as Hit.Related. Use to understand surrounding functions.",
			},
			"sibling_window": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Cap sibling count when include_siblings=true. 0 = service default (20).",
			},
		},
	})
}

// DocumentFilterSchema 返回用于 MCP tool input_schema 的 JSON Schema 片段。
func DocumentFilterSchema() json.RawMessage {
	return mustMarshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"doc_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Limit to specific document IDs.",
			},
			"heading_path_contains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Metadata.heading_path must contain all these (AND). Example: [\"支付\"] matches chunks under any \"支付\" heading regardless of depth.",
			},
			"max_per_doc": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Cap hits per doc. 0 = unlimited. Recommended 2-3 for diverse PRD context (avoid one doc hogging top-K).",
			},
			"min_similarity": map[string]any{
				"type":        "number",
				"description": "Drop hits below this similarity (0-1). Reference: 0.3 filters clearly unrelated; 0.5 filters weak matches.",
			},
			"min_rerank_score": map[string]any{
				"type":        "number",
				"description": "Drop hits below this rerank score. BGE-reranker-v2-m3 raw: 0.0 ≈ coin-flip, 1.0 ≈ strong match.",
			},
			"return_parents": map[string]any{
				"type":        "boolean",
				"description": "When a leaf chunk hits, also return its parent section chunk ID in Hit.Related. Use for heading-level context.",
			},
		},
	})
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
