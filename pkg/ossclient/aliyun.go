// aliyun.go 阿里云 OSS 客户端实现。
package ossclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

type aliyunClient struct {
	bucket *oss.Bucket
	cfg    Config
	domain string
}

func newAliyun(cfg Config) (Client, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, errors.New("ossclient/aliyun: access key / secret required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("ossclient/aliyun: bucket required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		if cfg.Region == "" {
			return nil, errors.New("ossclient/aliyun: either endpoint or region required")
		}
		endpoint = fmt.Sprintf("oss-%s.aliyuncs.com", cfg.Region)
	}

	client, err := oss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("ossclient/aliyun: create client: %w", err)
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("ossclient/aliyun: get bucket %s: %w", cfg.Bucket, err)
	}

	domain := cfg.Domain
	if domain == "" {
		domain = fmt.Sprintf("https://%s.%s", cfg.Bucket, endpoint)
	}
	domain = strings.TrimRight(domain, "/")

	return &aliyunClient{
		bucket: bucket,
		cfg:    cfg,
		domain: domain,
	}, nil
}

func (c *aliyunClient) Put(_ context.Context, key string, data []byte, contentType string) (string, error) {
	opts := []oss.Option{}
	if contentType != "" {
		opts = append(opts, oss.ContentType(contentType))
	}
	if err := c.bucket.PutObject(key, bytes.NewReader(data), opts...); err != nil {
		return "", fmt.Errorf("ossclient/aliyun: put %s: %w", key, err)
	}
	return c.URL(key), nil
}

func (c *aliyunClient) Get(_ context.Context, key string) (io.ReadCloser, error) {
	body, err := c.bucket.GetObject(key)
	if err != nil {
		return nil, fmt.Errorf("ossclient/aliyun: get %s: %w", key, err)
	}
	return body, nil
}

func (c *aliyunClient) Delete(_ context.Context, key string) error {
	if err := c.bucket.DeleteObject(key); err != nil {
		return fmt.Errorf("ossclient/aliyun: delete %s: %w", key, err)
	}
	return nil
}

func (c *aliyunClient) Presign(_ context.Context, key string, expireSeconds int64) (string, error) {
	if expireSeconds <= 0 {
		expireSeconds = int64((1 * time.Hour).Seconds())
	}
	signedURL, err := c.bucket.SignURL(key, oss.HTTPGet, expireSeconds)
	if err != nil {
		return "", fmt.Errorf("ossclient/aliyun: presign %s: %w", key, err)
	}
	return signedURL, nil
}

func (c *aliyunClient) Exists(_ context.Context, key string) (bool, error) {
	ok, err := c.bucket.IsObjectExist(key)
	if err != nil {
		return false, fmt.Errorf("ossclient/aliyun: exists %s: %w", key, err)
	}
	return ok, nil
}

func (c *aliyunClient) URL(key string) string {
	return fmt.Sprintf("%s/%s", c.domain, strings.TrimLeft(key, "/"))
}

func (c *aliyunClient) PathPrefix() string {
	return strings.Trim(c.cfg.PathPrefix, "/")
}
