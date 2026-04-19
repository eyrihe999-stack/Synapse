// Package ossclient 对象存储访问客户端。
//
// 提供上传/下载/删除/预签名 URL 接口。当前仅实现 Aliyun OSS,
// 未来可按 provider 扩展(AWS S3、MinIO 等)。
package ossclient

import (
	"context"
	"fmt"
	"io"
)

// ProviderAliyun 阿里云 OSS provider 名称。
const ProviderAliyun = "aliyun"

// Config OSS 客户端配置。
type Config struct {
	Provider        string `yaml:"provider"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	// Endpoint 为空时由 Region 推导 (oss-<region>.aliyuncs.com)。
	Endpoint string `yaml:"endpoint"`
	// Domain 自定义域名/CDN 域名,用于生成公开访问 URL。空则使用 bucket.<endpoint>。
	Domain string `yaml:"domain"`
	// PathPrefix 所有对象的全局前缀,例如 "synapse/docs",不含前后斜杠。
	PathPrefix string `yaml:"path_prefix"`
}

// Client OSS 统一访问接口。
type Client interface {
	// Put 上传字节流到指定 key,返回访问 URL。
	Put(ctx context.Context, key string, data []byte, contentType string) (string, error)
	// Get 下载对象字节流。调用方负责 Close。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete 删除对象。对象不存在不返回错误。
	Delete(ctx context.Context, key string) error
	// Presign 生成临时签名下载 URL,expireSeconds 秒内有效。
	Presign(ctx context.Context, key string, expireSeconds int64) (string, error)
	// Exists 判断对象是否存在。
	Exists(ctx context.Context, key string) (bool, error)
	// URL 返回对象的公开访问 URL(不签名,适用于 bucket 可公开读的场景)。
	URL(key string) string
	// PathPrefix 返回配置的全局 key 前缀。
	PathPrefix() string
}

// New 根据 cfg.Provider 构造对应实现。
func New(cfg Config) (Client, error) {
	switch cfg.Provider {
	case "", ProviderAliyun:
		return newAliyun(cfg)
	default:
		return nil, fmt.Errorf("ossclient: unknown provider %q", cfg.Provider)
	}
}
