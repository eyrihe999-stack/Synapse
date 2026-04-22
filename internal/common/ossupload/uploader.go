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

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// Client 对外抽象。PutObject 返回可直接展示 / 下载的 URL;
// GetObject 拉回原字节(pipeline chunk + embed 需要);DeleteObject 用于历史版本裁剪 / 删文档清理。
type Client interface {
	PutObject(ctx context.Context, key string, data []byte, contentType string) (url string, err error)
	GetObject(ctx context.Context, key string) ([]byte, error)
	DeleteObject(ctx context.Context, key string) error
	URL(key string) string
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
