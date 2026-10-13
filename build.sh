#!/bin/bash
set -e
umask 077  # New files: 0600/0700 (owner-executable binaries)
cd "$(dirname "${BASH_SOURCE[0]}")"

export PATH="$PATH:/usr/local/go/bin"
# Pin the exact patch: host and every plugin must be built with the SAME
# toolchain, or plugin.Open fails with "different version of package".
# GOTOOLCHAIN=auto lets this fetch go1.27.2 if it is not installed yet.
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
PINNED_GO=go1.27.2
if ! go version | grep -q "go1\.27\.2"; then
    echo "⚠️  go version is $(go version); forcing ${PINNED_GO} via GOTOOLCHAIN"
fi

BIN_DIR="/opt/kizuna-eye/bin"
mkdir -p "$BIN_DIR"

# バージョン情報を ldflags で埋め込む
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.7.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

# メモリ最適化: -s -w でシンボル/DWARF を削り、-trimpath でビルドパスを
# 埋め込まない。バイナリが約3割小さくなり、RSS の大半を占めるファイル
# マップページが減る（低スペック機で効く）。Go のスタックトレースは
# pclntab から生成されるため -s -w でも関数名・行番号は出る。
# ソースレベルデバッグ（gdb/dlv）が必要なときは KIZUNA_DEBUG_BUILD=1。
# NOTE: -trimpath is intentionally NOT used. It changes the recorded
# package build info, and a host built with it refuses to load a plugin
# built without it (and vice versa): "plugin was built with a different
# version of package Kizuna-Eye/pkg/fsutil". The host and every plugin must
# use identical flags, so -trimpath is dropped everywhere. -s -w alone is
# safe and already shrinks the binaries by ~30%.
TRIMPATH=""
if [ "${KIZUNA_DEBUG_BUILD:-0}" = "1" ]; then
    LDFLAGS="$LDFLAGS"
else
    LDFLAGS="-s -w $LDFLAGS"
fi

echo "🔨 ビルド中... (version=${VERSION}, go=$(GOTOOLCHAIN=${PINNED_GO} go version | awk '{print $3}'), debug=${KIZUNA_DEBUG_BUILD:-0})"
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
# A-3: plugin signing helper (Ed25519). CGO is not required, but keep the
# build flags identical to the other binaries for a consistent toolchain.
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
