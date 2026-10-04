#!/bin/bash
# ============================================================
# Kizuna-Eye コアダンプ採取の設定（要 sudo・対話実行）
#
# やること:
#   1. コア置き場 /var/lib/kizuna-eye/coredump (0700) を作成
#   2. /etc/sysctl.d/60-kizuna-core.conf を導入（既存は /tmp へバックアップ）
#   3. sysctl --system で反映
#   4. 反映結果を表示
#
# ロールバック:
#   sudo rm /etc/sysctl.d/60-kizuna-core.conf && sudo sysctl --system
#   （バックアップ: /tmp/60-kizuna-core.conf.bak-<ts> があれば戻す）
#
# 副作用:
#   - core_pattern が apport パイプからファイル出力に変わる。
#     /var/crash への apport レポートは生成されなくなる（クラッシュ解析は
#     本スクリプトが作るコア + journalctl で行う）。
#   - fs.suid_dumpable=1 は setuid プロセスのコアも許可する。Kizuna-Eye 本体は
#     非 setuid なので必須ではないが、他 setuid バイナリのコアが残り得る点に注意。
#   - コアは数 GB になり得る。fs の空き容量に注意（Kizuna-Eye が監視もする）。
# ============================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "❌ root で実行してください: sudo $0"
    exit 1
fi

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONF_SRC="$SRC_DIR/60-kizuna-core.conf"
CONF_DST="/etc/sysctl.d/60-kizuna-core.conf"
CORE_DIR="/var/lib/kizuna-eye/coredump"
TS="$(date +%Y%m%d_%H%M%S)"

[ -f "$CONF_SRC" ] || { echo "❌ $CONF_SRC が見つかりません"; exit 1; }

echo "▶ 1. コア置き場を作成: $CORE_DIR"
install -d -m 700 "$CORE_DIR"

echo "▶ 2. sysctl 設定を導入: $CONF_DST"
if [ -f "$CONF_DST" ]; then
    cp -a "$CONF_DST" "/tmp/60-kizuna-core.conf.bak-$TS"
    echo "   既存を退避: /tmp/60-kizuna-core.conf.bak-$TS"
fi
install -m 644 "$CONF_SRC" "$CONF_DST"

echo "▶ 3. 反映 (sysctl --system)"
sysctl --system >/dev/null

echo "▶ 4. 現在値"
echo -n "   kernel.core_pattern = "; cat /proc/sys/kernel/core_pattern
echo -n "   kernel.core_uses_pid = "; cat /proc/sys/kernel/core_uses_pid
echo -n "   fs.suid_dumpable = "; cat /proc/sys/fs/suid_dumpable
echo ""
echo "✅ 完了。systemd unit 側の LimitCORE=infinity / GOTRACEBACK=crash と"
echo "   併せて、次回クラッシュ時に $CORE_DIR へコアが残ります。"
echo "   既存プロセスに反映するには再起動が必要です:"
echo "     sudo systemctl restart kizuna-agent kizuna-dashboard"
