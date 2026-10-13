#!/bin/bash
# ============================================================
# Kizuna-Eye の完全再ビルド: host バイナリ + 全プラグイン .so。
#
# なぜ必要か
#   Go プラグインは全共有パッケージ（pkg/module、pkg/status、pkg/fsutil など）に
#   対して正確な build-hash 一致を要求する。そのいずれかを編集すると、導入済みの
#   全 .so が
#       plugin was built with a different version of package Kizuna-Eye/pkg/module
#   でロードに失敗し、agent は全監視を黙って失う（ログに ERROR が1行出るだけ）。
#   2026-10-08 に実際に踏んだ: agent だけ再ビルドすると kizuna_security が落ち、
#   手でプラグインを再ビルドするまで復旧しなかった。
#
#   pkg/module、pkg/status、pkg/fsutil、その他共有パッケージを変更したら、
#   build.sh ではなくこれを実行する。
#
# 使い方
#   ./build-all.sh                 # env 変数で見つけた host + プラグイン
#   KIZUNA_SEC_PLUGIN_DIR=/path/to/Kizuna-Security/plugin \
#   KIZUNA_LITE_PLUGIN_DIR=/path/to/Kizuna-Backup-LITE/plugin ./build-all.sh
#
# プラグインのソース位置は opt-in（プロジェクトは同梱しない）。
# install.sh / scripts/build-plugin.sh と全く同じ方針だ。
# ============================================================
# Kizuna-Eye full rebuild: host binaries + every plugin .so.
#
# WHY THIS EXISTS
#   Go plugins require an exact build-hash match for every shared
#   package (pkg/module, pkg/status, pkg/fsutil, ...). Editing one of
#   those makes every installed .so fail to load with
#       plugin was built with a different version of package Kizuna-Eye/pkg/module
#   and the agent silently loses all monitoring (only an ERROR line in
#   the log). This bit us on 2026-10-08: an agent-only rebuild dropped
#   kizuna_security until the plugins were rebuilt by hand.
#
#   Run this instead of build.sh whenever pkg/module, pkg/status,
#   pkg/fsutil, or any other shared package changed.
#
# USAGE
#   ./build-all.sh                 # host + plugins found via env vars
#   KIZUNA_SEC_PLUGIN_DIR=/path/to/Kizuna-Security/plugin \
#   KIZUNA_LITE_PLUGIN_DIR=/path/to/Kizuna-Backup-LITE/plugin ./build-all.sh
#
# The plugin source locations are opt-in (the project does not ship
# them), exactly like install.sh / scripts/build-plugin.sh.
# ============================================================
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

export PATH="$PATH:/usr/local/go/bin"
# Pin the exact patch: the host and every plugin must be built with the SAME
# toolchain, or plugin.Open fails with "different version of package".
# GOTOOLCHAIN=auto は未導入時に Go が go1.27.2 を取得できるようにする。
# GOTOOLCHAIN=auto lets Go fetch go1.27.2 when it is not installed yet.
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
PINNED_GO=go1.27.2

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
DEPLOY_DIR="$BIN_DIR/plugins"
SIGN_BIN="$BIN_DIR/plugin-sign"
SIGNING_KEY="${KIZUNA_SIGNING_KEY:-$HOME/.kizuna-eye/keys/plugin_signing/plugin_signing.key}"
SIGNING_PUB="${KIZUNA_PLUGIN_PUBKEY:-$(dirname "$SIGNING_KEY")/plugin_signing.pub}"
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-}"
SEC_PLUGIN_DIR="${KIZUNA_SEC_PLUGIN_DIR:-}"

log() { echo "[build-all] $*"; }

log "1/3 host binaries"
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.8.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"
# メモリ最適化: -s -w でバイナリを約3割縮める（RSS の大半は
# バイナリのファイルマップページ）。ソースレベルデバッグが要るときは
# KIZUNA_DEBUG_BUILD=1 を付ける。
#
# -trimpath は host と全 plugin で必ず同じ設定にする（ここでは常時 ON）。
# 外すと、ビルドしたユーザーの GOMODCACHE 絶対パスが build ID に埋め込まれ、
# agent を sudo で、plugin を user でビルドしただけで ABI 不一致になる
# （実機で発生）。両方に付けば build ID がパス非依存になり一致する。
TRIMPATH="-trimpath"
if [ "${KIZUNA_DEBUG_BUILD:-0}" = "1" ]; then
    : # keep LDFLAGS as-is
else
    LDFLAGS="-s -w $LDFLAGS"
fi
PLUGIN_LDFLAGS="-s -w"
if [ "${KIZUNA_DEBUG_BUILD:-0}" = "1" ]; then
    PLUGIN_LDFLAGS=""
fi

# ----------------------------------------------------------------------------
# 重要: host と全プラグインを1つの combined go.work の下でビルドする。
# CRITICAL: build host AND plugins under ONE combined go.work.
#
# The host module carries a go.work, but a plugin directory (outside the repo)
# has none, so `go build` there falls back to the plugin's own
# `replace Kizuna-Eye => ...` directive. go.work and replace resolve the same
# source but produce a DIFFERENT build ID for every shared package
# (pkg/module, pkg/status, pkg/fsutil): the workspace changes how the module
# graph and the build flags are hashed. Mixing the two makes plugin.Open fail
# with
#     plugin was built with a different version of package Kizuna-Eye/pkg/fsutil
# even though both were built with the same toolchain and -trimpath (observed
# on the host 2026-10-10: a fresh plugin built with GOWORK=off would not load
# into a workspace-built agent).
#
# host と全プラグインディレクトリを `use` する単一の go.work が両グラフを
# 同一にし、build ID が一致する。workspace 外のプラグイン（ソースディレクトリ
# 未設定）は単にファイルに現れない。
# A single go.work that `use`s the host and every plugin directory makes both
# graphs identical, so the build IDs match. Plugins outside the workspace (no
# source dir configured) are simply absent from the file.
# ----------------------------------------------------------------------------
WORKFILE=""
cleanup_work() { [ -n "$WORKFILE" ] && rm -f "$WORKFILE"; }
trap cleanup_work EXIT
if [ -n "$SEC_PLUGIN_DIR" ] || [ -n "$LITE_PLUGIN_DIR" ]; then
    WORKFILE="$(mktemp /tmp/kizuna-build-XXXXXX.work)"
    # タブインデントのパスには echo ではなく printf を使う。一部の echo ビルトインは
    # 	 を展開せず、リテラルの "	" が残ると go がパスをワークファイルの
    # ディレクトリ（/tmp）相対で解決してしまう。
    # printf (not echo) for the tab-indented paths: some echo builtins do not
    # expand \t, which would leave a literal "\t" and make go resolve the path
    # relative to the work file's directory (/tmp).
    {
        printf 'go 1.27.1\n'
        printf 'toolchain %s\n\n' "${PINNED_GO}"
        printf 'use (\n'
        printf '\t%s\n' "$PWD"
        [ -n "$SEC_PLUGIN_DIR" ]  && [ -d "$SEC_PLUGIN_DIR" ]  && printf '\t%s\n' "$SEC_PLUGIN_DIR"
        [ -n "$LITE_PLUGIN_DIR" ] && [ -d "$LITE_PLUGIN_DIR" ] && printf '\t%s\n' "$LITE_PLUGIN_DIR"
        printf ')\n'
    } > "$WORKFILE"
    log "using combined go.work: $WORKFILE"
fi

mkdir -p "$BIN_DIR"
CGO_ENABLED=1 GOWORK=${WORKFILE:-auto} GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 GOWORK=${WORKFILE:-auto} GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 GOWORK=${WORKFILE:-auto} GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
CGO_ENABLED=1 GOWORK=${WORKFILE:-auto} GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
log "    host OK (version=${VERSION})"

build_plugin() {
    local name="$1" dir="$2"
    if [ -z "$dir" ]; then
        log "skip $name (source dir not set)"
        return 0
    fi
    if [ ! -d "$dir" ]; then
        log "WARN $name source dir missing: $dir"
        return 1
    fi
    log "2/3 plugin $name ($dir)"
    # host と同一の combined go.work の下でビルドする（上記の注意を参照）。
    # ここで GOWORK=off にするとプラグイン自身の `replace` にフォールバックし、
    # 共有パッケージの build ID が変わり、workspace でビルドした host に対して
    # plugin.Open が壊れる。combined workspace を作らなかった場合（プラグイン
    # ディレクトリ無し）は、プラグイン自身のモジュール（GOWORK=off）に戻す。
    # host と同一の combined go.work の下でビルドする（上記の注意を参照）。
    # ここで GOWORK=off にするとプラグイン自身の `replace` にフォールバックし、
    # 共有パッケージの build ID が変わり、workspace でビルドした host に対して
    # plugin.Open が壊れる。combined workspace を作らなかった場合は
    # プラグイン自身のモジュール（GOWORK=off）に戻す。
    # Build under the SAME combined go.work as the host (see the note above):
    # GOWORK=off here would fall back to the plugin.s `replace` and change the
    # shared-package build IDs, breaking plugin.Open against a workspace-built
    # host. When no combined workspace was created (no plugin dirs), fall back
    # to the plugin.s own module (GOWORK=off).
    ( cd "$dir" && GOWORK=${WORKFILE:-off} CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build -buildvcs=false ${TRIMPATH} -ldflags "$PLUGIN_LDFLAGS" -buildmode=plugin -o "$name.so" . )
    if [ ! -r "$SIGNING_KEY" ]; then
        log "WARN signing key not readable ($SIGNING_KEY); leaving $name.so unsigned"
    else
        "$SIGN_BIN" -embed "$dir/$name.so" -private-key "$SIGNING_KEY"
        "$SIGN_BIN" -verify "$dir/$name.so" -public-key "$SIGNING_PUB"
    fi
    mkdir -p "$DEPLOY_DIR"
    cp "$dir/$name.so" "$DEPLOY_DIR/$name.so"
    chmod 0644 "$DEPLOY_DIR/$name.so"
    log "    $name deployed"
}

build_plugin kizuna_security "$SEC_PLUGIN_DIR" || true
build_plugin kizuna_backup_lite "$LITE_PLUGIN_DIR" || true

log "3/3 done"
log "restart to apply: systemctl restart kizuna-eye-agent kizuna-dashboard (needs sudo)"
log "REMINDER: never deploy an agent built from new shared packages without rebuilding every plugin."
