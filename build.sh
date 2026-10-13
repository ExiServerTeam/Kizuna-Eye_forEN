#!/bin/bash
set -e
umask 077  # 新規ファイルは 0600/0700（所有者のみ実行可能なバイナリ）
cd "$(dirname "${BASH_SOURCE[0]}")"

export PATH="$PATH:/usr/local/go/bin"
# 正確なパッチを固定する。host と全プラグインは同一ツールチェーンで
# ビルドしなければならない。さもないと plugin.Open が
# "different version of package" で失敗する。
# GOTOOLCHAIN=auto は未導入時に go1.27.2 を取得させる。
# Pin the exact patch: host and every plugin must be built with the SAME
# toolchain, or plugin.Open fails with "different version of package".
# GOTOOLCHAIN=auto lets this fetch go1.27.2 if it is not installed yet.
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
PINNED_GO=go1.27.2
if ! go version | grep -q "go1\.27\.2"; then
    echo "⚠️  go version is $(go version); forcing ${PINNED_GO} via GOTOOLCHAIN"
fi

# 出力先は環境変数で上書きできる。deb ビルド（docker-build-deb.sh）は
# /opt と切り離したステージング先を指定し、インストール済み環境のバイナリを
# 誤って梱包しないようにする。
# BIN_DIR is overridable so the deb build can use a staging dir instead of
# /opt (which may hold binaries from a previous install).
BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
mkdir -p "$BIN_DIR"

# バージョン情報を ldflags で埋め込む
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.8.0)}"
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
# A-3: プラグイン署名ツール（Ed25519）。CGO は不要だが、ツールチェーンを
# 揃えるため他バイナリと同じビルドフラグを保つ。
# A-3: plugin signing helper (Ed25519). CGO is not required, but keep the
# build flags identical to the other binaries for a consistent toolchain.
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
echo "✅ ビルド完了"
ls -la "$BIN_DIR"
