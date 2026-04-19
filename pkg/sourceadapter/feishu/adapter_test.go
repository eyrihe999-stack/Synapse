package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyrihe999-stack/Synapse/pkg/sourceadapter"
)

// fakeClient 测试用 ClientAPI 实现。按构造时提供的固定返回值工作,调用计数给断言用。
type fakeClient struct {
	driveFiles  map[string]*DriveFilesPage   // folderToken → page
	wikiSpaces  *WikiSpacesPage
	wikiNodes   map[string]*WikiNodesPage // spaceID+parentToken → page
	docxMeta    map[string]*DocxMetadata
	docxBlocks  map[string][]DocxBlock

	listDriveCount int
	fetchDocxCount int
}

func (f *fakeClient) ListDriveFiles(_ context.Context, folderToken, _ string) (*DriveFilesPage, error) {
	f.listDriveCount++
	if p, ok := f.driveFiles[folderToken]; ok {
		return p, nil
	}
	return &DriveFilesPage{}, nil
}

func (f *fakeClient) ListWikiSpaces(_ context.Context, _ string) (*WikiSpacesPage, error) {
	if f.wikiSpaces != nil {
		return f.wikiSpaces, nil
	}
	return &WikiSpacesPage{}, nil
}

func (f *fakeClient) ListWikiNodes(_ context.Context, spaceID, parentToken, _ string) (*WikiNodesPage, error) {
	key := spaceID + "|" + parentToken
	if p, ok := f.wikiNodes[key]; ok {
		return p, nil
	}
	return &WikiNodesPage{}, nil
}

func (f *fakeClient) GetDocxMetadata(_ context.Context, token string) (*DocxMetadata, error) {
	if m, ok := f.docxMeta[token]; ok {
		return m, nil
	}
	return nil, errors.New("fake: metadata not found: " + token)
}

func (f *fakeClient) GetDocxBlocks(_ context.Context, token string) ([]DocxBlock, error) {
	f.fetchDocxCount++
	if b, ok := f.docxBlocks[token]; ok {
		return b, nil
	}
	return nil, errors.New("fake: blocks not found: " + token)
}

// ─── Adapter.Type / 接口合约 ─────────────────────────────────────────────────

func TestAdapter_TypeAndInterface(t *testing.T) {
	a := NewAdapterWithClient(Config{}, &fakeClient{})
	if a.Type() != AdapterType {
		t.Errorf("Type() = %q, want %q", a.Type(), AdapterType)
	}
	// var _ sourceadapter.Adapter = (*Adapter)(nil) 编译期证明,这里跑一下 Registry 验收。
	reg := sourceadapter.NewRegistry()
	if err := reg.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := reg.Get(AdapterType)
	if !ok || got == nil {
		t.Error("Registry Get failed")
	}
}

// ─── Sync:drive + wiki 路径 ─────────────────────────────────────────────────

func TestAdapter_SyncDrive_CollectsDocx(t *testing.T) {
	fc := &fakeClient{
		driveFiles: map[string]*DriveFilesPage{
			"": {Files: []DriveFile{
				{Token: "doxA", Name: "A.md", Type: FileTypeDocx, ModifiedTime: "2026040100"},
				{Token: "sheetB", Name: "B", Type: FileTypeSheet, ModifiedTime: "2026040100"}, // 跳过
				{Token: "folderC", Name: "C/", Type: FileTypeFolder, ModifiedTime: "2026040100"},
			}},
			"folderC": {Files: []DriveFile{
				{Token: "doxD", Name: "D.md", Type: FileTypeDocx, ModifiedTime: "2026040100"},
			}},
		},
	}
	a := NewAdapterWithClient(Config{}, fc)
	changes, err := a.Sync(context.Background(), 42, time.Time{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// 期望拿到 doxA 和 doxD 两条,sheetB 被跳过,folderC 递归了。
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2 (doxA + doxD)", len(changes))
	}
	seen := map[string]bool{}
	for _, c := range changes {
		ref, _ := RefFromSourceRef(c.Ref)
		seen[ref.FileToken] = true
		if c.Action != sourceadapter.ChangeUpdate {
			t.Errorf("action = %v, want update", c.Action)
		}
	}
	if !seen["doxA"] || !seen["doxD"] {
		t.Errorf("missing expected tokens: %v", seen)
	}
}

func TestAdapter_SyncDrive_FiltersBySince(t *testing.T) {
	// since = 2026-04-02 00:00 UTC → 只留 modified 之后的。
	since := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	beforeTs := "1743552000"  // 2025-04-02, 早
	afterTs := "1775347200"   // 2026-04-02 12:00 附近,晚于 since
	fc := &fakeClient{
		driveFiles: map[string]*DriveFilesPage{
			"": {Files: []DriveFile{
				{Token: "old", Type: FileTypeDocx, ModifiedTime: beforeTs},
				{Token: "new", Type: FileTypeDocx, ModifiedTime: afterTs},
			}},
		},
	}
	a := NewAdapterWithClient(Config{}, fc)
	changes, err := a.Sync(context.Background(), 1, since)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("want 1 change (new only), got %d", len(changes))
	}
	ref, _ := RefFromSourceRef(changes[0].Ref)
	if ref.FileToken != "new" {
		t.Errorf("filtered wrong: %s", ref.FileToken)
	}
}

// ─── Fetch:docx → markdown ──────────────────────────────────────────────────

func TestAdapter_FetchDocx_ConvertsToMarkdown(t *testing.T) {
	// 构造两个 block:一个 H1 + 一个 text paragraph。
	fc := &fakeClient{
		docxMeta: map[string]*DocxMetadata{
			"doxX": {DocumentID: "doxX", Title: "My Doc", OwnerID: "ou_123", ModifiedTime: 1800000000},
		},
		docxBlocks: map[string][]DocxBlock{
			"doxX": buildFakeBlocks(t,
				fakeBlock{ID: "root", Type: blockTypePage, ParentID: ""},
				fakeBlock{ID: "h1", Type: blockTypeHeading1, ParentID: "root", Text: "Overview"},
				fakeBlock{ID: "p1", Type: blockTypeText, ParentID: "root", Text: "Hello world"},
			),
		},
	}
	a := NewAdapterWithClient(Config{}, fc)
	ref := &Ref{FileToken: "doxX", Type: FileTypeDocx}
	src, _ := ref.ToSourceRef()
	doc, err := a.Fetch(context.Background(), 1, src)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	md := string(doc.Content)
	if !strings.Contains(md, "# Overview") {
		t.Errorf("markdown missing H1: %q", md)
	}
	if !strings.Contains(md, "Hello world") {
		t.Errorf("markdown missing paragraph: %q", md)
	}
	if doc.Title != "My Doc" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.MIMEType != "text/markdown" {
		t.Errorf("mime = %q", doc.MIMEType)
	}
	if doc.Metadata["feishu_owner_id"] != "ou_123" {
		t.Errorf("metadata missing owner_id: %v", doc.Metadata)
	}
}

func TestAdapter_FetchUnsupportedType(t *testing.T) {
	a := NewAdapterWithClient(Config{}, &fakeClient{})
	ref := &Ref{FileToken: "sheetY", Type: FileTypeSheet}
	src, _ := ref.ToSourceRef()
	_, err := a.Fetch(context.Background(), 1, src)
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("expected ErrUnsupportedType, got %v", err)
	}
}

// ─── Ref 序列化往返 ─────────────────────────────────────────────────────────

func TestRef_RoundTrip(t *testing.T) {
	in := &Ref{FileToken: "doxZ", Type: FileTypeDocx, SpaceID: "wikcn1"}
	src, err := in.ToSourceRef()
	if err != nil {
		t.Fatalf("ToSourceRef: %v", err)
	}
	out, err := RefFromSourceRef(src)
	if err != nil {
		t.Fatalf("RefFromSourceRef: %v", err)
	}
	if *out != *in {
		t.Errorf("round-trip mismatch: in=%+v out=%+v", in, out)
	}
}

func TestRef_ValidateMissingFields(t *testing.T) {
	if err := (&Ref{Type: FileTypeDocx}).Validate(); err == nil {
		t.Error("missing FileToken should error")
	}
	if err := (&Ref{FileToken: "x"}).Validate(); err == nil {
		t.Error("missing Type should error")
	}
}

// ─── BlocksToMarkdown 独立单测 ───────────────────────────────────────────────

func TestBlocksToMarkdown_HeadingsAndParagraph(t *testing.T) {
	blocks := buildFakeBlocks(t,
		fakeBlock{ID: "root", Type: blockTypePage, ParentID: ""},
		fakeBlock{ID: "h1", Type: blockTypeHeading1, ParentID: "root", Text: "标题"},
		fakeBlock{ID: "h2", Type: blockTypeHeading2, ParentID: "root", Text: "子标题"},
		fakeBlock{ID: "p", Type: blockTypeText, ParentID: "root", Text: "正文段落"},
	)
	md := BlocksToMarkdown(blocks)
	if !strings.Contains(md, "# 标题") || !strings.Contains(md, "## 子标题") || !strings.Contains(md, "正文段落") {
		t.Errorf("markdown wrong: %q", md)
	}
}

func TestBlocksToMarkdown_Code(t *testing.T) {
	// 代码块:style.language = "go",content = "fmt.Println(\"hi\")"
	codeJSON := mustJSON(t, map[string]any{
		"elements": []map[string]any{
			{"text_run": map[string]any{"content": "fmt.Println(\"hi\")"}},
		},
		"style": map[string]any{"language": "go"},
	})
	blocks := []DocxBlock{
		{BlockID: "root", BlockType: blockTypePage, ParentID: ""},
		{BlockID: "c", BlockType: blockTypeCode, ParentID: "root", Code: codeJSON},
	}
	md := BlocksToMarkdown(blocks)
	if !strings.Contains(md, "```go\nfmt.Println(\"hi\")\n```") {
		t.Errorf("code block not rendered: %q", md)
	}
}

func TestBlocksToMarkdown_InlineStyles(t *testing.T) {
	textJSON := mustJSON(t, map[string]any{
		"elements": []map[string]any{
			{"text_run": map[string]any{"content": "bold", "text_element_style": map[string]any{"bold": true}}},
			{"text_run": map[string]any{"content": " "}},
			{"text_run": map[string]any{"content": "link", "text_element_style": map[string]any{"link": map[string]any{"url": "http://x"}}}},
		},
	})
	blocks := []DocxBlock{
		{BlockID: "root", BlockType: blockTypePage, ParentID: ""},
		{BlockID: "p", BlockType: blockTypeText, ParentID: "root", Text: textJSON},
	}
	md := BlocksToMarkdown(blocks)
	if !strings.Contains(md, "**bold**") {
		t.Errorf("bold not rendered: %q", md)
	}
	if !strings.Contains(md, "[link](http://x)") {
		t.Errorf("link not rendered: %q", md)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

type fakeBlock struct {
	ID       string
	Type     int
	ParentID string
	Text     string // 纯文本,会自动包成 elements.text_run
}

// buildFakeBlocks 给每个 fakeBlock 生成对应 JSON 字段。用来省去测试里手写 JSON 的重复。
func buildFakeBlocks(t *testing.T, items ...fakeBlock) []DocxBlock {
	t.Helper()
	out := make([]DocxBlock, 0, len(items))
	for _, it := range items {
		b := DocxBlock{BlockID: it.ID, BlockType: it.Type, ParentID: it.ParentID}
		if it.Text == "" {
			out = append(out, b)
			continue
		}
		raw := mustJSON(t, map[string]any{
			"elements": []map[string]any{
				{"text_run": map[string]any{"content": it.Text}},
			},
		})
		switch it.Type {
		case blockTypeText:
			b.Text = raw
		case blockTypeHeading1:
			b.Heading1 = raw
		case blockTypeHeading2:
			b.Heading2 = raw
		case blockTypeHeading3:
			b.Heading3 = raw
		case blockTypeBullet:
			b.Bullet = raw
		case blockTypeOrdered:
			b.Ordered = raw
		case blockTypeQuote:
			b.Quote = raw
		}
		out = append(out, b)
	}
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}
