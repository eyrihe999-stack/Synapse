#!/usr/bin/env bash
# build.sh — 把 agent-bridge 打成 mac 双架构二进制(arm64 + amd64)。
#
# 用法:
#   ./cmd/agent-bridge/build.sh                    # 用 git 自动算版本号(v0.1.0-3-gabc1234)
#   ./cmd/agent-bridge/build.sh v0.1.0             # 显式指定版本号
#
# 输出:
#   dist/agent-bridge/<version>/agent-bridge-darwin-arm64
#   dist/agent-bridge/<version>/agent-bridge-darwin-amd64
#   dist/agent-bridge/<version>/checksums.txt
#
# 上传(手动跑):
#   ossutil cp -r dist/agent-bridge/<version>/ oss://<your-bucket>/agent-bridge/<version>/
#
# 然后把这个 version 告诉前端下载页(SecurityPage 的 download 块)。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# 版本号:命令行第一个参数 > git describe > "dev"
VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  if git rev-parse --git-dir >/dev/null 2>&1; then
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
  else
    VERSION="dev"
  fi
fi

OUT_DIR="dist/agent-bridge/${VERSION}"
mkdir -p "$OUT_DIR"

# build flags:注入版本号 + strip 调试信息(减小体积约 30%)
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "→ building agent-bridge ${VERSION}"
echo "  output: ${OUT_DIR}/"
echo

build_one() {
  local goos=$1 goarch=$2 name=$3
  echo "  ${name} ..."
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" \
    -o "${OUT_DIR}/${name}" ./cmd/agent-bridge
}

build_one darwin arm64 agent-bridge-darwin-arm64
build_one darwin amd64 agent-bridge-darwin-amd64

# checksums(用户可选验证完整性)
echo
echo "→ checksums:"
( cd "$OUT_DIR" && shasum -a 256 agent-bridge-darwin-* > checksums.txt && cat checksums.txt )

echo
echo "✓ done."
echo
echo "上传 OSS(自己跑):"
echo "  ossutil cp -r ${OUT_DIR}/ oss://<your-bucket>/agent-bridge/${VERSION}/"
echo
echo "前端下载链接 pattern:"
echo "  https://<your-bucket>.<your-endpoint>.aliyuncs.com/agent-bridge/${VERSION}/agent-bridge-darwin-arm64"
echo "  https://<your-bucket>.<your-endpoint>.aliyuncs.com/agent-bridge/${VERSION}/agent-bridge-darwin-amd64"
