#!/bin/bash
# ============================================================
# Kizuna-Eye E2E（Docker 再現手順）
#
# 固定ツールチェーン（go1.27.2 の公式イメージ）のコンテナ内で
# ビルド + API/WS スモーク（scripts/verify.sh --smoke）を実行する。
# ホストに Go が無くても、CI と同じ土台で検証できる。
#
# 使い方:
#   ./scripts/e2e.sh                 # 既定 golang:1.27-bookworm
#   KIZUNA_E2E_IMAGE=golang:1.24-bookworm ./scripts/e2e.sh
#
# 前提: docker（root 相当）が使えること。
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
docker run --rm -v "$ROOT":/src:ro "$IMAGE" bash -euo pipefail -c '
  cp -a /src /work && cd /work
  # CGO/race に必要なビルドツールと、スモークに使う curl を導入。
  apt-get update -qq >/dev/null 2>&1
  apt-get install -y -qq --no-install-recommends build-essential curl ca-certificates >/dev/null 2>&1
  export GOTOOLCHAIN=local
  export BIN_DIR=/work/out
  ./scripts/verify.sh --smoke
'

echo "✅ E2E 完了"
