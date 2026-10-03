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

# Go ランタイムのメモリ調整。
# GOGC を下げると GC が早く動き、アイドル時のヒープ（RSS）を小さく保つ。
# GOMEMLIMIT はソフト上限で、超えそうになると GC を強める。
# どちらも環境変数で上書きできる（KIZUNA_GOGC / KIZUNA_GOMEMLIMIT）。
# 注: これで下がるのはヒープのみ。RSS の大半はバイナリとプラグインの
#     コードページ(.text)で、そちらは削れない。
export GOGC="${KIZUNA_GOGC:-50}"
export GOMEMLIMIT="${KIZUNA_GOMEMLIMIT:-64MiB}"

# is_running <binary-name> -> 0 if running
is_running() {
    local name="$1" pidfile="$RUN_DIR/$name.pid"
    [ -f "$pidfile" ] || return 1
    local pid; pid="$(cat "$pidfile" 2>/dev/null || true)"
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

# start_one <binary-name> <config> <logfile>
# 起動した場合は 0、既に起動中だった場合は 1 を返す。
# 個別の出力はせず、呼び出し側でまとめて表示する。
start_one() {
    local name="$1" cfg="$2" log="$3"
    if is_running "$name"; then
        return 1
    fi
    # nohup: SSH セッション終了時の SIGHUP を無視。stdin を /dev/null に。
    nohup "$BIN_DIR/$name" -config "$cfg" >> "$log" 2>&1 < /dev/null &
    local pid=$!
    echo "$pid" > "$RUN_DIR/$name.pid"
    disown 2>/dev/null || true
    return 0
}

new_started=0
already_running=0
start_one dashboard_linux dashboard_config.json logs/dashboard.log && new_started=1 || already_running=1
sleep 1
start_one agent_linux agent_config.json logs/agent.log && new_started=1 || already_running=1

echo ""
if [ "$new_started" -eq 1 ]; then
    echo "✅ Kizuna-Eyeを起動しました。"
else
    echo "ℹ️  Kizuna-Eyeは既に起動しています。"
fi
echo ""
echo "🌐 http://localhost:8080"
exit 0
