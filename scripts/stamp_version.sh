#!/bin/bash
# ============================================================
# Kizuna-Eye stamp_version.sh
# VERSION（リポジトリ直下・単一の真実源）を web/static に一括反映する。
#
#   ?v=X.Y.Z      … CSS/JS のキャッシュバスター
#   vX.Y.Z        … version-badge / footer の表示
#   const VERSION … app.js / i18n.js の内部定数
#
# これらを手で更新すると必ずずれる。リリース時は
#   1) VERSION を書き換える
#   2) ./scripts/stamp_version.sh を実行する
#   3) git diff で差分を確認してコミット
# の順で行う。
# ============================================================
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ ! -f VERSION ]; then
    echo "❌ VERSION ファイルが見つかりません（リポジトリ直下）" >&2
    exit 1
fi

RAW="$(tr -d '[:space:]' < VERSION)"      # 例: v0.7.0
# キャッシュバスター用の数字だけの版（先頭の v を除く）
NUM="${RAW#v}"

if [ -z "$RAW" ]; then
    echo "❌ VERSION が空です" >&2
    exit 1
fi

changed=0
for f in web/static/*.html; do
    before="$(cat "$f")"
    # ?v=... を現在の版に揃える（既存の値を問わず置換）。
    sed -i -E "s/\?v=[0-9]+\.[0-9]+\.[0-9]+/?v=${NUM}/g" "$f"
    # version-badge / footer の表示版を揃える。
    sed -i -E "s/v[0-9]+\.[0-9]+\.[0-9]+/${RAW}/g" "$f"
    [ "$before" != "$(cat "$f")" ] && changed=$((changed+1))
done

for f in web/static/app.js web/static/i18n.js; do
    [ -f "$f" ] || continue
    before="$(cat "$f")"
    sed -i -E "s/const VERSION = 'v[0-9]+\.[0-9]+\.[0-9]+'/const VERSION = '${RAW}'/" "$f"
    [ "$before" != "$(cat "$f")" ] && changed=$((changed+1))
done

echo "✅ VERSION=${RAW} を web/static に反映しました（更新ファイル: ${changed}）"
