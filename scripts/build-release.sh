#!/usr/bin/env bash
# 按当前仓库 VERSION 打出发行包，放到 dist/。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

VERSION=$(tr -d ' \n' <VERSION)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS="-s -w -X wsproxy/internal/version.Version=${VERSION} -X wsproxy/internal/version.Commit=${COMMIT}"

rm -rf dist
mkdir -p dist

targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
)

for spec in "${targets[@]}"; do
  os=${spec%/*}
  arch=${spec#*/}
  tmp=$(mktemp -d)
  echo "build ${os}/${arch}"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$tmp/wsproxy" ./cmd/wsproxy
  tar -C "$tmp" -czf "dist/wsproxy-${os}-${arch}.tar.gz" wsproxy
  rm -rf "$tmp"
done

cp install.sh dist/install.sh
chmod +x dist/install.sh

(
  cd dist
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum * >SHA256SUMS
  else
    shasum -a 256 * >SHA256SUMS
  fi
)

echo "VERSION ${VERSION}"
ls -l dist
