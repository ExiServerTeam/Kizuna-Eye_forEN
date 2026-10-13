#!/bin/bash
set -e
umask 077  # New files: 0600/0700 (owner-executable binaries)
cd "$(dirname "${BASH_SOURCE[0]}")"

BIN_DIR="/opt/kizuna-eye/bin"
mkdir -p "$BIN_DIR"

# バージョン情報を ldflags で埋め込む
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.7.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

echo "🔨 ビルド中... (version=${VERSION})"
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
# A-3: plugin signing helper (Ed25519). CGO is not required, but keep the
# build flags identical to the other binaries for a consistent toolchain.
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
