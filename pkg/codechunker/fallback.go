// fallback.go tree-sitter 不支持 / parse 失败时的启发式切分。
//
// 策略:按固定行 window 切。不尝试识别函数边界 —— 启发式能做的"聪明"基本都会被边界情况打脸
// (Python 缩进 vs C 花括号 vs Lua `end`...),不如承认这是降级路径,保证简单可预测,
// chunk kind 标 unparsed 让检索层知道质量低。
package codechunker

import "strings"

// chunkHeuristic 走字节 + 行数双上限切分。和 splitLargePiece 共用 splitByBytesAndLines,
// 确保任何路径产出的 chunk 都受 MaxChunkBytes 约束,不会撑爆 embedder。
//
// language 参数用于回填 Piece.Language —— 即使走 fallback,如果 LanguageFromFilename 识别出来了
// 也要保留这个信息,让检索层能按语言 filter。未知语言 language = "unknown"。
func (c *chunker) chunkHeuristic(content, language string) []Piece {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if language == "" {
		language = "unknown"
	}
	return splitByBytesAndLines(content, language, 1 /* 从第 1 行起 */, "" /* 无 symbol 归属 */, ChunkKindUnparsed, c.cfg)
}
