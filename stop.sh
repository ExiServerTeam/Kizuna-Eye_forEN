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
# ============================================================
set -u
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

# stop_one <binary-name>
stop_one() {
    local name="$1"
    local pidfile="$RUN_DIR/$name.pid"
    local stopped=0

    # 1) PID ファイル（最優先・最も正確）
    if [ -f "$pidfile" ]; then
        local pid
        pid="$(cat "$pidfile" 2>/dev/null || true)"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            local i
            for i in $(seq 1 25); do
                kill -0 "$pid" 2>/dev/null || break
                sleep 0.2
            done
            kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
            echo "🛑 $name 停止 (PID: $pid)"
            stopped=1
        fi
        rm -f "$pidfile"
    fi

    # 2) フォールバック: PID ファイルが無い/古い場合のみ。
    #    コマンドライン文字列の部分一致（pgrep -f）は、無関係なプロセスや
    #    自分自身のシェルを誤って拾う恐れがある。ここでは /proc/PID/exe が
    #    対象バイナリの実体と一致するものだけを停止する（Linux 前提）。
    local target="$BIN_DIR/$name"
    if [ -d /proc ]; then
        local self=$$
        local ppid="$PPID"
        local pid exe
        for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
            [ "$pid" = "$self" ] && continue
            [ "$pid" = "$ppid" ] && continue
            exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
            [ "$exe" = "$target" ] || continue
            kill "$pid" 2>/dev/null
            local i
            for i in $(seq 1 25); do
                kill -0 "$pid" 2>/dev/null || break
                sleep 0.2
            done
            kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
            echo "🛑 $name 停止 (PID: $pid)"
            stopped=1
        done
    fi

    [ "$stopped" -eq 0 ] && echo "ℹ️  $name は起動していません"
    return 0
}

stop_one dashboard_linux
stop_one agent_linux
exit 0
