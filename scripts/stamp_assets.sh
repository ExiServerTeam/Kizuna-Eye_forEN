#!/bin/bash
# ============================================================
# Kizuna-Eye stamp_assets.sh
#
# web/static/*.html 内のローカルアセット参照の ?v= を、その
# ファイル内容の短いハッシュに置き換える。
#
# なぜ必要か:
#   stamp_version.sh は ?v= をリリース版（VERSION）に揃える。
#   リリース間で JS/CSS を編集しても VERSION が同じ間は
#   ブラウザが古いキャッシュを使い続け、修正が反映されない。
#   内容ハッシュにすれば、ファイルが変わったときだけ URL が
#   変わり、キャッシュが確実に無効化される。
#
# 使い方:
#   ./scripts/stamp_assets.sh [static_dir]
#   （既定は web/static。配信ディレクトリにも同じものを適用する）
#
# 冪等: 同じ内容なら 2 回目は差分ゼロ。
# ============================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATIC_DIR="${1:-$ROOT_DIR/web/static}"

if [ ! -d "$STATIC_DIR" ]; then
    echo "❌ static dir not found: $STATIC_DIR" >&2
    exit 1
fi

# hash8 prints the first 8 hex chars of the file's sha256 (content-addressed).
hash8() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -c1-8
    else
        shasum -a 256 "$1" | cut -c1-8
    fi
}

changed=0
for html in "$STATIC_DIR"/*.html; do
    [ -f "$html" ] || continue
    before="$(cat "$html")"
    # For every local href/src="...?v=...", recompute the hash of the target
    # file and substitute it. External URLs (http/https/protocol-relative) are
    # skipped: their content is not ours to hash.
    while IFS= read -r ref; do
        # ref is e.g. /app.js?v=0.7.1 ; split path and drop the query
        path="${ref%%\?*}"
        # Only local, non-external references.
        case "$path" in
            http://*|https://*|//*) continue ;;
        esac
        # Resolve /foo.js to <dir>/foo.js and foo.js to <dir>/foo.js.
        rel="${path#/}"
        target="$STATIC_DIR/$rel"
        [ -f "$target" ] || continue
        h="$(hash8 "$target")"
        # Replace the query value after ?v= for exactly this path.
        # Use a literal match on the path so unrelated ?v= are untouched.
        before2="$(cat "$html")"
        perl -0pi -e "s{(\Q$path\E\?v=)[^\"'<>]*}{\${1}$h}g" "$html" 2>/dev/null \
            || sed -i -E "s#(\Q$path\E\?v=)[^\"'<>]*#\1$h#g" "$html"
        if [ "$before2" != "$(cat "$html")" ]; then
            : # changed; counted below
        fi
    done < <(grep -oE '(href|src)="[^"]+\?v=[^"]*"' "$html" | sed -E 's/^(href|src)="//; s/"$//')
    if [ "$before" != "$(cat "$html")" ]; then
        changed=$((changed+1))
    fi
done

echo "✅ asset cache-buster stamped in ${STATIC_DIR} (updated HTML: ${changed})"
