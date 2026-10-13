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
#
# -trimpath は「host と全 plugin で必ず同じ設定」にする（ここでは常時 ON）。
# これを外すと、ビルドしたユーザーの GOMODCACHE 絶対パス（/root/go と
# /home/user/go など）がパッケージの build ID に埋め込まれ、agent を sudo で、
# plugin を user でビルドしただけで `plugin was built with a different version
# of package github.com/shirou/gopsutil/v3/common` でロード不能になる（実機で
# 発生）。-trimpath を両方に付ければ build ID がパス非依存になり、誰がビルド
# しても一致する。以前壊れたのは「片方だけ付けた」ため。
TRIMPATH="-trimpath"
if [ "${KIZUNA_DEBUG_BUILD:-0}" = "1" ]; then
    LDFLAGS="$LDFLAGS"
else
    LDFLAGS="-s -w $LDFLAGS"
fi

echo "🔨 ビルド中... (version=${VERSION}, go=$(GOTOOLCHAIN=${PINNED_GO} go version | awk '{print $3}'), debug=${KIZUNA_DEBUG_BUILD:-0})"
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
# A-3: plugin signing helper (Ed25519). CGO is not required, but keep the
# build flags identical to the other binaries for a consistent toolchain.
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
