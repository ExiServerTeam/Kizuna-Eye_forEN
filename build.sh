#!/bin/bash
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

BIN_DIR="/opt/kizuna-eye/bin"
mkdir -p "$BIN_DIR"

echo "🔨 ビルド中..."
CGO_ENABLED=1 go build -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 go build -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 go build -o "$BIN_DIR/agent_linux" ./cmd/agent
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
