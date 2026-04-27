// upload-agent-bridge - 一次性把 dist/agent-bridge/<version>/ 下所有文件上传到 OSS。
//
// 用法:
//
//	go run ./cmd/agent-bridge/upload v0.1.0
//
// 读 config 拿 OSS 凭证(走 Synapse 主 config 加载逻辑,自动合并 config.local.yaml)。
// 上传到 oss key:agent-bridge/<version>/<filename>
// 用 application/octet-stream(浏览器会触发下载而不是渲染)。
//
// 输出每个文件的公开 URL,前端下载页直接用这些 URL。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/common/ossupload"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/agent-bridge/upload <version>")
		os.Exit(2)
	}
	version := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	if cfg.OSS.AccessKeyID == "" || cfg.OSS.Bucket == "" {
		fmt.Fprintln(os.Stderr, "OSS not configured (check config/config.local.yaml)")
		os.Exit(1)
	}

	client, err := ossupload.New(ossupload.Config{
		AccessKeyID:     cfg.OSS.AccessKeyID,
		AccessKeySecret: cfg.OSS.AccessKeySecret,
		Endpoint:        cfg.OSS.Endpoint,
		Region:          cfg.OSS.Region,
		Bucket:          cfg.OSS.Bucket,
		Domain:          cfg.OSS.Domain,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build oss client:", err)
		os.Exit(1)
	}

	distDir := filepath.Join("dist/agent-bridge", version)
	entries, err := os.ReadDir(distDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read dist dir:", err)
		fmt.Fprintln(os.Stderr, "(先跑 ./cmd/agent-bridge/build.sh", version, "出二进制)")
		os.Exit(1)
	}

	ctx := context.Background()
	fmt.Printf("→ uploading dist/agent-bridge/%s/ to oss bucket %s\n\n", version, cfg.OSS.Bucket)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(distDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read", path, ":", err)
			os.Exit(1)
		}
		ossKey := fmt.Sprintf("agent-bridge/%s/%s", version, e.Name())
		url, err := client.PutObject(ctx, ossKey, data, "application/octet-stream")
		if err != nil {
			fmt.Fprintln(os.Stderr, "put", ossKey, ":", err)
			os.Exit(1)
		}
		fmt.Printf("✓ %-32s %d bytes\n  %s\n\n", e.Name(), len(data), url)
	}

	fmt.Println("✓ done. 把上面的 URL pattern 给前端下载页用。")
}
