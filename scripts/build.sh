#!/bin/bash
# Kizuna-Eye ビルドスクリプト（build.sh と同等。リポジトリ直下から実行）
set -e
umask 077  # New files: 0600/0700 (owner-executable binaries)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

BIN_DIR="${BIN_DIR:-/opt/kizuna-eye/bin}"
mkdir -p "$BIN_DIR"

VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.7.1)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

echo "[build] version=${VERSION} bin=${BIN_DIR}"
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
# A-3: Ed25519 プラグイン署名ツール（CGO 不要だが他と揃えて CGO_ENABLED=1）。
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "[build] done"
ls -la "$BIN_DIR"
