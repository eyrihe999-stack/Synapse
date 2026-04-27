// Package ossupload 封装阿里云 OSS 的读 / 写 / 删三类操作。
//
// 基于 sayso-server/pkg/ossupload,裁剪掉未使用的签名直传逻辑,补齐 GetObject + DeleteObject。
// Client 是单例,cmd/synapse 装配期构造一次,注入给 handler / ingestion runner 共用。
package ossupload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// Client 对外抽象。PutObject 返回可直接展示 / 下载的 URL;
// GetObject 拉回原字节(pipeline chunk + embed 需要);DeleteObject 用于历史版本裁剪 / 删文档清理。
//
// PresignPutURL / HeadObject / StreamGet 三个方法服务于"client 直传 OSS"场景
// (channel 共享文档 ≥ 几十 KB 的上传)—— LLM 拿 presign URL 后用 HTTP/Bash 工具
// 直接 PUT 字节到 OSS,server 只用 HEAD 拿 size + StreamGet 算 sha256,字节不经 server。
type Client interface {
	PutObject(ctx context.Context, key string, data []byte, contentType string) (url string, err error)
	GetObject(ctx context.Context, key string) ([]byte, error)
	DeleteObject(ctx context.Context, key string) error
	URL(key string) string

	// PresignPutURL 生成"客户端直传 PUT"的预签名 URL,有效期 ttl。
	// 客户端必须在 PUT 时带 `Content-Type: <contentType>` 头(签名时会绑定)。
	PresignPutURL(ctx context.Context, key string, ttl time.Duration, contentType string) (string, error)

	// PresignGetURL 生成"客户端直拉 GET"的预签名 URL,有效期 ttl。
	// 客户端 GET 该 URL 即可拿对象字节,无须携带额外头。bucket 私读时这是唯一访问方式。
	PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	// HeadObject 拿对象 size(不下载内容)。不存在返 (-1, err)。
	HeadObject(ctx context.Context, key string) (size int64, err error)

	// StreamGet 流式拉对象,maxBytes > 0 时遇到超限读到 maxBytes+1 后停(让调用方判断"超限")。
	// 返回的 ReadCloser 必须 Close。
	StreamGet(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, error)
}

// Config 单 bucket 的接入参数。Endpoint 为空时按 Region 自动拼 oss-<region>.aliyuncs.com。
// Domain 为空时走 bucket.endpoint 默认域;填 CDN 可走自定义域返链。
type Config struct {
	AccessKeyID     string
	AccessKeySecret string
	Endpoint        string
	Region          string
	Bucket          string
	Domain          string
}

type aliyunClient struct {
	bucket *oss.Bucket
	domain string
}

// New 构造 Aliyun OSS client。任一字段缺失(AK / Bucket)都会让 oss.New 或 client.Bucket 返错,上抛让调用方 fatal。
func New(cfg Config) (Client, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("oss-%s.aliyuncs.com", cfg.Region)
	}
	client, err := oss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("ossupload: create client: %w", err)
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("ossupload: get bucket %s: %w", cfg.Bucket, err)
	}
	domain := cfg.Domain
	if domain == "" {
		domain = fmt.Sprintf("https://%s.%s", cfg.Bucket, endpoint)
	}
	return &aliyunClient{bucket: bucket, domain: domain}, nil
}

// PutObject 上传字节。ctx 暂未透传到 SDK(sdk 内部不支持 ctx 级取消),长上传依赖 HTTP timeout 控制。
func (c *aliyunClient) PutObject(_ context.Context, key string, data []byte, contentType string) (string, error) {
	opts := []oss.Option{oss.ContentType(contentType)}
	if err := c.bucket.PutObject(key, bytes.NewReader(data), opts...); err != nil {
		return "", fmt.Errorf("ossupload: put %s: %w", key, err)
	}
	return c.URL(key), nil
}

// GetObject 一次性拉回全部字节。md 文件 ≤ 10MB,单次 ReadAll 可接受。
func (c *aliyunClient) GetObject(_ context.Context, key string) ([]byte, error) {
	body, err := c.bucket.GetObject(key)
	if err != nil {
		return nil, fmt.Errorf("ossupload: get %s: %w", key, err)
	}
	//sayso-lint:ignore defer-err
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("ossupload: read %s: %w", key, err)
	}
	return data, nil
}

// DeleteObject 删除单个对象。历史版本裁剪 / 文档删除时调用,失败由调用方决定告警不告警。
func (c *aliyunClient) DeleteObject(_ context.Context, key string) error {
	if err := c.bucket.DeleteObject(key); err != nil {
		return fmt.Errorf("ossupload: delete %s: %w", key, err)
	}
	return nil
}

// URL 拼出对象的完整访问 URL(不做签名)。bucket 公读时前端可直接 GET。
func (c *aliyunClient) URL(key string) string {
	return fmt.Sprintf("%s/%s", c.domain, key)
}

// PresignPutURL 生成"客户端直传 PUT"的预签名 URL。
//
// 阿里云 SDK 的 SignURL 把 ContentType 选项作为签名输入 —— 客户端 PUT 时
// 必须带相同的 Content-Type header,否则 OSS 返 SignatureDoesNotMatch。
//
// ttl 内有效;过期后 OSS 拒。建议 5min 左右(足够大文件传输 + 不留过长窗口)。
func (c *aliyunClient) PresignPutURL(_ context.Context, key string, ttl time.Duration, contentType string) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("ossupload: presign ttl must be positive, got %s", ttl)
	}
	expSec := int64(ttl.Seconds())
	if expSec < 1 {
		expSec = 1
	}
	url, err := c.bucket.SignURL(key, oss.HTTPPut, expSec, oss.ContentType(contentType))
	if err != nil {
		return "", fmt.Errorf("ossupload: presign put %s: %w", key, err)
	}
	return url, nil
}

// PresignGetURL 生成 GET 预签名 URL。GET 不绑定 Content-Type(对称 PresignPutURL 的不对称之处)。
//
// 用途:agent / web 端拿到 URL 后直接 curl / fetch 下载对象,字节不经 Synapse。
// 用于 channel 共享文档 / KB 文档的"本地编辑"场景:LLM 调 Bash + curl 下载 → 改本地 → 直传上传。
func (c *aliyunClient) PresignGetURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("ossupload: presign ttl must be positive, got %s", ttl)
	}
	expSec := int64(ttl.Seconds())
	if expSec < 1 {
		expSec = 1
	}
	url, err := c.bucket.SignURL(key, oss.HTTPGet, expSec)
	if err != nil {
		return "", fmt.Errorf("ossupload: presign get %s: %w", key, err)
	}
	return url, nil
}

// HeadObject 拿对象 size 不下载。
//
// 不存在 / 网络错都返 (-1, err);调用方按 err 判 NotFound 用 errors.Is(err, oss.ServiceError) 之类。
// 当前实现简化:任何错都包装成内部错,调用方按 err string 判定不太严谨,后续如需精细
// 处理可暴露 SDK error。
func (c *aliyunClient) HeadObject(_ context.Context, key string) (int64, error) {
	hdr, err := c.bucket.GetObjectMeta(key)
	if err != nil {
		return -1, fmt.Errorf("ossupload: head %s: %w", key, err)
	}
	clen := hdr.Get("Content-Length")
	if clen == "" {
		return -1, fmt.Errorf("ossupload: head %s: empty Content-Length", key)
	}
	n, err := strconv.ParseInt(clen, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("ossupload: head %s: bad Content-Length %q: %w", key, clen, err)
	}
	return n, nil
}

// StreamGet 流式读 OSS 对象,返 ReadCloser。maxBytes > 0 时建议调用方读到
// maxBytes+1 字节后停 — 用于"读够 max+1 即可证明超限"的语义。
//
// SDK 没有原生 byte-range 流上限,这里直接返完整 GetObject 流,由调用方决定
// 读多少。md / text 文件 ≤ 1MB,几百 ms 读完不痛。
func (c *aliyunClient) StreamGet(_ context.Context, key string, _ int64) (io.ReadCloser, error) {
	body, err := c.bucket.GetObject(key)
	if err != nil {
		return nil, fmt.Errorf("ossupload: stream get %s: %w", key, err)
	}
	return body, nil
}
