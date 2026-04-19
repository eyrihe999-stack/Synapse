// gitlab.go GitLab 作为 CodeSource 的实现。
//
// 依赖:pkg/sourceadapter/gitlab 的 ClientAPI(已抽象好的 HTTP 层)+ 文件过滤规则(ShouldSkipPath
// + MaxFileBytes + ErrFileTooLarge)。本文件负责:
//   - 分页循环拼接全集
//   - GitLab 特定错误映射到 Source 层 sentinel(ErrInvalidToken / ErrRepoUnavailable / ErrFileGone)
//   - tree entry → FileEntry 的规范化 + 应用 path 过滤
package source

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/eyrihe999-stack/Synapse/internal/code"
	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter/gitlab"
)

// gitlabPerPage GitLab API 单页上限 100。全量 list 按此值拉。
const gitlabPerPage = 100

// GitLabSource 单个 PAT 对应一个实例。不跨用户复用。
type GitLabSource struct {
	client gitlab.ClientAPI
	token  string
}

// NewGitLabSource 构造。client / token 都必须非零。
// client 按 per-org 的 BaseURL 由调用方预先构造(gitlab.NewClient)。
func NewGitLabSource(client gitlab.ClientAPI, token string) *GitLabSource {
	return &GitLabSource{client: client, token: token}
}

// Provider 见接口。
func (s *GitLabSource) Provider() string { return code.ProviderGitLab }

// ListRepos 循环拉全部 pages 直到 NextPage=0。
//
// 过滤规则:
//   - Archived=true 跳过(归档 repo 不再接新代码,同步无意义)
//   - DefaultBranch="" 跳过(空 repo,ListTree 会报错)
func (s *GitLabSource) ListRepos(ctx context.Context) ([]RepoSnapshot, error) {
	var out []RepoSnapshot
	page := 1
	for {
		resp, err := s.client.ListProjects(ctx, s.token, page, gitlabPerPage)
		if err != nil {
			if errors.Is(err, gitlab.ErrInvalidToken) {
				return nil, ErrInvalidToken
			}
			return nil, fmt.Errorf("gitlab source: list projects page %d: %w", page, err)
		}
		for _, p := range resp.Projects {
			if p.Archived || p.DefaultBranch == "" {
				continue
			}
			out = append(out, RepoSnapshot{
				ExternalProjectID: strconv.FormatInt(p.ID, 10),
				PathWithNamespace: p.PathWithNamespace,
				DefaultBranch:     p.DefaultBranch,
				WebURL:            p.WebURL,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return out, nil
}

// ListFiles 循环拉 repo tree 并过滤。
//
// 过滤:
//   - 只保留 Type=="blob"(忽略 tree 子目录元信息 + submodule 的 "commit" type)
//   - 应用 gitlab.ShouldSkipPath(路径黑名单 + 扩展名黑名单 + 锁文件名)
//   - BlobSHA 空的条目 skip(不正常但防御性过滤)
func (s *GitLabSource) ListFiles(ctx context.Context, repo RepoSnapshot) ([]FileEntry, error) {
	pid, err := strconv.ParseInt(repo.ExternalProjectID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("gitlab source: invalid project id %q: %w", repo.ExternalProjectID, err)
	}
	var out []FileEntry
	page := 1
	for {
		resp, err := s.client.ListTree(ctx, s.token, pid, repo.DefaultBranch, "", page, gitlabPerPage)
		if err != nil {
			if errors.Is(err, gitlab.ErrInvalidToken) {
				return nil, ErrInvalidToken
			}
			if errors.Is(err, gitlab.ErrProjectNotFound) {
				// repo 在调用期间被删 / 权限撤销。skip 该 repo,让 ingest 继续别的。
				return nil, ErrRepoUnavailable
			}
			return nil, fmt.Errorf("gitlab source: list tree project=%d page=%d: %w", pid, page, err)
		}
		for _, e := range resp.Entries {
			if e.Type != "blob" || e.ID == "" {
				continue
			}
			if skip, _ := gitlab.ShouldSkipPath(e.Path); skip {
				continue
			}
			out = append(out, FileEntry{
				Path:    e.Path,
				BlobSHA: e.ID,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return out, nil
}

// FetchFile 拉文件内容。应用 MaxFileBytes 大小上限。
//
// GitLab raw endpoint 已经在 header 里带了 blob_sha / last_commit_id,这里直接搬过来。
// 理论上 header 里的 blob_sha 应该和 tree 阶段拿到的 BlobSHA 一致,若不一致说明
// 分支 HEAD 在 list tree → fetch 之间变过了 —— 以 header 值为准(反映当前实际拉到的内容)。
func (s *GitLabSource) FetchFile(ctx context.Context, repo RepoSnapshot, entry FileEntry) (*FileContent, error) {
	pid, err := strconv.ParseInt(repo.ExternalProjectID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("gitlab source: invalid project id: %w", err)
	}
	fc, err := s.client.GetRawFile(ctx, s.token, pid, repo.DefaultBranch, entry.Path)
	if err != nil {
		if errors.Is(err, gitlab.ErrInvalidToken) {
			return nil, ErrInvalidToken
		}
		if errors.Is(err, gitlab.ErrFileNotFound) {
			// tree → fetch 窗口里文件被删,或 repo 分支 head 变了
			return nil, ErrFileGone
		}
		return nil, fmt.Errorf("gitlab source: fetch %s@%s: %w", entry.Path, repo.DefaultBranch, err)
	}
	size := int64(len(fc.Content))
	if size > gitlab.MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	// header 里的 BlobSHA 为空时,回退用 tree 阶段的 entry.BlobSHA。
	// 大多数情况下 header 会填,这是防御性兜底。
	blobSHA := fc.BlobSHA
	if blobSHA == "" {
		blobSHA = entry.BlobSHA
	}
	return &FileContent{
		Content:      fc.Content,
		BlobSHA:      blobSHA,
		LastCommitID: fc.CommitID,
		SizeBytes:    size,
	}, nil
}
