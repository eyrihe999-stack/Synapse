// filter.go 文件树过滤规则。决定 adapter Sync 哪些路径要 skip,避免把锁文件、二进制、构建产物
// 塞进知识库。独立成文件方便单测和后续加规则。
//
// 两层过滤:
//
//  1. **路径层**(ShouldSkipPath):在 ListTree 结果上判断,不用 Fetch 就能过滤大部分噪音。
//     覆盖"常见垃圾目录 + 二进制/锁文件扩展名"。
//  2. **大小层**(MaxFileBytes 常量):Fetch 后发现 body 超过 2MB 就丢。由 adapter.Fetch 返
//     ErrFileTooLarge,runner skip 该文件。
//
// 规则硬编码而非 config 化 —— 初期保持简单,等实际跑出痛点再按需抽 config。
package gitlab

import (
	"errors"
	"strings"
)

// MaxFileBytes 单文件上限 2MB。超过的 adapter.Fetch 返 ErrFileTooLarge。
// 选值理由见 2026-04-18 session:代码文件超 1MB 基本是生成物,文档 2MB 够绝大多数长 handbook。
const MaxFileBytes int64 = 2 * 1024 * 1024

// ErrFileTooLarge Fetch 时文件超过 MaxFileBytes。runner 应作为单文件 skip 处理,不算失败。
var ErrFileTooLarge = errors.New("gitlab: file exceeds size limit")

// skipDirs 路径中任一段命中这个集合就 skip 整个文件。
// 都是"公认的构建/依赖产物目录",误杀成本极低。
// 大小写敏感 —— Git 路径本身大小写敏感,所以这里也严格匹配。
var skipDirs = map[string]struct{}{
	".git":             {},
	".svn":             {},
	".hg":              {},
	"node_modules":     {},
	"bower_components": {},
	"vendor":           {}, // go/ruby/php 的依赖目录,实战中会塞几百 MB 第三方源
	"dist":             {},
	"build":            {},
	"out":              {},
	"target":           {}, // rust / java
	".venv":            {},
	"venv":             {},
	"env":              {}, // python 虚拟环境约定名
	"__pycache__":      {},
	".idea":            {},
	".vscode":          {},
	".next":            {},
	".nuxt":            {},
	".cache":           {},
	".gradle":          {},
	"coverage":         {},
	".terraform":       {},
}

// skipExtensions 扩展名黑名单(带点,小写比较)。命中就 skip。
// 分组整理:锁文件 / 二进制产物 / 图片字体 / 压缩归档 / 媒体 / 数据库 / Office 文档(暂不解析)
var skipExtensions = map[string]struct{}{
	// 锁文件和 sourcemap —— 语义冗余(描述在 package.json / Cargo.toml 里)
	".lock": {},
	".map":  {},

	// 编译 / 压缩产物
	".exe":    {},
	".dll":    {},
	".so":     {},
	".dylib":  {},
	".class":  {},
	".jar":    {},
	".war":    {},
	".pyc":    {},
	".pyo":    {},
	".o":      {},
	".a":      {},
	".min.js": {}, // 只能匹配精确扩展名,".min.js" 会被 extOf 规则取最后一段 ".js" —— 见下方 extOf 注释

	// 图片 / 字体
	".png":   {},
	".jpg":   {},
	".jpeg":  {},
	".gif":   {},
	".webp":  {},
	".ico":   {},
	".bmp":   {},
	".tiff":  {},
	".svg":   {}, // SVG 是 XML 文本但基本是 asset,索引无价值
	".woff":  {},
	".woff2": {},
	".ttf":   {},
	".otf":   {},
	".eot":   {},

	// 压缩 / 归档
	".zip": {},
	".tar": {},
	".gz":  {},
	".bz2": {},
	".xz":  {},
	".7z":  {},
	".rar": {},
	".deb": {},
	".rpm": {},

	// 媒体
	".mp4": {},
	".mov": {},
	".avi": {},
	".mkv": {},
	".mp3": {},
	".wav": {},
	".flac": {},

	// 数据库文件
	".db":      {},
	".sqlite":  {},
	".sqlite3": {},

	// Office 文档(内容是压缩后的 XML,MVP 不解析)
	".doc":  {},
	".docx": {},
	".xls":  {},
	".xlsx": {},
	".ppt":  {},
	".pptx": {},
	".pdf":  {},
}

// skipBaseNames 精确匹配的文件名黑名单。某些锁文件/产物不走扩展名模式(如 go.sum、Gemfile.lock)
// —— Gemfile.lock 的扩展名是 ".lock" 会被 skipExtensions 命中,但 go.sum 是 ".sum" 需要精确匹配。
var skipBaseNames = map[string]struct{}{
	"go.sum":             {},
	"package-lock.json":  {},
	"yarn.lock":          {}, // .lock 也能命中,双保险
	"pnpm-lock.yaml":     {}, // .yaml 不 skip,这里精确挡
	"composer.lock":      {},
	"Cargo.lock":         {},
	"poetry.lock":        {},
	"Gemfile.lock":       {},
	"gradle.lockfile":    {},
	".DS_Store":          {},
	"Thumbs.db":          {},
}

// ShouldSkipPath 判断一个文件路径(tree entry 的 path 字段,已是相对 repo root 的完整路径)
// 是否要跳过。返 (skip bool, reason string),reason 在 log 里用能让运维知道是哪条规则命中。
//
// 匹配顺序:目录段 → 基本名 → 扩展名。任一命中即返 skip=true。
func ShouldSkipPath(path string) (bool, string) {
	if path == "" {
		return true, "empty path"
	}

	// 1. 路径切段,任一段是黑名单目录就 skip。用 "/" 而非 filepath.Separator —— GitLab 始终用 "/"。
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		if _, ok := skipDirs[seg]; ok {
			return true, "skip dir: " + seg
		}
	}

	// 2. basename 精确匹配
	base := segments[len(segments)-1]
	if _, ok := skipBaseNames[base]; ok {
		return true, "skip basename: " + base
	}

	// 3. 扩展名(取最后一个点,转小写)
	if ext := extOf(base); ext != "" {
		if _, ok := skipExtensions[ext]; ok {
			return true, "skip ext: " + ext
		}
	}

	return false, ""
}

// extOf 取 basename 的小写扩展名(含点)。无扩展名返空串。
//
// 只取最后一个点之后的部分 —— "foo.min.js" 返 ".js",不返 ".min.js"。
// 这是有意的:".min.js" 这种"复合扩展名"在 skipExtensions 里登记了但靠这里匹配不到,
// 我们靠 skipBaseNames 精确文件名 / 或接受误判为普通 .js 索引(.min.js 通常很大会被 MaxFileBytes 兜底)。
func extOf(base string) string {
	idx := strings.LastIndex(base, ".")
	if idx < 0 || idx == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[idx:])
}
