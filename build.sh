#!/bin/bash
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

BIN_DIR="/opt/kizuna-eye/bin"
mkdir -p "$BIN_DIR"

# バージョン情報を ldflags で埋め込む
VERSION="${VERSION:-v0.7.1}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

echo "🔨 ビルド中... (version=${VERSION})"
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
