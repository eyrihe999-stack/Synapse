// Package tokenizer 中英文分词,给 BM25 全文检索的 tsvector 通路喂"可搜索的词序列"。
//
// 为什么要预分词:PG 内置的 text search 对 CJK 没有开箱策略 —— `to_tsvector('chinese', ...)` 不存在,
// `to_tsvector('simple', '支付模块')` 会把整串当成一个 token,查询命中率崩溃。通行做法是:
//   - Go 侧分词:'支付模块' → '支付 模块',用空格隔开每个词
//   - DB 侧用 'simple' 配置建 tsvector(只做 lowercase + whitespace split,不做 stem)
// 这样 PG 看到的是"已分好的词流",能按词条粒度建倒排 + ts_rank 打分。
//
// 实现:github.com/go-ego/gse —— 纯 Go,自带中英文词典,无 CGO 依赖(Docker 构建 / 交叉编译友好)。
// 文件名 / 路径 / 代码符号这类 Latin 混合串 gse 也能处理好(不会被中英边界错切)。
package tokenizer

import (
	"strings"

	"github.com/go-ego/gse"
)

// Tokenizer 分词器接口。上层(index_pipeline / search_service)只依赖接口,不关心具体实现,
// 方便将来换更准的词典(gse + 自定义词典,或切换到 jieba CGO 版)或在测试里注入 stub。
type Tokenizer interface {
	// Tokenize 把 text 切成词序列。空输入返 nil,多空白输入返 nil(保持"没内容就没词"语义)。
	// 相同输入多次调用应返回相同结果(无状态)。
	Tokenize(text string) []string

	// TokensString 等价 `strings.Join(Tokenize(text), " ")`,给 DB 写入 / 查询构造方便用。
	// 提供 shortcut 是为了让 caller 不必每次都拼 slice — 这是最高频的使用场景。
	TokensString(text string) string
}

// NewGse 构造默认 gse 分词器,加载内置简体中文 + 英文词典。
//
// gse.New() 不传参:加载内置的 data/dict/dictionary.txt(简体中文主词典)+ 英文支持。
// 企业语料里偶尔出现的繁体 / 日文会 fallback 成单字串,不是最优但可接受;
// 需要繁体加载 gse.New("zh_t") 或补自定义词典。
//
// 返回接口类型而不是具体结构:让调用方对 Tokenizer 抽象编程,后续替换实现时无感。
func NewGse() (Tokenizer, error) {
	seg, err := gse.New()
	if err != nil {
		return nil, err
	}
	// 静默掉 gse 启动时的字典加载 log —— 它直接走 Go stdlib log,会污染我们自己的结构化日志输出。
	// 生产运行 / 测试都不需要看这几行。
	seg.SkipLog = true
	return &gseTokenizer{seg: &seg}, nil
}

type gseTokenizer struct {
	seg *gse.Segmenter
}

// Tokenize gse.Cut 做基础分词。空输入 / 全空白输入短路返 nil,避免下游产出 "" token。
func (g *gseTokenizer) Tokenize(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	raw := g.seg.Cut(text)
	// 过滤掉纯空白 token(gse 有时会给 "\t" / " " 这种边界产物)。
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TokensString 用单空格连接,契合 PG 'simple' 配置对 tsvector 输入的期望(空格分词)。
func (g *gseTokenizer) TokensString(text string) string {
	return strings.Join(g.Tokenize(text), " ")
}
