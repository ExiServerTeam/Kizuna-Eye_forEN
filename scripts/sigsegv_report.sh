#!/bin/bash
# ============================================================
# SIGSEGV 相関分析レポート（sudo 不要）
#
# やること:
#   1. ログ中の "unexpected return pc" / "SIGSEGV" を数える
#   2. 各クラッシュの直前にある「最後のタイムスタンプ付き行」を抽出し、
#      「どのセキュリティイベント送信の直後に落ちたか」を時系列で出す
#   3. coredumpctl / journalctl からコア・スタックの有無を出す
#
# 使い方:
#   scripts/sigsegv_report.sh
# ============================================================
set -u
cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." || exit 1

DATA_LOGS="$HOME/.kizuna-eye/data/logs"
PAT='unexpected return pc'

echo "=================================================="
echo " Kizuna-Eye SIGSEGV レポート  $(date '+%Y-%m-%d %H:%M:%S')"
echo "=================================================="

scan() {
    local f="$1"
    [ -f "$f" ] || return 0
    local n
    n="$(grep -c "$PAT" "$f" 2>/dev/null || true)"
    [ "${n:-0}" -eq 0 ] && return 0
    echo ""
    echo "--- $f : $n hits ---"
    # クラッシュ行の行番号を取得し、直前 6 行から直近のタイムスタンプ行を探す
    grep -n "$PAT" "$f" | cut -d: -f1 | while read -r ln; do
        [ -z "$ln" ] && continue
        start=$(( ln > 6 ? ln - 6 : 1 ))
        ctx="$(sed -n "${start},$((ln-1))p" "$f")"
        ts="$(printf '%s\n' "$ctx" | grep -oE '[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]+' | tail -1)"
        ev="$(printf '%s\n' "$ctx" | grep -E 'セキュリティイベント送信|送信失敗|アラート|sudo|SSH|FIM' | tail -1 | cut -c1-160)"
        echo "  crash@line $ln  last_ts=${ts:-?}"
        [ -n "$ev" ] && echo "    prev: $ev"
    done
}

for pat in "logs/agent.log" "logs/agent.log.1" "logs/agent.log.2" "logs/agent.log.3" \
           "logs/dashboard.log" "logs/dashboard.log.1" \
           "$DATA_LOGS/agent.log" "$DATA_LOGS/dashboard.log"; do
    scan "$pat"
done

echo ""
echo "== coredumpctl (systemd-coredump 導入時のみ) =="
if command -v coredumpctl >/dev/null 2>&1; then
    coredumpctl list 2>/dev/null | grep -iE 'kizuna|agent_linux|dashboard_linux' | tail -20 \
        || echo "  (該当コアなし)"
else
    echo "  coredumpctl なし"
fi

echo ""
echo "== journalctl (systemd unit 導入後のみ) =="
if command -v journalctl >/dev/null 2>&1; then
    for u in kizuna-agent kizuna-dashboard; do
        systemctl is-active --quiet "$u" 2>/dev/null || continue
        echo "  --- $u ---"
        journalctl -u "$u" --no-pager -n 200 2>/dev/null \
            | grep -E 'SIGSEGV|unexpected return pc|panic|fatal error' | tail -10 \
            || echo "  (該当行なし)"
    done
fi

echo ""
echo "（dmesg は kernel.dmesg_restrict=1 のため要 sudo: sudo dmesg | grep -i segfault）"
