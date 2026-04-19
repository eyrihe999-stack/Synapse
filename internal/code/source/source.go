// Package source 代码仓库数据源抽象。
//
// 每个 provider(gitlab / 未来 github / gitea...)实现一遍 Source,返归一化的 RepoSnapshot +
// FileEntry + FileContent。ingest service 只依赖本包接口,不关心具体 provider。
//
// 和 pkg/sourceadapter/gitlab 的区别:
//   - pkg/sourceadapter/gitlab 是"纯 GitLab HTTP 客户端",通用,不含 Synapse 业务
//   - source 层是 code 模块的"数据源适配器",把 HTTP client 编排成 ingest 需要的形态
//     (过滤规则、分页循环、大小限额等都在这里应用)
package source

import (
	"context"
	"errors"
)

// RepoSnapshot 一次同步时对单个 repo 的描述。
//
// ExternalProjectID 是 provider 侧的唯一 ID,转成字符串存(见 code.CodeRepository.ExternalProjectID
// 注释)。空 repo(no commits, DefaultBranch="")在 ListRepos 阶段就会被过滤掉,
// ingest 看到的 RepoSnapshot 保证 DefaultBranch 非空。
type RepoSnapshot struct {
	ExternalProjectID string
	PathWithNamespace string
	DefaultBranch     string
	WebURL            string
}

// FileEntry tree entry 过滤后的结果。只保留 ingest 需要的定位信息。
//
// BlobSHA 是 provider 原生 blob hash(GitLab git blob sha1)—— ingest 层按它判断"文件内容是否变化"
// 和做 CAS 去重;非空保证,空 BlobSHA 的 entry 会被 source 内部过滤掉。
type FileEntry struct {
	Path    string
	BlobSHA string
}

// FileContent FetchFile 的返回。Content 是原始字节,SizeBytes 冗余存方便上层不重算 len。
type FileContent struct {
	Content      []byte
	BlobSHA      string
	LastCommitID string
	SizeBytes    int64
}

// Source 代码数据源抽象。具体实现生命周期:每次 sync 一个用户就构造一个(带 user 的 token),
// 不跨用户复用。
type Source interface {
	// Provider 返 code.ProviderXxx 常量。ingest service 按此写 code_repositories.provider 列。
	Provider() string

	// ListRepos 列 user 可见的 **活跃** repo(archived 跳过、空 repo 跳过)。
	// 返回的 RepoSnapshot 顺序由 provider 决定(通常按 ID 升序),不承诺稳定性;
	// 上层按 ExternalProjectID upsert,不依赖顺序。
	//
	// PAT 无效 → 返封装后的错误(调用方按 errors.Is(err, ErrInvalidToken) 判);
	// 网络错/服务端错 → 直接返,ingest 整轮失败。
	ListRepos(ctx context.Context) ([]RepoSnapshot, error)

	// ListFiles 列 repo 当前 DefaultBranch 下的全部文件。**已经按 filter 过滤**掉
	// 黑名单目录 / 扩展名(如 node_modules / 二进制 / 锁文件),调用方拿到就是要 ingest 的候选集。
	//
	// 大小过滤不在此发生 —— tree API 不返文件大小,超限判断要等 FetchFile 里才能做。
	//
	// repo 在调用期间被删或权限撤销 → ErrRepoUnavailable(ingest 应 skip 该 repo 继续别的,
	// 不让一个被删 repo 整体拖垮 sync)。
	ListFiles(ctx context.Context, repo RepoSnapshot) ([]FileEntry, error)

	// FetchFile 拉单文件内容。超过大小上限 → ErrFileTooLarge(ingest 作单文件 skip,不算失败)。
	// repo / 文件在本次调用前夕消失(极少见的并发窗口)→ ErrFileGone,同样作单文件 skip 处理。
	FetchFile(ctx context.Context, repo RepoSnapshot, entry FileEntry) (*FileContent, error)
}

// ─── Sentinel 错误 ───────────────────────────────────────────────────────────

// ErrInvalidToken PAT 被 provider 拒绝。ingest 应立刻终止整轮同步,返用户"请重贴 token"。
var ErrInvalidToken = errors.New("code source: invalid or revoked token")

// ErrRepoUnavailable 单个 repo 不可访问(被删 / 权限撤销 / 临时 500)。
// ingest 应 skip 该 repo 继续处理其他 repo,不让整轮同步失败。
var ErrRepoUnavailable = errors.New("code source: repo not accessible")

// ErrFileTooLarge 单文件超过大小上限(source 层按 provider 的 MaxFileBytes 判)。
// ingest 应 skip 该文件,不算失败。
var ErrFileTooLarge = errors.New("code source: file exceeds size limit")

// ErrFileGone 文件在 FetchFile 时已不存在(tree → fetch 中间窗口被删)。
// ingest 应 skip 该文件,不算失败。
var ErrFileGone = errors.New("code source: file no longer exists")
