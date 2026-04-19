// treesitter.go tree-sitter 驱动的 AST 切分。
//
// 流程:
//   1. Parse 整个文件 → 根节点
//   2. 遍历根节点的"直接 named 子节点"(不递归),按 spec.Definition 判定哪些是独立 chunk
//   3. 所有 definition 之前的部分 = preamble(imports + 文件级注释)
//   4. 每个 definition → 一个 Piece
//   5. 单 Piece 超过 MaxChunkBytes → 按行 window 切成多段,kind 降级成 unparsed(保留行号)
package codechunker

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// maxSignatureBytes Piece.Signature 超过这个长度截断。对特别嵌套的泛型签名(C++/Rust)有保护作用。
const maxSignatureBytes = 1024

// chunkWithAST 走 tree-sitter 切分。失败(parse error)返 non-nil error,调用方 fallback 到 heuristic。
//
// tree-sitter 的 ParseCtx 即使遇到语法错误也能返回 tree(带 error 节点),只有极端情况才真返 err
// —— 所以这里的 err 实际上很少发生,大多数"坏语法"也能出一棵"尽力而为"的 AST。
func (c *chunker) chunkWithAST(spec *languageSpec, content string) ([]Piece, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(spec.Language)

	src := []byte(content)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return nil, nil
	}

	var pieces []Piece
	var defNodes []*sitter.Node
	var firstDefStart uint32 = ^uint32(0) // max uint32 = "尚无 def"

	// 只扫 root 的 named children —— named 过滤掉 punctuation / whitespace。
	// 嵌套定义不展开:class 内的方法打包在 class 的一个 chunk 里,保留阅读上下文。
	n := int(root.NamedChildCount())
	for i := 0; i < n; i++ {
		child := root.NamedChild(i)
		if child == nil {
			continue
		}
		if _, ok := spec.Definition[child.Type()]; ok {
			defNodes = append(defNodes, child)
			if child.StartByte() < firstDefStart {
				firstDefStart = child.StartByte()
			}
		}
	}

	// Preamble:从文件头到第一个 definition 之前。包含 imports / package / 文件级注释 + 顶层变量。
	// 没有 definition 时整个文件走 heuristic(return nil pieces → fallback),避免 "整个文件都是 preamble" 的奇怪切分。
	//
	// 注意:preamble 可能很大(Go 里 var block 几百行字面量、Python 里巨大 config 常量)—— 和 definition 一样
	// 走字节 + 行数双上限切分,避免单 chunk 撑爆 embedder。
	if firstDefStart != ^uint32(0) && firstDefStart > 0 {
		preambleRaw := string(src[:firstDefStart])
		if strings.TrimSpace(preambleRaw) != "" {
			preambleContent := strings.TrimRight(preambleRaw, "\n \t")
			preamblePiece := Piece{
				Kind:       ChunkKindPreamble,
				Content:    preambleContent,
				LineStart:  1,
				LineEnd:    countLines(preambleContent),
				TokenCount: estimateTokens(preambleContent),
				Language:   spec.Name,
			}
			if len(preambleContent) > c.cfg.MaxChunkBytes {
				pieces = append(pieces, splitLargePiece(preamblePiece, c.cfg)...)
			} else {
				pieces = append(pieces, preamblePiece)
			}
		}
	}

	// 没找到任何 definition(纯配置文件、无函数的脚本 etc.)→ 返 nil,让 chunker.Chunk 兜底走 heuristic。
	// 已收集的 preamble 也抛弃 —— 反正整份内容会被 heuristic 切完,不做双份。
	if len(defNodes) == 0 {
		return nil, nil
	}

	for _, node := range defNodes {
		kind := spec.Definition[node.Type()]
		name := extractName(node, src, spec.NameField)
		signature := extractSignature(node, src)
		piece := Piece{
			Kind:       kind,
			SymbolName: name,
			Signature:  signature,
			Content:    string(src[node.StartByte():node.EndByte()]),
			LineStart:  int(node.StartPoint().Row) + 1,
			LineEnd:    int(node.EndPoint().Row) + 1,
			Language:   spec.Name,
		}
		piece.TokenCount = estimateTokens(piece.Content)

		// 超大 chunk(巨型函数)拆成行 window:保留 LineStart + 切段 —— kind 降级 unparsed
		// 让检索层知道这些片段语义不完整。
		if len(piece.Content) > c.cfg.MaxChunkBytes {
			pieces = append(pieces, splitLargePiece(piece, c.cfg)...)
			continue
		}
		pieces = append(pieces, piece)
	}
	return pieces, nil
}

// extractName 从 node 的 NameField 列出的 field 名依次找,第一个命中就返。
//
// Fallback:若本节点的 field 都没命中,再看第一个 named child 身上的 field。
// 这层 fallback 覆盖"包装类"AST —— Go 的 `type_declaration` 把 name 藏在 `type_spec` 子节点,
// Python 的 `decorated_definition` 把 name 藏在内部 `function_definition`,
// TS/JS 的 `export_statement` 把 name 藏在内部 `function_declaration`。
// 只下沉一层,避免递归导致误取(如一个函数里嵌套函数的 name)。
func extractName(node *sitter.Node, src []byte, nameFields []string) string {
	for _, f := range nameFields {
		if sub := node.ChildByFieldName(f); sub != nil {
			return string(src[sub.StartByte():sub.EndByte()])
		}
	}
	if node.NamedChildCount() > 0 {
		first := node.NamedChild(0)
		for _, f := range nameFields {
			if sub := first.ChildByFieldName(f); sub != nil {
				return string(src[sub.StartByte():sub.EndByte()])
			}
		}
	}
	return ""
}

// extractSignature 取 node 的"第一行"作为签名。
//
// 函数定义的首行通常包含完整 signature(func/method 定义 + 参数 + 返回类型),截断到
// 第一个 '{'(Go/Java/JS/Rust)或 ':'(Python)或行尾,取前 maxSignatureBytes 字节。
// 精准的 signature 抽取要 per-language query,MVP 这个粗粒度提取够用 —— agent 看到的签名
// 实际上就是代码第一行,误差对使用者透明。
func extractSignature(node *sitter.Node, src []byte) string {
	content := string(src[node.StartByte():node.EndByte()])
	// 只看第一行
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		content = content[:idx]
	}
	// 去掉 body 开头的 { 或 : 及之后的内容。注意 python 的 `def f(x):` 要保留整行的 ':' 前缀。
	// 简单处理:找第一个 '{' 截断(Python 没 '{' 不影响),':' 不处理。
	if idx := strings.IndexByte(content, '{'); idx >= 0 {
		content = content[:idx]
	}
	content = strings.TrimSpace(content)
	if len(content) > maxSignatureBytes {
		content = content[:maxSignatureBytes-3] + "..."
	}
	return content
}

// splitLargePiece 把一个超长 Piece 按"字节 + 行数"双上限切成多段。kind 降级为 unparsed,
// 保留原始 SymbolName + 起始行号偏移(让 agent 能知道"这是 FooFunc 的第二段")。
//
// 双上限语义:buf 累积期间,先碰到哪个上限(字节 / 行数)就 flush 出一个 chunk。
// 单行本身就超过 MaxChunkBytes(极端场景:minified 文件、内嵌 base64)→ 该行独立出一个超大 chunk,
// 上层 ingest 会在 embed 前做硬截断兜底,这里不强切一行中间(保持可读性)。
func splitLargePiece(p Piece, cfg Config) []Piece {
	return splitByBytesAndLines(p.Content, p.Language, p.LineStart, p.SymbolName, ChunkKindUnparsed, cfg)
}

// splitByBytesAndLines 通用的"字节 + 行数双上限"切分器。
// splitLargePiece 和 chunkHeuristic 共用 —— 确保所有产出 chunk 都受控。
//
// startLine:返回 Piece 的 LineStart 从此开始计(1-based);symbolName:给每个片段打上的符号归属;
// kind:统一标记(当前调用都传 unparsed,保留参数以备将来需要)。
func splitByBytesAndLines(content, language string, startLine int, symbolName, kind string, cfg Config) []Piece {
	maxBytes := cfg.MaxChunkBytes
	if maxBytes <= 0 {
		maxBytes = 8 * 1024
	}
	maxLines := cfg.FallbackWindowLines
	if maxLines <= 0 {
		maxLines = 60
	}

	lines := strings.Split(content, "\n")
	var out []Piece
	var buf strings.Builder
	bufStart := 0   // 当前 chunk 起始行(相对 lines 切片,0-based)
	linesInBuf := 0

	flush := func(endExclusive int) {
		text := strings.TrimRight(buf.String(), "\n")
		buf.Reset()
		linesInBuf = 0
		if strings.TrimSpace(text) == "" {
			return
		}
		out = append(out, Piece{
			Kind:       kind,
			SymbolName: symbolName,
			Content:    text,
			LineStart:  startLine + bufStart,
			LineEnd:    startLine + endExclusive - 1,
			TokenCount: estimateTokens(text),
			Language:   language,
		})
	}

	for i, line := range lines {
		lineWithNL := line + "\n"
		// 当前 buf 非空 + 加这行会溢字节 → 先 flush 旧 buf,从这行另起
		if buf.Len() > 0 && buf.Len()+len(lineWithNL) > maxBytes {
			flush(i)
			bufStart = i
		}
		buf.WriteString(lineWithNL)
		linesInBuf++
		// 行数上限到 → flush 含这行
		if linesInBuf >= maxLines {
			flush(i + 1)
			bufStart = i + 1
		}
	}
	flush(len(lines))
	return out
}
