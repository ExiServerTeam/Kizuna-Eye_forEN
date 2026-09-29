#!/bin/bash
# ============================================================
# Kizuna-Eye start.sh
# dashboard / agent を起動する。
#
# 以前は `pkill -f` で無条件に落としてから `&` で起動していた。
#   - pkill の自滅バグ（stop.sh 参照）
#   - nohup 無しの & は SSH セッション終了で SIGHUP により落ちる
# という問題があった。ここでは
#   - 既に起動中なら何もしない（二重起動防止）
#   - nohup + stdin=/dev/null でセッション終了に耐える
#   - PID ファイルを書いて stop.sh から確実に停止できる
# ようにする。
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

for bin in dashboard_linux agent_linux; do
    if [ ! -x "$BIN_DIR/$bin" ]; then
        echo "❌ $BIN_DIR/$bin が見つかりません。./build.sh を実行してください。"
        exit 1
    fi
done

mkdir -p logs

# is_running <binary-name> -> 0 if running
is_running() {
    local name="$1" pidfile="$RUN_DIR/$name.pid"
    [ -f "$pidfile" ] || return 1
    local pid; pid="$(cat "$pidfile" 2>/dev/null || true)"
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

# start_one <binary-name> <config> <logfile>
start_one() {
    local name="$1" cfg="$2" log="$3"
    if is_running "$name"; then
        echo "ℹ️  $name は既に起動中です (PID: $(cat "$RUN_DIR/$name.pid"))"
        return 0
    fi
    # nohup: SSH セッション終了時の SIGHUP を無視。stdin を /dev/null に。
    nohup "$BIN_DIR/$name" -config "$cfg" >> "$log" 2>&1 < /dev/null &
    local pid=$!
    echo "$pid" > "$RUN_DIR/$name.pid"
    disown 2>/dev/null || true
    echo "✅ $name 起動 (PID: $pid)"
}

start_one dashboard_linux dashboard_config.json logs/dashboard.log
sleep 1
start_one agent_linux agent_config.json logs/agent.log

echo ""
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$LAN_IP" ] && echo "🌐 http://$LAN_IP:8080"
exit 0
