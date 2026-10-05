#!/bin/bash
# ============================================================
# Kizuna-Eye .deb を「古い Ubuntu」コンテナ内でビルドする。
#
# 26.04 で直接ビルドすると glibc 2.43 にリンクされ、20.04 で動かない。
# コンテナ内（例: ubuntu:20.04 = glibc 2.31）でビルドすれば、その
# libc にリンクされ、20.04 以降で動く .deb ができる。
#
# 使い方:
#   sudo ./packaging/docker-build-deb.sh [ubuntu:20.04]
#   → dist/kizuna-eye_<ver>_amd64.deb（古い libc 対応）
#
# 注意: docker は root 権限が必要（sudo で実行）。
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${1:-ubuntu:20.04}"
GO_VER="${KIZUNA_GO_VER:-1.27.1}"
RUN_UID="$(id -u)"
RUN_GID="$(id -g)"

echo "[docker-build] image=$IMAGE go=$GO_VER"

# コンテナ内でビルド。最後に生成物の所有者を実行ユーザーへ戻す
# （SMB 共有で root 所有が残ると git が壊れるため）。
docker run --rm -v "$ROOT":/src -w /src "$IMAGE" bash -c "
set -e
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq build-essential git curl ca-certificates >/dev/null 2>&1

# 公式 Go を導入（apt の golang は古く 1.27.1 を満たさない）
if [ ! -x /usr/local/go/bin/go ]; then
  curl -fsSL https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz -o /tmp/go.tgz
  tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:\$PATH
export GOTOOLCHAIN=local
export GOFLAGS=-buildvcs=false

go version
./build.sh
./packaging/build-deb.sh
"

# 生成物の所有者を実行ユーザーへ（root 所有の残骸を防ぐ）
if [ "$RUN_UID" -ne 0 ]; then
    chown -R "$RUN_UID:$RUN_GID" "$ROOT/dist" 2>/dev/null || true
fi

echo "[docker-build] 完了: $ROOT/dist"
ls -l "$ROOT/dist" 2>/dev/null || true
