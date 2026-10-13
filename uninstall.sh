#!/bin/bash
# ============================================================
# Kizuna-Eye uninstall.sh
# install.sh が導入したものを安全に撤去する。
#
# 既定（データ温存）:
#   - systemd サービス停止 + disable
#   - unit ファイル削除（kizuna-eye-agent / kizuna-dashboard / 旧 unit）
#   - sudoers（smartctl / cron / action）削除
#   - ヘルパー（/usr/local/bin/kizuna-*.sh）削除
#   - /etc/kizuna-eye（署名公開鍵）削除
#   - /etc/sysctl.d/60-kizuna-core.conf 削除
#   ※ ユーザー kizuna-eye / データ / バイナリは残す（再インストールを容易に）
#
# --purge（完全削除）:
#   - 上記に加えて
#   - /var/lib/kizuna-eye（状態・鍵・ログ）削除
#   - ~/.kizuna-eye（設定・署名鍵・バックアップ）削除
#   - /opt/kizuna-eye（バイナリ・プラグイン）削除
#   - repo logs / plugins の生成物削除
#   - ユーザー/グループ kizuna-eye 削除
#
# 使い方:
#   sudo ./uninstall.sh            # データ温存でサービスだけ撤去
#   sudo ./uninstall.sh --purge    # 完全削除（確認あり）
#   sudo ./uninstall.sh --purge --yes   # 確認なし
#   ./uninstall.sh --dry-run       # 実行内容の表示のみ（変更しない）
# ============================================================
set -u
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
STATE_ROOT="/var/lib/kizuna-eye"
ETC_DIR="/etc/kizuna-eye"
SYSCTL_CONF="/etc/sysctl.d/60-kizuna-core.conf"
NEW_USER="kizuna-eye"
REPO_LOGS="$PWD/logs"
REPO_PLUGINS="$PWD/plugins"

PURGE=0
ASSUME_YES=0
DRY_RUN=0
for arg in "$@"; do
    case "$arg" in
        --purge) PURGE=1 ;;
        --yes|-y) ASSUME_YES=1 ;;
        --dry-run) DRY_RUN=1 ;;
        -h|--help)
            echo "使い方: $0 [--purge] [--yes] [--dry-run]"
            echo "  --purge   データとユーザーも削除（完全アンインストール）"
            echo "  --yes     確認プロンプトをスキップ"
            echo "  --dry-run 実行内容の表示のみ"
            exit 0 ;;
        *) echo "不明な引数: $arg" >&2; exit 1 ;;
    esac
done

# ログ表示。"▶ 見出し" の行は手順として自動採番し、区切り線で囲む。
STEP_NO=0
log() {
    case "${1:-}" in
        "▶ "*)
            STEP_NO=$((STEP_NO+1))
            echo "──────────────────────────────────────────────────"
            echo " 【手順 ${STEP_NO}】${1#▶ }"
            echo "──────────────────────────────────────────────────"
            ;;
        *) echo "$*" ;;
    esac
}
warn() { echo "⚠️  $*"; }
err()  { echo "❌ $*"; }
ok()   { echo "✅ $*"; }

# run <cmd...>: dry-run 時は表示だけ。
run() {
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "   [dry-run] $*"
        return 0
    fi
    "$@"
}

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
    err "root で実行してください: sudo $0 $*"
    exit 1
fi

# sudo 実行時の実ユーザー（データ削除の対象ホーム解決に使う）
RUN_USER="${SUDO_USER:-$(id -un)}"
RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
[ -n "$RUN_HOME" ] || RUN_HOME="$HOME"

echo ""
echo "╔══════════════════════════════════════════════════╗"
echo "║          Kizuna-Eye アンインストール              ║"
echo "╚══════════════════════════════════════════════════╝"
if [ "$PURGE" -eq 1 ]; then log " モード: 完全削除（--purge）"; else log " モード: データ温存"; fi
[ "$DRY_RUN" -eq 1 ] && log " (dry-run: 変更しません)"

# ---- 確認 ----
if [ "$ASSUME_YES" -eq 0 ] && [ "$DRY_RUN" -eq 0 ]; then
    if [ "$PURGE" -eq 1 ]; then
        warn "--purge はデータ・鍵・ユーザーを完全に削除します。復元できません。"
    fi
    printf "続行しますか? [y/N] "
    read -r ans
    case "$ans" in
        y|Y|yes|YES) ;;
        *) echo "中止しました。"; exit 0 ;;
    esac
fi

# ---- 1. systemd サービス停止 + disable ----
log ""
log "▶ systemd サービスを停止・disable"
UNITS="kizuna-eye-agent kizuna-dashboard kizuna-agent kizuna-eye"
if command -v systemctl >/dev/null 2>&1; then
    for u in $UNITS; do
        if systemctl list-unit-files "$u.service" >/dev/null 2>&1 \
           && { systemctl is-active --quiet "$u" 2>/dev/null || systemctl is-enabled --quiet "$u" 2>/dev/null; }; then
            log "   $u を停止・disable"
            run systemctl disable --now "$u" 2>/dev/null || true
        fi
    done
else
    warn "systemctl が無いためサービス停止をスキップ"
fi

# ---- 2. 手動プロセス停止（PID ファイル方式） ----
log ""
log "▶ 手動起動プロセスを停止"
if [ -x ./stop.sh ]; then
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "   [dry-run] ./stop.sh"
    else
        ./stop.sh || true
    fi
fi

# ---- 3. unit ファイル削除 ----
log ""
log "▶ unit ファイルを削除"
for u in $UNITS; do
    f="/etc/systemd/system/$u.service"
    if [ -f "$f" ]; then
        log "   削除: $f"
        run rm -f "$f"
    fi
done
if command -v systemctl >/dev/null 2>&1; then
    run systemctl daemon-reload || true
fi

# ---- 4. sudoers 削除 ----
log ""
log "▶ sudoers 設定を削除"
for f in /etc/sudoers.d/kizuna-smartctl /etc/sudoers.d/kizuna-security-cron \
         /etc/sudoers.d/kizuna-security-action /etc/sudoers.d/kizuna-cron-verify; do
    if [ -f "$f" ]; then
        log "   削除: $f"
        run rm -f "$f"
    fi
done
# 旧セッションが残した sudoers のバックアップ (*.bak-*) も掃除する。
# 本体パスだけを消すと、これらの退避ファイルが残り続ける。
for f in /etc/sudoers.d/kizuna-*.bak-*; do
    [ -e "$f" ] || continue
    log "   削除: $f"
    run rm -f "$f"
done

# ---- 5. ヘルパー削除 ----
log ""
log "▶ ヘルパースクリプトを削除"
for f in /usr/local/bin/kizuna-cron-read.sh /usr/local/bin/kizuna-action.sh; do
    if [ -f "$f" ]; then
        log "   削除: $f"
        run rm -f "$f"
    fi
done

# ---- 6. /etc/kizuna-eye（署名公開鍵）削除 ----
log ""
log "▶ /etc/kizuna-eye を削除"
if [ -d "$ETC_DIR" ]; then
    log "   削除: $ETC_DIR"
    run rm -rf "$ETC_DIR"
fi

# ---- 7. sysctl 設定削除 ----
log ""
log "▶ コアダンプ sysctl 設定を削除"
if [ -f "$SYSCTL_CONF" ]; then
    log "   削除: $SYSCTL_CONF"
    run rm -f "$SYSCTL_CONF"
    if [ "$DRY_RUN" -eq 0 ] && command -v sysctl >/dev/null 2>&1; then
        sysctl --system >/dev/null 2>&1 || true
    fi
fi

# ---- 8. --purge: データ・バイナリ・ユーザー削除 ----
if [ "$PURGE" -eq 1 ]; then
    log ""
    log "▶ データ・バイナリを削除（--purge）"

    if [ -d "$STATE_ROOT" ]; then
        log "   削除: $STATE_ROOT"
        run rm -rf "$STATE_ROOT"
    fi
    if [ -d "$RUN_HOME/.kizuna-eye" ]; then
        log "   削除: $RUN_HOME/.kizuna-eye"
        run rm -rf "$RUN_HOME/.kizuna-eye"
    fi
    # /opt/kizuna-eye（bin / run / plugins）
    OPT_ROOT="$(dirname "$BIN_DIR")"
    if [ -d "$OPT_ROOT" ]; then
        log "   削除: $OPT_ROOT"
        run rm -rf "$OPT_ROOT"
    fi
    # repo の生成物（ログ・プラグイン .so）
    if [ -d "$REPO_LOGS" ]; then
        log "   repo logs を削除: $REPO_LOGS"
        run rm -rf "$REPO_LOGS"
    fi
    for so in "$REPO_PLUGINS"/*.so "$REPO_PLUGINS"/*.so.sig; do
        [ -e "$so" ] || continue
        log "   削除: $so"
        run rm -f "$so"
    done

    # ユーザー/グループ削除
    log ""
    log "▶ 専用ユーザー/グループを削除"
    if id "$NEW_USER" >/dev/null 2>&1; then
        log "   ユーザー削除: $NEW_USER（ホームごと）"
        run userdel -r "$NEW_USER" 2>/dev/null || run userdel "$NEW_USER" 2>/dev/null || true
    fi
    if getent group "$NEW_USER" >/dev/null 2>&1; then
        log "   グループ削除: $NEW_USER"
        run groupdel "$NEW_USER" 2>/dev/null || true
    fi
fi

# ---- 完了 ----
log ""
if [ "$DRY_RUN" -eq 1 ]; then
    echo "╔══════════════════════════════════════════════════╗"
    echo "║        ℹ️  dry-run 完了（変更していません）      ║"
    echo "╚══════════════════════════════════════════════════╝"
elif [ "$PURGE" -eq 1 ]; then
    echo "╔══════════════════════════════════════════════════╗"
    echo "║        ✅ 完全アンインストール完了               ║"
    echo "╚══════════════════════════════════════════════════╝"
else
    echo "╔══════════════════════════════════════════════════╗"
    echo "║        ✅ アンインストール完了（データ温存）     ║"
    echo "╚══════════════════════════════════════════════════╝"
    log ""
    log "  再インストール    : ./install.sh"
    log "  データも消す場合  : sudo ./uninstall.sh --purge"
fi
log ""
