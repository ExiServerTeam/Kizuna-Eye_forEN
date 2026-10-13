#!/bin/bash
# Kizuna-Eye ビルドスクリプト（build.sh と同等。リポジトリ直下から実行）
set -e
umask 077  # New files: 0600/0700 (owner-executable binaries)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export PATH="$PATH:/usr/local/go/bin"
# 正確なパッチを固定する。host と全プラグインは同一ツールチェーンを使う
# 必要がある。さもないと plugin.Open が
# "different version of package" で失敗する。
# Pin the exact patch: host and every plugin must use the SAME toolchain, or
# plugin.Open fails with "different version of package".
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
PINNED_GO=go1.27.2

BIN_DIR="${BIN_DIR:-/opt/kizuna-eye/bin}"
mkdir -p "$BIN_DIR"

VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.8.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

# メモリ最適化（低スペック機向け）: -s -w でシンボルを削る。
# -trimpath は host と全 plugin で必ず同じ設定にする（ここでは常時 ON）。
# 外すとビルドしたユーザーの GOMODCACHE 絶対パスが build ID に埋め込まれ、
# agent を sudo・plugin を user でビルドしただけで ABI 不一致になる。
TRIMPATH="-trimpath"
if [ "${KIZUNA_DEBUG_BUILD:-0}" = "1" ]; then
    : # keep LDFLAGS as-is
else
    LDFLAGS="-s -w $LDFLAGS"
fi

echo "[build] version=${VERSION} bin=${BIN_DIR} go=${PINNED_GO} debug=${KIZUNA_DEBUG_BUILD:-0}"
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
# A-3: Ed25519 プラグイン署名ツール（CGO 不要だが他と揃えて CGO_ENABLED=1）。
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "[build] done"
ls -la "$BIN_DIR"
