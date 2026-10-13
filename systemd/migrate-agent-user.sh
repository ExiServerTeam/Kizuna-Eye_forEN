#!/bin/bash
# ============================================================
# A-4: agent を専用ユーザー kizuna-agent へ移行する（要 sudo・対話実行）
#
# 目的:
#   agent を運用アカウント `user`(uid=1000) から切り離す。uid=1000 が
#   奪われても FIM ベースライン / チェーン鍵 / アラート状態を改変できない。
#   併せて AmbientCapabilities=CAP_DAC_READ_SEARCH で /etc/shadow 等を
#   読めるようにし、A-2 の「権限不足 INFO」も解消する。
#
# 使い方:
#   sudo systemd/migrate-agent-user.sh [旧ユーザー]
#   （旧ユーザー省略時は sudo 実行者。状態ファイルの移行元）
#
# 前提: systemd/kizuna-agent-a4.service が本リポジトリにあること。
#
# ロールバック:
#   sudo systemctl disable --now kizuna-agent
#   sudo cp -a <backup>/... (下で表示する tar) を展開して chown user:user
#   従来どおり ./start.sh で起動
# ============================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "❌ root で実行してください: sudo $0 [旧ユーザー]"
    exit 1
fi

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OLD_USER="${1:-${SUDO_USER:-}}"
if [ -z "$OLD_USER" ] || [ "$OLD_USER" = "root" ]; then
    echo "❌ 旧ユーザーを指定してください: sudo $0 <user>"
    exit 1
fi
OLD_HOME="$(getent passwd "$OLD_USER" | cut -d: -f6)"
SRC_DATA="$OLD_HOME/.kizuna-eye/data"
BIN_DIR="/opt/kizuna-eye/bin"

NEW_USER="kizuna-agent"
NEW_HOME="/var/lib/kizuna-eye"
TS="$(date +%Y%m%d_%H%M%S)"
BACKUP="/tmp/kizuna-a4-backup-$TS.tar.gz"

echo "▶ 0. バックアップ"
tar czf "$BACKUP" -C / \
    "${SRC_DATA#/}" \
    "${BIN_DIR#/}"/*.meta.json 2>/dev/null || true
# 状態ファイルは repo logs にもあるため併せて退避
tar rzf "$BACKUP" -C "$DIR" logs 2>/dev/null || true
echo "   $BACKUP"
echo "   ロールバックはこの tar を展開し、元の所有者へ chown して start.sh で起動します。"

echo "▶ 1. ユーザー作成 (存在すればスキップ)"
if id "$NEW_USER" >/dev/null 2>&1; then
    echo "   既存: $(id "$NEW_USER")"
else
    useradd --system --home "$NEW_HOME" --create-home --shell /usr/sbin/nologin "$NEW_USER"
    echo "   作成: $(id "$NEW_USER")"
fi

echo "▶ 2. ディレクトリ作成"
install -d -o "$NEW_USER" -g "$NEW_USER" -m 0700 "$NEW_HOME"/{state,keys,logs}

echo "▶ 3. 設定・状態ファイルを移行"
# 設定 (agent_config.json / modules.json)
for f in agent_config.json modules.json; do
    [ -f "$SRC_DATA/$f" ] && install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$SRC_DATA/$f" "$NEW_HOME/state/$f"
done

# 状態ファイル類（FIM/ports/suid/cron/logins/logstate/alertstate）
for f in "$SRC_DATA"/kizuna-security-*.json; do
    [ -e "$f" ] || continue
    install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$f" "$NEW_HOME/state/$(basename "$f")"
done
# repo の logs/ 側にも状態がある場合の取りこぼし防止
for f in "$DIR"/logs/kizuna-security-*.json; do
    [ -e "$f" ] || continue
    bn="$(basename "$f")"
    [ -f "$NEW_HOME/state/$bn" ] || install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$f" "$NEW_HOME/state/$bn"
done

# チェーン鍵
if [ -f "$SRC_DATA/keys/chain.key" ]; then
    install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$SRC_DATA/keys/chain.key" "$NEW_HOME/keys/chain.key"
fi

# modules.json の chain_key_path を新パスへ書き換え（旧→新）
OLD_KEY_PATH="$SRC_DATA/keys/chain.key"
NEW_KEY_PATH="$NEW_HOME/keys/chain.key"
if grep -q "$OLD_KEY_PATH" "$NEW_HOME/state/modules.json" 2>/dev/null; then
    sed -i "s|$OLD_KEY_PATH|$NEW_KEY_PATH|g" "$NEW_HOME/state/modules.json"
    echo "   chain_key_path を書き換え: $OLD_KEY_PATH -> $NEW_KEY_PATH"
else
    echo "   ℹ️ modules.json に $OLD_KEY_PATH が見つかりません（chain_key_path の手動確認推奨）"
fi

# 状態ファイルのパス（integrity_baseline_path / log_path）は cwd 相対のため
# /samba/share/Kizuna-Eye/logs のままでも動く（ReadWritePaths に含む）。

echo "▶ 4. unit 導入 (kizuna-agent.service = A-4 版)"
SRC_UNIT="$DIR/systemd/kizuna-agent-a4.service"
DST_UNIT="/etc/systemd/system/kizuna-agent.service"
[ -f "$SRC_UNIT" ] || { echo "❌ $SRC_UNIT が見つかりません"; exit 1; }
if [ -f "$DST_UNIT" ]; then
    cp -a "$DST_UNIT" "/tmp/kizuna-agent.service.bak-$TS"
    echo "   既存を退避: /tmp/kizuna-agent.service.bak-$TS"
fi
sed -e "s|__DIR__|$DIR|g" -e "s|__BIN__|$BIN_DIR|g" "$SRC_UNIT" > "$DST_UNIT"
chmod 0644 "$DST_UNIT"

echo "▶ 5. 旧 agent を停止"
systemctl disable --now kizuna-agent 2>/dev/null || true
if [ -x "$DIR/stop.sh" ]; then
    sudo -u "$OLD_USER" "$DIR/stop.sh" || true
fi
sleep 1

echo "▶ 6. 起動"
systemctl daemon-reload
systemctl enable --now kizuna-agent
sleep 2
systemctl --no-pager --full status kizuna-agent || true

echo ""
echo "✅ 移行完了。確認してください:"
echo "   - systemctl status kizuna-agent"
echo "   - journalctl -u kizuna-agent -n 50"
echo "   - ダッシュボード GET /api/status とアラート/Discord 通知"
echo "   - FIM の権限 INFO が消えること（CAP_DAC_READ_SEARCH の効果）"
echo "   - backup プラグインが /samba/share/CD へ書けること"
echo "   ロールバック用バックアップ: $BACKUP"
