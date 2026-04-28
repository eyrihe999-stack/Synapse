// Package gitlab 是"GitLab 仓库全量同步"的 Fetcher 实现。
//
// 一次 ingest = 一个 Fetcher 实例,绑定 (org, source, branch, commit, GitLab client)。
// Fetch 走两步:
//
//	1. ListTreeRecursive(ref=branch) 拿全量 blob 列表
//	2. 对每个白名单内、≤ MaxGitLabFileBytes 的 blob:GetFileRaw → 构造 NormalizedDoc → emit
//
// 失败语义遵循 ingestion.Fetcher:单文件 401/404/text-binary 跳过 + log;整体 401/网络/ctx 取消 → 上抛。
package gitlab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	gitlabclient "github.com/eyrihe999-stack/Synapse/internal/integration/gitlab"
	"github.com/eyrihe999-stack/Synapse/internal/ingestion"
	"github.com/eyrihe999-stack/Synapse/internal/source"
)

// Input fetcher 构造参数。
type Input struct {
	OrgID             uint64
	UploaderID        uint64 // 同步任务发起人 = source.owner_user_id
	KnowledgeSourceID uint64 // sources.id
	ProjectID         string // GitLab project id
	PathWithNamespace string // 用于拼 ExternalRef.URI
	Branch            string // 同步分支(refs 名)
	CommitSHA         string // 解析后的具体 commit sha,documents.version 写它
	WebBaseURL        string // GitLab UI 根 URL,用于拼 blob/commit 链接

	// ChangedFiles 增量模式专用:仅拉这些文件路径(rel-to-repo-root),不调 ListTreeRecursive。
	// nil / 空 → 走全量(ListTreeRecursive)。runner 决定模式后传进来。
	// 注意:本字段只表示"应当被 fetch 的文件";removed 文件由 runner 在 fetcher 之外处理(直接删 doc),
	// fetcher 不接收 removed 列表。
	ChangedFiles []string
}

// Fetcher 见 ingestion.Fetcher。
type Fetcher struct {
	in  Input
	cli *gitlabclient.Client
	log logger.LoggerInterface
}

// New 构造。任一关键字段缺失 → Fetch 时返 error,不在构造期校验(让调用方语义松)。
func New(in Input, cli *gitlabclient.Client, log logger.LoggerInterface) *Fetcher {
	return &Fetcher{in: in, cli: cli, log: log}
}

// SourceType 走通用 document(一期不分代码 chunker;后续 PR-B 接 tree-sitter 时切到 SourceTypeCode)。
func (f *Fetcher) SourceType() string { return ingestion.SourceTypeDocument }

// Fetch 见 ingestion.Fetcher。
//
// 错误返还:
//   - 整体 ListTreeRecursive 失败 / 凭据失效 → 直接上抛(pipeline 标整轮失败)
//   - 单文件错(404 / 二进制 / 超限)→ skip + warn,不影响其他文件
//   - emit 上游返 error → 立刻停止 + 上抛(pipeline 已经知道了)
//
//nolint:funlen,gocyclo // 串行流程,拆分会拉长
func (f *Fetcher) Fetch(ctx context.Context, emit ingestion.Emit) error {
	if f.cli == nil {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("gitlab fetcher: nil client")
	}
	if f.in.ProjectID == "" || f.in.Branch == "" || f.in.CommitSHA == "" {
		//sayso-lint:ignore log-coverage
		return fmt.Errorf("gitlab fetcher: project_id/branch/commit_sha required")
	}

	// 决定本轮要 fetch 的文件路径列表:增量模式直接用 ChangedFiles;否则全量 ListTreeRecursive。
	var paths []string
	if len(f.in.ChangedFiles) > 0 {
		paths = f.in.ChangedFiles
		f.log.InfoCtx(ctx, "gitlab fetcher: incremental mode", map[string]any{
			"project_id": f.in.ProjectID, "branch": f.in.Branch, "file_count": len(paths),
		})
	} else {
		tree, err := f.cli.ListTreeRecursive(ctx, f.in.ProjectID, f.in.Branch)
		if err != nil {
			//sayso-lint:ignore sentinel-wrap
			return err
		}
		f.log.InfoCtx(ctx, "gitlab fetcher: full mode tree listed", map[string]any{
			"project_id": f.in.ProjectID, "branch": f.in.Branch, "node_count": len(tree),
		})
		paths = make([]string, 0, len(tree))
		for _, node := range tree {
			if node.Type != "blob" {
				continue
			}
			paths = append(paths, node.Path)
		}
	}

	emitted := 0
	skipped := 0
	for _, p := range paths {
		if ctx.Err() != nil {
			//sayso-lint:ignore sentinel-wrap
			return ctx.Err()
		}
		if !isTextLikeByExt(p) {
			skipped++
			continue
		}
		raw, err := f.cli.GetFileRaw(ctx, f.in.ProjectID, p, f.in.CommitSHA)
		if err != nil {
			// 单文件 404/超限/二进制 → skip,记 warn 不上抛
			if errors.Is(err, source.ErrSourceGitLabRepoNotFound) {
				f.log.WarnCtx(ctx, "gitlab fetcher: file gone (404), skip", map[string]any{
					"project_id": f.in.ProjectID, "path": p,
				})
				skipped++
				continue
			}
			if errors.Is(err, source.ErrSourceGitLabUpstream) && strings.Contains(err.Error(), "exceeds") {
				f.log.WarnCtx(ctx, "gitlab fetcher: file too large, skip", map[string]any{
					"project_id": f.in.ProjectID, "path": p, "max": source.MaxGitLabFileBytes,
				})
				skipped++
				continue
			}
			// 401 / 网络 / 5xx → 致命,整轮失败
			//sayso-lint:ignore sentinel-wrap
			return err
		}
		if !looksLikeText(raw) {
			f.log.DebugCtx(ctx, "gitlab fetcher: non-text content, skip", map[string]any{
				"project_id": f.in.ProjectID, "path": p, "byte_size": len(raw),
			})
			skipped++
			continue
		}

		doc := f.buildDoc(p, raw)
		if err := emit(ctx, doc); err != nil {
			// pipeline 已经记日志,这里直接上抛
			//sayso-lint:ignore sentinel-wrap
			return err
		}
		emitted++
	}

	f.log.InfoCtx(ctx, "gitlab fetcher: done", map[string]any{
		"project_id": f.in.ProjectID, "emitted": emitted, "skipped": skipped, "total_paths": len(paths),
	})
	return nil
}

// buildDoc NormalizedDoc 构造。SourceID 命名约定:"gitlab:<project_id>:<path>"
// (和 ingestion/normalized.go 注释里的写法一致),给 persister 的 (org, source_type, source_id) upsert 用。
func (f *Fetcher) buildDoc(filePath string, content []byte) *ingestion.NormalizedDoc {
	sum := sha256.Sum256(content)
	contentHash := hex.EncodeToString(sum[:])

	title := path.Base(filePath)
	mimeType := mimeFromExt(filePath)

	blobURL := ""
	if f.in.WebBaseURL != "" {
		// path 不进行 url-encode — GitLab UI 接受原 path 形式;空格等特殊字符场景一期容忍
		blobURL = strings.TrimRight(f.in.WebBaseURL, "/") + "/" + f.in.PathWithNamespace + "/-/blob/" + f.in.CommitSHA + "/" + filePath
	}

	return &ingestion.NormalizedDoc{
		OrgID:             f.in.OrgID,
		SourceType:        ingestion.SourceTypeDocument,
		SourceID:          fmt.Sprintf("gitlab:%s:%s", f.in.ProjectID, filePath),
		Version:           contentHash,
		Title:             title,
		MIMEType:          mimeType,
		FileName:          filePath,
		Content:           content,
		// Language 按扩展名映射 — 已注册 backend 的语言走 code chunker;
		// 没注册 → ChunkerSelector 自动降级 plaintext / markdown(都基于 MIMEType 路由)。
		Language:          languageFromExt(filePath),
		UploaderID:        f.in.UploaderID,
		KnowledgeSourceID: f.in.KnowledgeSourceID,
		ExternalRef: ingestion.ExternalRef{
			Kind:   "git",
			URI:    blobURL,
			Repo:   f.in.PathWithNamespace,
			Path:   filePath,
			Commit: f.in.CommitSHA,
		},
		// Payload 不填:upload 那边的 docpersister.Payload 是 upload 专用,GitLab 走 document 通道
		// 但不依赖 Payload —— persister 对 nil Payload 容忍(走 provider="" 默认分支)。
	}
}

// languageFromExt 文件扩展名 → Language tag(对应 code chunker 的 backend Language)。
//
// 一期只 Go 有 backend 注册;其他扩展名映射先留着,等 PR-B2 接 tree-sitter 后即可生效,
// 现阶段返这些 tag 没问题:ChunkerSelector 通过 codeCk.Supports(language) 兜底降级 plaintext。
//
// 不识别的扩展返空字符串 → ChunkerSelector 走 plaintext / markdown。
func languageFromExt(filePath string) string {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".rb":
		return "ruby"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp":
		return "cpp"
	case ".swift":
		return "swift"
	case ".scala":
		return "scala"
	case ".php":
		return "php"
	}
	return ""
}

// ─── 文件类型判断 ──────────────────────────────────────────────────────────

// textLikeExtensions 一期保守白名单。tree-sitter 接入(PR-B)前先用扩展名兜底过滤,
// 避免把图片 / pdf / 压缩包当文本喂进 chunker 烧 embed token。
var textLikeExtensions = map[string]string{
	// 源代码
	".go":    "text/x-go",
	".py":    "text/x-python",
	".rb":    "text/x-ruby",
	".rs":    "text/x-rust",
	".c":     "text/x-c",
	".cc":    "text/x-c++src",
	".cpp":   "text/x-c++src",
	".h":     "text/x-c-header",
	".hpp":   "text/x-c++hdr",
	".java":  "text/x-java-source",
	".kt":    "text/x-kotlin",
	".swift": "text/x-swift",
	".js":    "text/javascript",
	".jsx":   "text/javascript",
	".ts":    "application/typescript",
	".tsx":   "application/typescript",
	".vue":   "text/x-vue",
	".php":   "text/x-php",
	".scala": "text/x-scala",
	".sh":    "text/x-shellscript",
	".bash":  "text/x-shellscript",
	".zsh":   "text/x-shellscript",
	".sql":   "application/sql",
	// 配置 / 数据格式(纯文本)
	".yaml":       "application/yaml",
	".yml":        "application/yaml",
	".json":       "application/json",
	".toml":       "application/toml",
	".xml":        "application/xml",
	".ini":        "text/plain",
	".conf":       "text/plain",
	".properties": "text/plain",
	".env":        "text/plain",
	// 文档
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".mdx":      "text/markdown",
	".rst":      "text/x-rst",
	".txt":      "text/plain",
	// 构建脚本
	".dockerfile": "text/x-dockerfile",
	".mk":         "text/x-makefile",
	".gradle":     "text/x-groovy",
}

// noExtAllowedNames 没有扩展名但内容是文本的常见文件(全部小写比较)。
var noExtAllowedNames = map[string]string{
	"dockerfile":  "text/x-dockerfile",
	"makefile":    "text/x-makefile",
	"jenkinsfile": "text/x-groovy",
	"readme":      "text/plain",
	"license":     "text/plain",
	"changelog":   "text/plain",
}

// isTextLikeByExt 仅按文件名判断"看起来是文本"。
//
// 二阶段过滤(looksLikeText)再按字节做 NUL 字符检测;两步合作覆盖"扩展名说是文本但实际是
// minified 二进制混入"和"无扩展名脚本"两种 case。
func isTextLikeByExt(filePath string) bool {
	base := strings.ToLower(path.Base(filePath))
	if _, ok := noExtAllowedNames[base]; ok {
		return true
	}
	ext := strings.ToLower(path.Ext(base))
	_, ok := textLikeExtensions[ext]
	return ok
}

// mimeFromExt 给 chunker selector 路由用。空 string → caller 走默认分支(plaintext)。
func mimeFromExt(filePath string) string {
	base := strings.ToLower(path.Base(filePath))
	if mt, ok := noExtAllowedNames[base]; ok {
		return mt
	}
	ext := strings.ToLower(path.Ext(base))
	return textLikeExtensions[ext]
}

// looksLikeText 简易文本探测:
//   - 空文件视为文本(让 persister 用空内容写 doc 元数据 + 0 chunk,保持 git 状态镜像一致)
//   - 含 NUL 字节(\x00) → 二进制
//   - 否则按 utf8 校验:连续非法序列 > 阈值视为非文本
//
// 不引第三方 mimetype 包:这里需求小,内联省依赖。
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.IndexByte(b, 0x00) >= 0 {
		return false
	}
	// 抽样前 8KB 检测 — 足够区分二进制
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if !utf8.Valid(sample) {
		return false
	}
	return true
}

