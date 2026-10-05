#!/bin/bash
# ============================================================
# Kizuna-Eye stop.sh
# dashboard / agent を安全に停止する。
#
# 以前は `pkill -f dashboard_linux` を使っていたが、これは
#   - 停止と起動を同じコマンド行で実行すると、その行自体が -f に
#     マッチして自分を殺す（自滅）
#   - パターンが広く、無関係なプロセスを巻き込む
# という問題があった。ここでは PID ファイルを第一に使い、無ければ
# 実行ファイルの絶対パスで正確に照合する。
#
# A-4（agent 専用ユーザー kizuna-agent）以降は agent だけ systemd 管理になる。
# 別ユーザー所有のプロセスは kill できない（EPERM）ため、このスクリプトは
# agent が systemd 管理下なら agent を触らない。以前の実装は kill の成否を
# 確認せず「停止しました」と表示していたので、start.sh が二重起動していた。
#
# 使い方:
#   ./stop.sh            # dashboard + agent（agent が systemd 管理なら dashboard のみ）
#   ./stop.sh dashboard  # dashboard のみ
#   ./stop.sh agent      # agent のみ（systemd 管理下なら何もしない）
#   KIZUNA_FORCE_MANUAL=1 ./stop.sh
#                         # systemd 管理下でも手動停止を試みる
# ============================================================
set -u
umask 077  # New files/dirs: 0600/0700 (secrets, pid, logs)
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"

resolve_run_dir() {
    local d="${KIZUNA_RUN_DIR:-/opt/kizuna-eye/run}"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    d="$PWD/logs"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    echo "/tmp"
}
RUN_DIR="$(resolve_run_dir)"

# wait_gone <pid> -> 0 = 終了した（TERM → KILL）, 1 = まだ生きている
# 別ユーザー所有のプロセスは kill が EPERM で失敗する。ここで確認しないと
# 「停止した」と嘘をつき、start.sh が二重起動する（A-4 で実際に起きる）。
wait_gone() {
    local pid="$1" i
    for i in $(seq 1 25); do
        kill -0 "$pid" 2>/dev/null || return 0
        sleep 0.2
    done
    kill -9 "$pid" 2>/dev/null
    for i in $(seq 1 10); do
        kill -0 "$pid" 2>/dev/null || return 0
        sleep 0.2
    done
    return 1
}

# stop_one <binary-name>
# 停止した場合は何も出力しない（呼び出し側でまとめて表示する）。
# 戻り値: 0 = 停止した, 1 = 起動していなかった, 2 = 稼働中だが停止できなかった。
stop_one() {
    local name="$1"
    local pidfile="$RUN_DIR/$name.pid"
    local stopped=0 alive=0

    # 1) PID ファイル（最優先・最も正確）
    if [ -f "$pidfile" ]; then
        local pid
        pid="$(cat "$pidfile" 2>/dev/null || true)"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            if wait_gone "$pid"; then stopped=1; else alive=1; fi
        fi
        rm -f "$pidfile"
    fi

    # 2) フォールバック: PID ファイルが無い/古い場合のみ。
    #    コマンドライン文字列の部分一致（pgrep -f）は、無関係なプロセスや
    #    自分自身のシェルを誤って拾う恐れがある。ここでは /proc/PID/exe が
    #    対象バイナリの実体と一致するものだけを停止する（Linux 前提）。
    local target="$BIN_DIR/$name"
    if [ "$alive" -eq 0 ] && [ -d /proc ]; then
        local self=$$
        local ppid="$PPID"
        local pid exe
        for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
            [ "$pid" = "$self" ] && continue
            [ "$pid" = "$ppid" ] && continue
            exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
            # 起動中にバイナリが再ビルド（上書き）されると /proc/PID/exe は
            # "/path/to/bin (deleted)" を指す。サフィックスを除去しないと
            # target と一致せず停止できない（verify.sh と同じ修正）。
            exe="${exe% (deleted)}"
            [ -n "$exe" ] || continue
            [ "$exe" = "$target" ] || continue
            kill "$pid" 2>/dev/null
            if wait_gone "$pid"; then stopped=1; else alive=1; fi
        done
    fi

    [ "$alive" -eq 1 ] && return 2
    [ "$stopped" -eq 1 ] && return 0
    return 1
}

# 停止対象（all | dashboard | agent）。既定は all（従来どおり）。
WANT="${1:-all}"
case "$WANT" in
    all|dashboard|agent) ;;
    -h|--help)
        echo "usage: $0 [all|dashboard|agent]"
        echo "  agent が systemd (kizuna-agent) 管理下のときは agent を停止しません。"
        echo "  agent の停止: sudo systemctl stop kizuna-agent"
        exit 0 ;;
    *)
        echo "usage: $0 [all|dashboard|agent]" >&2
        exit 2 ;;
esac

# agent が systemd (kizuna-agent.service) の管理下か。
#   active : 今まさに systemd が動かしている
#   enabled: systemd が起動時に立ち上げる（= 手動管理から外れている）
# どちらの場合もこのスクリプトでは触らない（kill は EPERM、かつ systemd が
# すぐ起動し直すため、成功と偽ると start.sh が二重起動する）。
systemd_manages_agent() {
    [ "${KIZUNA_FORCE_MANUAL:-0}" = "1" ] && return 1
    command -v systemctl >/dev/null 2>&1 || return 1
    systemctl is-active --quiet kizuna-agent 2>/dev/null && return 0
    systemctl is-enabled --quiet kizuna-agent 2>/dev/null && return 0
    systemctl is-active --quiet kizuna-eye-agent 2>/dev/null && return 0
    systemctl is-enabled --quiet kizuna-eye-agent 2>/dev/null && return 0
    return 1
}

# 個別の出力はせず、まとめて表示する。
any_stopped=0
any_failed=0
skipped_systemd=0

if [ "$WANT" != "agent" ]; then
    stop_one dashboard_linux
    case $? in
        0) any_stopped=1 ;;
        2) any_failed=1 ;;
    esac
fi

if [ "$WANT" != "dashboard" ]; then
    if systemd_manages_agent; then
        skipped_systemd=1
    else
        stop_one agent_linux
        case $? in
            0) any_stopped=1 ;;
            2) any_failed=1 ;;
        esac
    fi
fi

if [ "$skipped_systemd" -eq 1 ]; then
    echo "ℹ️  agent は systemd (kizuna-agent) が管理中です。ここからは停止しません:"
    echo "    sudo systemctl stop kizuna-agent"
fi
if [ "$any_failed" -eq 1 ]; then
    echo "⚠️  Kizuna-Eyeを完全に停止できませんでした（別ユーザー所有のプロセスは kill できません）。"
elif [ "$any_stopped" -eq 1 ]; then
    echo "🛑 Kizuna-Eyeを停止しました。"
elif [ "$skipped_systemd" -eq 1 ]; then
    # agent は systemd 管理下で動いている。ここで「起動していません」と言うと
    # 「agent も止まっている」と誤解されるため区別する。
    echo "ℹ️  このスクリプトが管理するプロセス（dashboard など）は起動していません。"
else
    echo "ℹ️  Kizuna-Eyeは起動していません。"
fi
[ "$any_failed" -eq 1 ] && exit 1
exit 0
