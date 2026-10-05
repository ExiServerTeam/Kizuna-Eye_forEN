#!/bin/bash
# ============================================================
# Kizuna-Eye 暫定 watch dog（sudo 不要）
#
# 背景:
#   agent / dashboard が SIGSEGV で落ちても、自動再起動の仕組みが無いと
#   監視が静かに停止する（systemd 未使用・手動起動のため）。
#   systemd unit を導入するまでの間、このスクリプトを 1 分ごとに回して
#   落ちていたら start.sh で復帰させる。
#
# 使い方（いずれか）:
#   A) user systemd timer（推奨・sudo 不要）
#        install -d -m 700 ~/.config/systemd/user
#        sed "s|__DIR__|$PWD|g" systemd/kizuna-watchdog.service \
#          > ~/.config/systemd/user/kizuna-watchdog.service
#        cp -p systemd/kizuna-watchdog.timer ~/.config/systemd/user/
#        systemctl --user daemon-reload
#        systemctl --user enable --now kizuna-watchdog.timer
#        # ログインしていなくても動かすなら（要 sudo/管理者）:
#        sudo loginctl enable-linger "$USER"
#   B) cron（sudo 不要）
#        (crontab -l 2>/dev/null; echo '* * * * * '"$PWD"'/systemd/kizuna-watchdog.sh') | crontab -
#
# systemd unit (kizuna-agent / kizuna-dashboard) が導入済みなら、
# 二重再起動を避けるため本番犬は何もしない。
# ============================================================
set -u
cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
LOG="logs/kizuna-watchdog.log"
mkdir -p logs

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG"; }

# start.sh と同じ RUN_DIR 解決（PID ファイルの場所）。
resolve_run_dir() {
    local d="${KIZUNA_RUN_DIR:-/opt/kizuna-eye/run}"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    d="$PWD/logs"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    echo "/tmp"
}
RUN_DIR="$(resolve_run_dir)"

# 暴走防止: 直近 20 秒以内に start.sh を叩いていたら今回は何もしない。
STAMP="$RUN_DIR/.kizuna-watchdog.last"
if [ -f "$STAMP" ]; then
    last="$(stat -c %Y "$STAMP" 2>/dev/null || echo 0)"
    now="$(date +%s)"
    if [ $(( now - last )) -lt 20 ]; then
        exit 0
    fi
fi

# systemd 管理下なら本番犬は無効（systemd の Restart=on-failure に任せる）。
if command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active --quiet kizuna-agent 2>/dev/null \
       || systemctl is-active --quiet kizuna-eye-agent 2>/dev/null \
       || systemctl is-active --quiet kizuna-dashboard 2>/dev/null; then
        exit 0
    fi
fi

# PID ファイルを実態に合わせて更新する。
#   start.sh の is_running は PID ファイルしか見ないため、手動起動などで
#   PID ファイルが陳腐化していると「片方だけ死んだ」ときに start.sh が
#   生きている側まで二重起動してしまう。先に現行 PID を書き直して防ぐ。
for name in dashboard_linux agent_linux; do
    pid="$(pgrep -f "$BIN_DIR/$name" 2>/dev/null | head -1)"
    if [ -n "$pid" ]; then
        echo "$pid" > "$RUN_DIR/$name.pid" 2>/dev/null || true
    fi
done

missing=""
for name in dashboard_linux agent_linux; do
    if ! pgrep -f "$BIN_DIR/$name" >/dev/null 2>&1; then
        missing="$missing $name"
    fi
done

if [ -n "$missing" ]; then
    touch "$STAMP"
    log "停止を検知:$missing -> start.sh 実行 (RUN_DIR=$RUN_DIR)"
    if ./start.sh >> "$LOG" 2>&1; then
        log "start.sh 完了"
    else
        log "start.sh 失敗 (exit=$?)"
    fi
fi
