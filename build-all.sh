#!/bin/bash
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
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.7.1)}"
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
mkdir -p "$BIN_DIR"
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent
CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-sign" ./cmd/plugin-sign
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
    ( cd "$dir" && GOWORK=off CGO_ENABLED=1 GOTOOLCHAIN=${PINNED_GO} go build ${TRIMPATH} -ldflags "$PLUGIN_LDFLAGS" -buildmode=plugin -o "$name.so" . )
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
