#!/bin/bash
# ============================================================
# Kizuna-Eye systemd 移行スクリプト
# 開発中は start.sh / stop.sh で手動管理し、運用フェーズで
# systemd に切り替えるための補助。
#
# やること:
#   1. 手動起動中の dashboard/agent を stop.sh で停止
#   2. systemd unit を /etc/systemd/system に導入（User を埋める）
#   3. daemon-reload → enable --now
#   4. 以降は systemctl で管理（start.sh/stop.sh は使わない）
#
# 使い方:
#   sudo ./systemd/migrate-to-systemd.sh [実行ユーザー]
#   （省略時は sudo 実行者 $SUDO_USER）
# ============================================================
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."   # リポジトリ直下へ

if [ "$(id -u)" -ne 0 ]; then
    echo "❌ root で実行してください: sudo $0 [user]"
    exit 1
fi

RUN_USER="${1:-${SUDO_USER:-}}"
if [ -z "$RUN_USER" ] || [ "$RUN_USER" = "root" ]; then
    echo "❌ 実行ユーザーを指定してください: sudo $0 <user>"
    exit 1
fi

UNIT_SRC="systemd/kizuna-eye.service"
UNIT_DST="/etc/systemd/system/kizuna-eye.service"
[ -f "$UNIT_SRC" ] || { echo "❌ $UNIT_SRC がありません"; exit 1; }

# 1. 手動プロセスを停止（PID ファイル方式なので安全に止まる）
if [ -x ./stop.sh ]; then
    echo "▶ 手動プロセスを停止..."
    sudo -u "$RUN_USER" ./stop.sh || true
    sleep 1
fi

# 2. unit 導入
echo "▶ systemd unit を導入 (User=$RUN_USER)"
sed "s/__USER__/$RUN_USER/g" "$UNIT_SRC" > "$UNIT_DST"
chmod 0644 "$UNIT_DST"

# 3. 有効化
systemctl daemon-reload
systemctl enable --now kizuna-eye
sleep 2
systemctl --no-pager --full status kizuna-eye || true

echo ""
echo "✅ systemd へ移行しました。以降の操作:"
echo "   systemctl status kizuna-eye"
echo "   systemctl restart kizuna-eye"
echo "   systemctl stop kizuna-eye"
echo "   journalctl -u kizuna-eye -f"
echo "   （start.sh/stop.sh は使わないでください）"
