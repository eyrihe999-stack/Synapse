package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	oauthmw "github.com/eyrihe999-stack/Synapse/internal/oauth/middleware"
)

// registerAttachmentTools 注册 channel 附件(PR #16')的 MCP tool。
//
// 2 个 tool —— "agent 上传带图片文档"完整闭环的 OSS 直传两段:
//   - request_channel_attachment_upload_url   拿 presign PUT URL + commit_token
//   - commit_channel_attachment_upload        通知服务端落 channel_attachments 行
//
// agent 完整工作流:
//
//	1. request_channel_attachment_upload_url(channel_id, mime_type[, filename])
//	   → 拿 upload_url + commit_token + content_type
//	2. curl --upload-file ./image.png '<upload_url>' -H 'Content-Type: <content_type>'
//	3. commit_channel_attachment_upload(channel_id, commit_token)
//	   → 拿 attachment_id + url(形如 /api/v2/channels/123/attachments/456)
//	4. 把 url 拼进 markdown:`![alt](/api/v2/channels/123/attachments/456)`
//	5. 走现有 request_document_upload_url + commit_document_upload 把 markdown 落 doc
//
// 不暴露 list / delete / get-by-id —— 治理动作走 Web。
func (s *Server) registerAttachmentTools() {
	if s.deps.AttachmentSvc == nil {
		return
	}

	s.mcp.AddTool(mcp.NewTool("request_channel_attachment_upload_url",
		mcp.WithDescription("Get a presigned PUT URL to upload an image attachment directly to OSS, plus a commit_token. "+
			"USE THIS to embed images in shared documents or messages — once committed you get a stable URL "+
			"that can be referenced from markdown as `![alt](/api/v2/channels/<cid>/attachments/<aid>)`. "+
			"\n\n"+
			"Allowed MIME types (first version): image/png, image/jpeg, image/gif, image/webp. "+
			"SVG is NOT allowed (sandboxing risk). Max byte size: 10 MB. "+
			"\n\n"+
			"After you get the URL: PUT the file with `curl --upload-file <local_path> '<url>' -H 'Content-Type: <mime_type>'` "+
			"(or any HTTP client). The Content-Type header MUST match `mime_type` exactly or OSS will reject "+
			"with SignatureDoesNotMatch. After PUT succeeds (HTTP 200), call commit_channel_attachment_upload with the commit_token. "+
			"The presign + token are valid for 5 minutes. Caller must be a channel member; channel must not be archived."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithString("mime_type", mcp.Required(), mcp.Description("One of: image/png, image/jpeg, image/gif, image/webp")),
		mcp.WithString("filename", mcp.Description("Optional original filename (≤256 chars). Stored as metadata; not used for OSS key.")),
	), s.handleRequestChannelAttachmentUploadURL)

	s.mcp.AddTool(mcp.NewTool("commit_channel_attachment_upload",
		mcp.WithDescription("Commit a previously-uploaded OSS attachment object. REQUIRES: you previously called "+
			"request_channel_attachment_upload_url and PUT the file. Server verifies token, fetches the OSS object, "+
			"checks size ≤ 10 MB, computes sha256, and writes a row in channel_attachments. "+
			"\n\n"+
			"DEDUP: if the same (channel_id, sha256) already exists (you or someone else uploaded the same bytes "+
			"to this channel before), `reused=true` is returned and the existing attachment_id is reused — "+
			"the OSS object you just uploaded is deleted to save storage. "+
			"\n\n"+
			"The returned `url` is a relative path you can copy DIRECTLY into markdown: "+
			"`![alt](/api/v2/channels/123/attachments/456)`. "+
			"When the user's browser later renders the markdown, it will fetch the URL → server checks "+
			"channel membership → 302 redirects to a short-lived signed OSS URL → image displays."),
		mcp.WithNumber("channel_id", mcp.Required()),
		mcp.WithString("commit_token", mcp.Required(), mcp.Description("Token from request_channel_attachment_upload_url. Single-use, expires in 5min.")),
	), s.handleCommitChannelAttachmentUpload)
}

func (s *Server) handleRequestChannelAttachmentUploadURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	mime := stringArg(req, "mime_type", "")
	filename := stringArg(req, "filename", "")
	if channelID == 0 || mime == "" {
		return mcp.NewToolResultError("channel_id and mime_type are required"), nil
	}
	out, err := s.deps.AttachmentSvc.PresignUploadByPrincipal(ctx, AttachmentPresignUploadByPrincipalInput{
		ChannelID:        channelID,
		ActorPrincipalID: auth.AgentPrincipalID,
		MimeType:         mime,
		Filename:         filename,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request_channel_attachment_upload_url: %s", err.Error())), nil
	}
	return jsonResult(map[string]any{
		"upload_url":    out.UploadURL,
		"commit_token":  out.CommitToken,
		"content_type":  out.ContentType,
		"expires_at":    out.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"max_byte_size": out.MaxByteSize,
		"hint":          "PUT bytes to upload_url with the exact Content-Type header above, then call commit_channel_attachment_upload with the commit_token.",
	})
}

func (s *Server) handleCommitChannelAttachmentUpload(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := oauthmw.AuthFromContext(ctx)
	if !ok {
		return mcp.NewToolResultError("authentication required"), nil
	}
	channelID := uint64Arg(req, "channel_id", 0)
	token := stringArg(req, "commit_token", "")
	if channelID == 0 || token == "" {
		return mcp.NewToolResultError("channel_id and commit_token are required"), nil
	}
	out, err := s.deps.AttachmentSvc.CommitUploadByPrincipal(ctx, AttachmentCommitUploadByPrincipalInput{
		ChannelID:        channelID,
		ActorPrincipalID: auth.AgentPrincipalID,
		CommitToken:      token,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("commit_channel_attachment_upload: %s", err.Error())), nil
	}
	att := out.Attachment
	url := fmt.Sprintf("/api/v2/channels/%d/attachments/%d", att.ChannelID, att.ID)
	return jsonResult(map[string]any{
		"attachment_id": att.ID,
		"channel_id":    att.ChannelID,
		"url":           url,
		"mime_type":     att.MimeType,
		"filename":      att.Filename,
		"byte_size":     att.ByteSize,
		"sha256":        att.Sha256,
		"reused":        out.Reused,
		"hint":          "Embed in markdown as: ![alt](" + url + ")",
	})
}
