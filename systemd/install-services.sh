#!/bin/bash
# ============================================================
# Kizuna-Eye dashboard / agent を systemd 管理下に置く（要 sudo・対話実行）
#
# なぜ必要か:
#   既存の kizuna-eye.service は Type=oneshot + start.sh のため、start.sh が
#   背景起動した子プロセスが SIGSEGV で落ちても systemd から見ると「active」の
#   ままになり、自動再起動されない（監視が静かに止まる）。
#   本スクリプトは dashboard / agent を別々の Type=simple unit にし、
#   Restart=on-failure / RestartSec=5 / LimitCORE=infinity を与える。
#
# 使い方:
#   sudo systemd/install-services.sh [実行ユーザー] [BIN_DIR]
#   （実行ユーザー省略時は sudo 実行者。BIN_DIR 既定 /opt/kizuna-eye/bin）
#
# 副作用・注意:
#   - 手動起動中の dashboard/agent を stop.sh で停止してから移行する。
#   - 旧 kizuna-eye.service が有効なら、二重起動を避けるため disable する。
#   - 秘密 (chain.key / *_config.json / modules.json) は共有外にある前提。
# ロールバック:
#   sudo systemctl disable --now kizuna-dashboard kizuna-agent
#   sudo rm /etc/systemd/system/kizuna-{dashboard,agent}.service
#   sudo systemctl daemon-reload
#   ./start.sh   # 従来の手動起動へ戻す
# ============================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "❌ root で実行してください: sudo $0 [user] [BIN_DIR]"
    exit 1
fi

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # リポジトリ直下
RUN_USER="${1:-${SUDO_USER:-}}"
if [ -z "$RUN_USER" ] || [ "$RUN_USER" = "root" ]; then
    echo "❌ 実行ユーザーを指定してください: sudo $0 <user> [BIN_DIR]"
    exit 1
fi
BIN_DIR="${2:-/opt/kizuna-eye/bin}"

RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
if [ -f "$RUN_HOME/.kizuna-eye/data/dashboard_config.json" ]; then
    DATA_DIR="$RUN_HOME/.kizuna-eye/data"
else
    DATA_DIR="$DIR"
fi

echo "▶ 設定"
echo "   repo       : $DIR"
echo "   user       : $RUN_USER ($RUN_HOME)"
echo "   bin        : $BIN_DIR"
echo "   data       : $DATA_DIR"

TS="$(date +%Y%m%d_%H%M%S)"

# 1. 手動プロセスの停止（PID ファイル方式なので安全）
if [ -x "$DIR/stop.sh" ]; then
    echo "▶ 手動プロセスを停止..."
    sudo -u "$RUN_USER" "$DIR/stop.sh" || true
    sleep 1
fi

# 2. 旧 kizuna-eye.service との競合を避ける
if systemctl list-unit-files kizuna-eye.service >/dev/null 2>&1 \
   && systemctl is-enabled --quiet kizuna-eye 2>/dev/null; then
    echo "▶ 旧 kizuna-eye.service を disable（二重起動防止）"
    systemctl disable --now kizuna-eye || true
fi

# 3. unit を導入
for u in kizuna-dashboard kizuna-agent; do
    SRC="$DIR/systemd/$u.service"
    DST="/etc/systemd/system/$u.service"
    [ -f "$SRC" ] || { echo "❌ $SRC が見つかりません"; exit 1; }
    if [ -f "$DST" ]; then
        cp -a "$DST" "/tmp/$u.service.bak-$TS"
        echo "   既存 unit を退避: /tmp/$u.service.bak-$TS"
    fi
    sed -e "s|__USER__|$RUN_USER|g" \
        -e "s|__DIR__|$DIR|g" \
        -e "s|__BIN__|$BIN_DIR|g" \
        -e "s|__DATA__|$DATA_DIR|g" \
        "$SRC" > "$DST"
    chmod 0644 "$DST"
    echo "▶ 導入: $DST"
done

# 4. 有効化
systemctl daemon-reload
systemctl enable --now kizuna-dashboard kizuna-agent
sleep 2
systemctl --no-pager --full status kizuna-dashboard kizuna-agent || true

echo ""
echo "✅ 完了。以降の操作:"
echo "   systemctl status kizuna-agent kizuna-dashboard"
echo "   journalctl -u kizuna-agent -f"
echo "   systemctl restart kizuna-agent"
echo "   （start.sh / stop.sh は使わないでください）"
echo ""
echo "次の推奨: sudo systemd/setup-coredump.sh"
