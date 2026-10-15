#!/bin/bash
# ============================================================
# Kizuna-Eye E2E（Docker 再現手順）
# Docker-based E2E (reproducible run).
#
# 固定ツールチェーンのコンテナ内で ビルド + 起動 + API/WS スモーク
# （scripts/verify.sh --smoke）を実行する。ホストに Go が無くても、CI と
# 同じ土台で検証できる。
#
# 使い方:
#   ./scripts/e2e.sh                 # 既定 golang:1.27-bookworm
#   KIZUNA_E2E_IMAGE=golang:1.27.2-bookworm ./scripts/e2e.sh
#
# 前提: docker（root 相当）が使えること。
#
# Run build + start + API/WS smoke (scripts/verify.sh --smoke) inside a
# container with the pinned toolchain, so the run is reproducible and does
# not depend on the host's Go version.
#
# Usage:
#   ./scripts/e2e.sh                 # defaults to golang:1.27-bookworm
#   KIZUNA_E2E_IMAGE=golang:1.27.2-bookworm ./scripts/e2e.sh
#
# Requires: docker (root-equivalent).
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${KIZUNA_E2E_IMAGE:-golang:1.27-bookworm}"

if ! command -v docker >/dev/null 2>&1; then
    echo "❌ docker が必要です" >&2
    exit 1
fi

echo "▶ E2E image=$IMAGE"
# ソースは ro で渡し、コンテナ内の作業コピーでビルドする（ホストのツリーを汚さない）。
# Mount the source read-only and build in an in-container copy so the host
# tree is never modified.
docker run --rm -v "$ROOT":/src:ro "$IMAGE" bash -euo pipefail -c '
  cp -a /src /work && cd /work
  # CGO/race に必要なビルドツールと、スモークに使う curl を導入。
  apt-get update -qq >/dev/null 2>&1
  apt-get install -y -qq --no-install-recommends build-essential curl ca-certificates >/dev/null 2>&1
  # イメージタグが go.mod/toolchain の固定と異なる 1.27.x に解決し得るため、
  # Go に正確な固定パッチを取得させる（host と plugin で同一ツールチェーンが必須）。
  # The image tag may resolve to a different 1.27.x patch than the
  # go.mod/toolchain pin, so let Go fetch the exact pinned patch (host and
  # plugins must share one toolchain).
  export GOTOOLCHAIN=auto
  export BIN_DIR=/work/out
  # スモーク用の最小設定を example から生成する（トークン一致・署名不要・通知なし）。
  # Generate a minimal smoke config from the examples (matched token, no
  # signature requirement, notifications off).
  ./scripts/smoke-config.sh /work/smoke-data
  export KIZUNA_DATA_DIR=/work/smoke-data
  ./scripts/verify.sh --smoke
'

echo "✅ E2E 完了"
