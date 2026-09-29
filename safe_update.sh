#!/bin/bash
# ============================================================
# Kizuna-Eye safe_update.sh
# update.sh を「安全に」呼ぶラッパー。
#   1. update.sh を実行（バックアップ＋ビルド＋プラグインビルド）
#   2. 再起動してヘルスチェック（/health）
#   3. 失敗したら前のバイナリに戻して再起動
#
# 使い方:
#   ./safe_update.sh [--install] [--no-plugin] [--plugin-dir DIR]
# ============================================================
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
BACKUP_ROOT="${KIZUNA_BACKUP_DIR:-/opt/kizuna-eye/backup}"
HEALTH_URL="${KIZUNA_HEALTH_URL:-http://127.0.0.1:8080/health}"

# update.sh と同じ規則でバックアップ先を解決する（/opt が sudo 所有のときは
# HOME 配下へ退避）。ここがずれるとロールバック先を見失う。
if ! mkdir -p "$BACKUP_ROOT" 2>/dev/null || [ ! -w "$BACKUP_ROOT" ]; then
    BACKUP_ROOT="${KIZUNA_BACKUP_DIR:-$HOME/.kizuna-eye/backup}"
    mkdir -p "$BACKUP_ROOT" 2>/dev/null || true
fi

echo "▶ update.sh を実行..."
if ! ./update.sh "$@"; then
    echo "❌ update.sh が失敗しました（update.sh 側でロールバック済み）。"
    exit 1
fi

# update.sh が作った「直前のバイナリ」のバックアップ（最新）を取得する。
# update.sh 実行「後」に取るのが重要: 実行前に取ると一つ前の更新のバックアップを
# 指してしまい、失敗時にさらに古いバイナリへ戻す事故になる。
LATEST_BACKUP="$(ls -1dt "$BACKUP_ROOT"/*/ 2>/dev/null | head -n1 || true)"

# 再起動
echo "▶ 再起動..."
./stop.sh || true
./start.sh || true

# ヘルスチェック（最大30秒待つ）
echo "▶ ヘルスチェック: $HEALTH_URL"
ok=0
for i in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$HEALTH_URL" 2>/dev/null || echo 000)"
    if [ "$code" = "200" ]; then
        ok=1
        break
    fi
    # 503 は「agent 未接続」だがサーバー自体は生きている。起動は成功とみなす。
    if [ "$code" = "503" ]; then
        ok=1
        break
    fi
    sleep 1
done

if [ "$ok" -eq 1 ]; then
    echo "✅ アップデート成功（ヘルスチェックOK）"
    exit 0
fi

echo "❌ ヘルスチェック失敗。ロールバックします..."
if [ -n "$LATEST_BACKUP" ]; then
    for f in agent_linux dashboard_linux plugin-inspect; do
        [ -f "$LATEST_BACKUP/$f" ] && cp -a "$LATEST_BACKUP/$f" "$BIN_DIR/$f"
    done
    ./stop.sh || true
    ./start.sh || true
    echo "⚠️  前のバージョンへ戻しました。"
fi
exit 1
