// adapter.go 飞书 SourceAdapter 主体:实现 sourceadapter.Adapter 接口。
//
// 每个 Adapter 实例对应一个已授权的飞书 user_access_token(通过 refresh_token 持久化)。
// 扫描范围 = 该用户可见的所有 drive + wiki 文件。
//
// Sync 策略:
//   1. 从 Config.RootFolderToken 起递归 ListDriveFiles,过滤 modified_time > since 的文件
//   2. 遍历 Config.WikiSpaceIDs(空 = 用户所有 wiki)的每个空间,DFS ListWikiNodes
//   3. MVP 只为 docx 类型产 Change,其他类型 log + skip
//
// Fetch 策略:
//   - 按 ref.type 分派:docx → GetDocxMetadata + GetDocxBlocks → BlocksToMarkdown
//   - 其他类型暂返 ErrUnsupportedType,后续按需扩
//
// 语义边界:
//   - Change.ContentHash 不填 —— 飞书 API 不暴露文件哈希,只给 modified_time。下游 pipeline
//     自己对 Fetch 结果做 sha256 dedup(和 HTTP 上传路径一致)。
//   - ChangeDelete 识别:飞书当前未在 list 接口返"已删"条目,webhook(drive.file.deleted_v1)
//     才有信号。本 MVP Sync 只产 Create/Update,Delete 留给未来 webhook adapter 分支处理。
package feishu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter"
)

// ErrUnsupportedType Fetch 到 MVP 不支持的文件类型(sheet/bitable/mindnote 等)。
// 上层编排应 skip 这条 Change、log 下,不算 adapter 整体失败。
var ErrUnsupportedType = errors.New("feishu: unsupported file type")

// Adapter 飞书云文档适配器。字段不可变,构造后并发安全。
type Adapter struct {
	cfg    Config
	client ClientAPI
}

// NewAdapter 按 user 粒度构造 Adapter。每个用户独立实例。
//
// 失败场景:Config 无效 / refresh_token 空。网络错和 token 刷新错延迟到首次调用时报。
func NewAdapter(cfg Config, refreshToken string) (*Adapter, error) {
	client, err := NewClient(cfg, refreshToken)
	if err != nil {
		return nil, err
	}
	return &Adapter{cfg: cfg, client: client}, nil
}

// NewAdapterWithClient 给测试用 —— 注入 fake ClientAPI 跳过真 HTTP。
// 生产路径永远走 NewAdapter。
func NewAdapterWithClient(cfg Config, client ClientAPI) *Adapter {
	return &Adapter{cfg: cfg, client: client}
}

// Type 见 sourceadapter.Adapter。
func (a *Adapter) Type() string { return AdapterType }

// Sync 扫描 user 可见的文档 & wiki,返 since 之后有变更的条目。
//
// orgID 参数目前只用于日志 —— 飞书侧无 org 概念,user 的归属 org 由调用方(Synapse)决定。
func (a *Adapter) Sync(ctx context.Context, orgID uint64, since time.Time) ([]sourceadapter.Change, error) {
	var changes []sourceadapter.Change

	// 第一路:Drive 文件。空 folderToken = 用户云空间根。
	driveChanges, err := a.syncDrive(ctx, a.cfg.RootFolderToken, since)
	if err != nil {
		return nil, fmt.Errorf("feishu sync drive: %w", err)
	}
	changes = append(changes, driveChanges...)

	// 第二路:Wiki 空间。Config 指定了就只扫那些,否则遍历用户所有 wiki。
	spaceIDs := a.cfg.WikiSpaceIDs
	if len(spaceIDs) == 0 {
		allSpaces, err := a.listAllWikiSpaces(ctx)
		if err != nil {
			return nil, fmt.Errorf("feishu list wiki spaces: %w", err)
		}
		spaceIDs = allSpaces
	}
	for _, spaceID := range spaceIDs {
		wikiChanges, err := a.syncWikiSpace(ctx, spaceID, since)
		if err != nil {
			// 单个空间失败 log 下继续别的 —— 部分失败比整体中断更实用。
			// TODO: 加上 logger 依赖后打 warn 级别日志。
			continue
		}
		changes = append(changes, wikiChanges...)
	}

	return changes, nil
}

// syncDrive 递归遍历 drive folder,返过滤后的 Change 列表。DFS,有 folder 时进入子。
func (a *Adapter) syncDrive(ctx context.Context, folderToken string, since time.Time) ([]sourceadapter.Change, error) {
	var changes []sourceadapter.Change
	pageToken := ""
	for {
		page, err := a.client.ListDriveFiles(ctx, folderToken, pageToken)
		if err != nil {
			return nil, err
		}
		for _, f := range page.Files {
			if f.Type == FileTypeFolder {
				// 递归子目录。
				sub, err := a.syncDrive(ctx, f.Token, since)
				if err != nil {
					return nil, err
				}
				changes = append(changes, sub...)
				continue
			}
			// 只收 MVP 支持的类型(docx)。其他先跳过,保留 log 点让运维能看到漏了啥。
			if f.Type != FileTypeDocx {
				continue
			}
			if !modifiedAfter(f.ModifiedTime, since) {
				continue
			}
			ref := &Ref{FileToken: f.Token, Type: FileTypeDocx}
			sref, err := ref.ToSourceRef()
			if err != nil {
				return nil, err
			}
			changes = append(changes, sourceadapter.Change{
				Action: sourceadapter.ChangeUpdate, // 无 Delete 信号,统一 Update(下游按 source_ref 判 create/update)
				Ref:    sref,
			})
		}
		if !page.HasMore || page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return changes, nil
}

// listAllWikiSpaces 翻页拉当前 user 可见的所有 wiki 空间 ID。
func (a *Adapter) listAllWikiSpaces(ctx context.Context) ([]string, error) {
	var ids []string
	pageToken := ""
	for {
		page, err := a.client.ListWikiSpaces(ctx, pageToken)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Spaces {
			ids = append(ids, s.SpaceID)
		}
		if !page.HasMore || page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return ids, nil
}

// syncWikiSpace DFS 一个 wiki 空间,过滤出 modified > since 的 docx 节点。
func (a *Adapter) syncWikiSpace(ctx context.Context, spaceID string, since time.Time) ([]sourceadapter.Change, error) {
	return a.syncWikiNodes(ctx, spaceID, "", since)
}

// syncWikiNodes 递归子节点。wiki API 本身没给 modified_time,只给 node_create_time / origin_node_create_time。
// 真 modified_time 需要对 obj_token 调 docx 元信息 —— 为避免每节点都打一次,MVP 策略:
// list 阶段先全拿,Fetch 阶段再跳过未变的(用户在 documents 表按 source_ref 查 modified_at)。
func (a *Adapter) syncWikiNodes(ctx context.Context, spaceID, parentNodeToken string, _ time.Time) ([]sourceadapter.Change, error) {
	var changes []sourceadapter.Change
	pageToken := ""
	for {
		page, err := a.client.ListWikiNodes(ctx, spaceID, parentNodeToken, pageToken)
		if err != nil {
			return nil, err
		}
		for _, n := range page.Nodes {
			if n.ObjType == FileTypeDocx && n.ObjToken != "" {
				ref := &Ref{FileToken: n.ObjToken, Type: FileTypeDocx, SpaceID: spaceID}
				sref, err := ref.ToSourceRef()
				if err != nil {
					return nil, err
				}
				changes = append(changes, sourceadapter.Change{
					Action: sourceadapter.ChangeUpdate,
					Ref:    sref,
				})
			}
			if n.HasChild {
				sub, err := a.syncWikiNodes(ctx, spaceID, n.NodeToken, time.Time{})
				if err != nil {
					return nil, err
				}
				changes = append(changes, sub...)
			}
		}
		if !page.HasMore || page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return changes, nil
}

// Fetch 按 Ref 拉具体内容。当前只支持 docx,其他类型返 ErrUnsupportedType。
func (a *Adapter) Fetch(ctx context.Context, orgID uint64, src sourceadapter.SourceRef) (*sourceadapter.RawDocument, error) {
	ref, err := RefFromSourceRef(src)
	if err != nil {
		return nil, fmt.Errorf("feishu fetch: %w", err)
	}
	switch ref.Type {
	case FileTypeDocx:
		return a.fetchDocx(ctx, ref)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, ref.Type)
	}
}

// fetchDocx docx 拉取主路径:元信息 + 结构化 blocks → markdown。
func (a *Adapter) fetchDocx(ctx context.Context, ref *Ref) (*sourceadapter.RawDocument, error) {
	meta, err := a.client.GetDocxMetadata(ctx, ref.FileToken)
	if err != nil {
		return nil, fmt.Errorf("feishu docx metadata: %w", err)
	}
	blocks, err := a.client.GetDocxBlocks(ctx, ref.FileToken)
	if err != nil {
		return nil, fmt.Errorf("feishu docx blocks: %w", err)
	}
	md := BlocksToMarkdown(blocks)

	fileName := meta.Title
	if fileName == "" {
		fileName = ref.FileToken
	}
	fileName += ".md"

	return &sourceadapter.RawDocument{
		Title:    meta.Title,
		FileName: fileName,
		MIMEType: "text/markdown",
		Content:  []byte(md),
		Metadata: map[string]any{
			"feishu_document_id":  meta.DocumentID,
			"feishu_revision_id":  meta.RevisionID,
			"feishu_owner_id":     meta.OwnerID,
			"feishu_space_id":     ref.SpaceID, // wiki 节点才有值
			"feishu_modified_at":  meta.ModifiedTime,
		},
	}, nil
}

// modifiedAfter 把飞书的 modified_time(unix seconds 字符串)和 since 比较。
// 飞书有时返空串 / 0 —— 当作"没有修改时间信息",直接放行(保守:宁可重拉不漏)。
func modifiedAfter(modifiedUnix string, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	if modifiedUnix == "" {
		return true
	}
	ts, err := strconv.ParseInt(modifiedUnix, 10, 64)
	if err != nil || ts == 0 {
		return true
	}
	return time.Unix(ts, 0).After(since)
}

// 编译期证明 Adapter 满足接口 —— 接口漂移时这行先编译报错,不让接错形状的实现混进 Registry。
var _ sourceadapter.Adapter = (*Adapter)(nil)
