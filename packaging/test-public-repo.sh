#!/bin/bash
# ============================================================
# 公開 APT リポジトリの検証
# GitHub Pages に公開したリポジトリから、実際に apt install kizuna-eye を
# 実行できるか隔離コンテナで確認する（本番のユーザーと同じ手順）。
#
# 使い方（root の docker が必要）:
#   sudo ./packaging/test-public-repo.sh [ubuntu:20.04] [BASE_URL]
# ============================================================
set -euo pipefail

IMAGE="${1:-ubuntu:20.04}"
BASE="${2:-https://exiserverteam.github.io/Kizuna-Eye_forEN}"

echo "▶ image=$IMAGE base=$BASE"
docker run --rm "$IMAGE" bash -c "
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq gnupg ca-certificates curl >/dev/null 2>&1

# 公開鍵を dearmor して登録（ユーザー手順そのまま）
curl -fsSL $BASE/kizuna.gpg | gpg --dearmor -o /usr/share/keyrings/kizuna.gpg
chmod 0644 /usr/share/keyrings/kizuna.gpg

echo \"deb [signed-by=/usr/share/keyrings/kizuna.gpg] $BASE stable main\" \
  > /etc/apt/sources.list.d/kizuna.list

echo === apt update ===
apt-get update -qq 2>&1 | tail -3
echo === apt-cache policy ===
apt-cache policy kizuna-eye
echo === apt install kizuna-eye ===
apt-get install -y -qq kizuna-eye 2>&1 | tail -4
echo === dpkg -l ===
dpkg -l kizuna-eye | tail -1
echo === run binaries ===
/opt/kizuna-eye/bin/agent_linux --help 2>&1 | head -1
/opt/kizuna-eye/bin/dashboard_linux --help 2>&1 | head -1
"
