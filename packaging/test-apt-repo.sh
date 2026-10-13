#!/bin/bash
# ============================================================
# APT リポジトリの検証
# ローカルの apt-repo/ を file:// で参照し、apt install kizuna-eye を
# （名前だけで）実行できるか隔離コンテナで確認する。
#
# 前提: packaging/apt-key.sh と packaging/apt-repo.sh 実行済み。root の docker。
# 使い方:
#   sudo ./packaging/test-apt-repo.sh [ubuntu:24.04]
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${1:-ubuntu:24.04}"
REPO="$ROOT/apt-repo"

if [ ! -d "$REPO" ]; then
    echo "❌ apt-repo/ がありません。先に ./packaging/apt-repo.sh を実行してください。" >&2
    exit 1
fi

echo "▶ image=$IMAGE"
docker run --rm -v "$REPO:/repo:ro" "$IMAGE" bash -c '
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq gnupg ca-certificates >/dev/null 2>&1

# 公開鍵を dearmor（バイナリ鍵束）して signed-by に登録する。
# signed-by は ASCII armored ではなく、dearmor 済みの鍵束を要求する。
gpg --dearmor < /repo/kizuna.gpg > /usr/share/keyrings/kizuna.gpg
chmod 0644 /usr/share/keyrings/kizuna.gpg

# リポジトリを追加
cat > /etc/apt/sources.list.d/kizuna.list <<EOF
# 公開時は https:// を使う。ここではローカル検証のみ。
deb [signed-by=/usr/share/keyrings/kizuna.gpg] file:///repo stable main
EOF

echo "=== apt update ==="
apt-get update -qq 2>&1 | tail -3
echo "=== apt-cache policy ==="
apt-cache policy kizuna-eye || true

# 小文字のパッケージ名が正しいことを確認
# ---= 名前だけでインストール =---
echo "=== apt install kizuna-eye ==="
apt-get install -y -qq kizuna-eye 2>&1 | tail -5

echo "=== dpkg -l ==="
dpkg -l kizuna-eye | tail -1

echo "=== user ==="
id kizuna-eye

echo "=== data ==="
ls -l /var/lib/kizuna-eye/data 2>&1 | head
'
