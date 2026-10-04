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
# コアを書くのはカーネルで、その権限は「クラッシュしたプロセス」のもの。
# root:root 0700 のままだと agent(kizuna-agent) も dashboard(user) も
# 書き込めず、core_pattern を変えた意味が消えてコアが 1 つも残らない。
# そこで sticky + 書き込み可（他者読み取り不可）のドロップボックスにする:
#   1733 = 所有者は読み書き、他は「作成のみ」（一覧・読み取りは不可）
# コア自身は 0600 で作られるため、内容は root しか読めない。
install -d -m 1733 "$CORE_DIR"

# 親ディレクトリは「通り抜け」だけ許可する。agent の home は HOME_MODE
# (既定 0750) で作られるため、そのままだと dashboard(user) が $CORE_DIR に
# 到達できず、ダッシュボード側のコアだけ取りこぼす。state/keys/logs は
# 0700 のままなので、中身は変わらず保護される（o+x は一覧も読み取りも
# 許可しない）。
PARENT="$(dirname "$CORE_DIR")"
if [ -d "$PARENT" ]; then
    chmod o+x "$PARENT"
    echo "   親を通り抜け可能に: $PARENT ($(stat -c '%a %U:%G' "$PARENT"))"
fi

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
