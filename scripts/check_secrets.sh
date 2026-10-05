#!/bin/bash
# ============================================================
# 公開してよい履歴・作業ツリーに秘密情報が混ざっていないかの簡易チェック。
#
# 目的: 鍵・パスワード・実設定ファイルを公開リポジトリへ push する事故を防ぐ。
#       CI（.github/workflows/ci.yml）と手元の両方で実行できる。
#
# 検査内容:
#   1. 公開対象（リモート追跡ブランチ + タグ）の履歴に、秘密情報らしきファイル名が
#      含まれていないか（削除済みのパスも含む）
#   2. 公開対象の現在のツリーに、秘密情報らしき文字列パターンが無いか
#      （値は出力せず、該当ファイルのみを表示）
#   3. 公開対象の全履歴（diff 単位。削除済みファイルや過去のコミットも含む）に、
#      秘密情報らしき文字列パターンが無いか（該当コミットのみを表示）
#   4. 作業ツリーで追跡されているファイルに、秘密情報らしき名前が無いか
#   5. ローカル ref にだけ存在する秘密情報（警告のみ）
#      refs/cline/checkpoints/* のようなローカル専用 ref は通常 push されないが、
#      `git push --mirror` やフォルダごとのコピーで漏れる。検出した場合は
#      「git clone --single-branch した複製から push する」か、その ref を削除して
#      `git reflog expire --expire=now --all && git gc --prune=now` する。
#
# 注意: 1〜3 はヒューリスティック（パターン一致）です。0 件でも「絶対に秘密が
#       無い」ことの証明にはなりません。疑わしい変更は必ず目視で確認してください。
#
# 使い方: scripts/check_secrets.sh
# 終了コード: 0=問題なし / 1=要確認（検出あり）
# ============================================================
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR" || exit 1

fail=0

# 秘密情報らしきファイル名（実設定ファイル・鍵・環境ファイル）
NAME_RE='(^|/)(\.ssh_env|\.env|id_rsa|id_ed25519|users\.json|sessions\.json|agent_config\.json|dashboard_config\.json|chain\.key)$|\.(pem|key|p12|pfx|jks)$'

# 秘密情報らしき内容。高シグナルなパターンのみに絞り、README 等の
# プレースホルダ（例: KE_PASS=yourpassword、KE_PASS=...）では発火しないようにする。
CONTENT_RE="-----BEGIN [A-Z ]*PRIVATE KEY-----|xox[baprs]-[A-Za-z0-9]+|discord(app)?\.com/api/webhooks/[0-9]+/|api\.telegram\.org/bot[0-9]+:|gh[pousr]_[A-Za-z0-9]+|AKIA[0-9A-Z]+|KE_PASS=[\"'][^\"']+[\"']"


# --- 公開対象の ref を集める（リモート追跡 + タグ） ---
refs=()
while IFS= read -r r; do
    [ -n "$r" ] && refs+=("$r")
done < <(git for-each-ref --format='%(refname)' refs/remotes/ refs/tags/ 2>/dev/null)
if [ "${#refs[@]}" -eq 0 ]; then
    echo "⚠️  リモート追跡 ref / タグが見つかりません。HEAD のみを検査します。"
    refs=(HEAD)
fi
echo "▶ 公開対象の履歴を検査: ${refs[*]}"

# --- 1) ファイル名 ---
publish_names="$(git rev-list --objects "${refs[@]}" 2>/dev/null \
    | awk '{ $1=""; sub(/^ /, ""); print }' \
    | grep -E "$NAME_RE" | sort -u || true)"
if [ -n "$publish_names" ]; then
    echo "❌ 公開対象の履歴に秘密情報らしきファイル名があります:"
    echo "$publish_names" | sed 's/^/   /'
    fail=1
else
    echo "✅ 公開対象の履歴に秘密情報らしきファイル名なし"
fi

# --- 2) 内容（値は出さず、ヒットしたパスだけを出す） ---
content_hits="$(git grep -I -l -E "$CONTENT_RE" "${refs[@]}" 2>/dev/null | sort -u || true)"
if [ -n "$content_hits" ]; then
    echo "❌ 公開対象の内容に秘密情報らしき文字列があります:"
    echo "$content_hits" | sed 's/^/   /'
    echo "   （値は出力していません。該当箇所を確認してください）"
    fail=1
else
    echo "✅ 公開対象の内容に秘密情報らしき文字列なし"
fi

# --- 3) 内容（全履歴: 削除済みファイル・過去コミットも diff 単位で走査。値は出さない） ---
hist_hits="$(git log --format='%h %ad %s' --date=short -G"$CONTENT_RE" "${refs[@]}" 2>/dev/null | head -20 || true)"
if [ -n "$hist_hits" ]; then
    echo "❌ 公開対象の履歴（diff）に秘密情報らしき文字列があります:"
    echo "$hist_hits" | sed 's/^/   /'
    echo "   （該当コミットを確認し、必要なら値のローテーション + 履歴の書き換えを行う）"
    fail=1
else
    echo "✅ 公開対象の履歴（diff）に秘密情報らしき文字列なし"
fi

# --- 4) 作業ツリー（追跡ファイル） ---
tracked_hits="$(git ls-files | grep -E "$NAME_RE" | sort -u || true)"
if [ -n "$tracked_hits" ]; then
    echo "❌ 追跡されている秘密情報らしきファイルがあります（.gitignore を確認）:"
    echo "$tracked_hits" | sed 's/^/   /'
    fail=1
else
    echo "✅ 追跡ファイルに秘密情報らしきファイル名なし"
fi

# --- 5) ローカル ref のみに存在する秘密情報（警告） ---
local_only="$(git rev-list --objects --all 2>/dev/null \
    | awk '{ $1=""; sub(/^ /, ""); print }' \
    | grep -E "$NAME_RE" | sort -u || true)"
if [ -n "$local_only" ]; then
    warn_targets=""
    while IFS= read -r p; do
        [ -z "$p" ] && continue
        if ! printf '%s\n' "$publish_names" | grep -qxF "$p"; then
            warn_targets="${warn_targets}${p}"$'\n'
        fi
    done <<< "$local_only"
    if [ -n "$warn_targets" ]; then
        echo "⚠️  ローカル ref にだけ存在する秘密情報らしきパス（通常は push されません）:"
        printf '%s' "$warn_targets" | sed 's/^/   /'
        echo '   `git push --mirror` やフォルダコピーで漏れます。公開時は'
        echo '   `git clone --single-branch` した複製から push してください。'
    fi
fi

if [ "$fail" -eq 0 ]; then
    echo "✅ secrets: OK"
else
    echo "❌ secrets: NG"
fi
exit "$fail"
