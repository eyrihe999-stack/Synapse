package retrieval

import "encoding/json"

// Tool MCP tool manifest 的最小表达;不耦合具体 MCP SDK,保持 retrieval 包零外部依赖。
// 序列化后可直接注册到 MCP server(Anthropic / 自建皆可)。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// BuildTools 基于注册表生成 LLM 可调用的 tool 列表。
//
// 命名约定:
//   - search_{modality}  按模态检索
//   - fetch_{modality}   按 ID 拉详情
//
// 为什么拆多个 tool 而不是单个 search(modality, ...) god tool:
//   - LLM 调用准确率取决于 input_schema 的具体性,god tool 的 filter 字段只能是 any,
//     schema 即 prompt 这层引导就没了;
//   - MCP 层面也更容易做每工具独立的 rate limit / auth / audit。
func BuildTools(reg *Registry) []Tool {
	mods := reg.List()
	out := make([]Tool, 0, len(mods)*2)
	for _, m := range mods {
		rv, ok := reg.Get(m)
		if !ok {
			continue
		}
		out = append(out, buildSearchTool(m, rv.FilterSchema()))
		out = append(out, buildFetchTool(m))
	}
	return out
}

func buildSearchTool(m Modality, filterSchema json.RawMessage) Tool {
	input := map[string]any{
		"type":     "object",
		"required": []string{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural-language query, keywords, or identifier name.",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"default":     8,
				"description": "Number of hits. Small = focused, large = broader context. Default 8 is a good first-pass.",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"", "vector", "bm25", "hybrid", "symbol"},
				"default":     "",
				"description": "Retrieval strategy. Empty = implementation default. Use \"symbol\" for exact identifier lookups (code only). Use \"hybrid\" when query contains proper nouns / API paths.",
			},
			"rerank": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "Enable cross-encoder rerank (higher precision, ~300ms extra). Turn on for final-draft research; off for quick scans.",
			},
			"filter": filterSchema,
		},
	}
	return Tool{
		Name:        "search_" + string(m),
		Description: searchDescription(m),
		InputSchema: mustMarshal(input),
	}
}

func buildFetchTool(m Modality) Tool {
	input := map[string]any{
		"type":     "object",
		"required": []string{"id"},
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Hit ID returned by search_" + string(m) + " or a parent ID from a previous hit.",
			},
		},
	}
	return Tool{
		Name:        "fetch_" + string(m),
		Description: "Fetch the full content of one " + string(m) + " hit by ID. Use when the snippet is insufficient and you need the full chunk / section / file.",
		InputSchema: mustMarshal(input),
	}
}

func searchDescription(m Modality) string {
	switch m {
	case ModalityCode:
		return "Search source code chunks (function / method / class level). Use mode=\"symbol\" when the user names a specific identifier (e.g. \"ChatService\"); vector or hybrid for semantic queries. Filter by languages / repo_ids to narrow scope."
	case ModalityDocument:
		return "Search product docs / markdown / PRDs / notes. Filter by content_types (heading / text / code / table) to narrow scope; set return_parents=true when section-level context matters. hybrid mode mixes BM25 for proper-noun / API-path recall."
	case ModalityImage:
		return "Search images (placeholder until image ingestion is wired)."
	case ModalityBug:
		return "Search historical bugs / incidents (placeholder)."
	default:
		return "Search " + string(m) + " knowledge."
	}
}
