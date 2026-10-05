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
#
# A-4（agent を専用ユーザー kizuna-agent で動かす）以降は agent だけ systemd
# 管理になる。その状態でここから agent を起動すると、systemd 管理下の agent と
# 二重に動き、state / chain.key（/var/lib/kizuna-eye、0700 kizuna-agent）を
# 書けない側が失敗し続ける（ダッシュボードには両方が見える）。よって agent が
# systemd 管理下（active または enabled）のときは agent を起動しない。
#
# 使い方:
#   ./start.sh            # dashboard + agent（agent が systemd 管理なら dashboard のみ）
#   ./start.sh dashboard  # dashboard のみ
#   ./start.sh agent      # agent のみ（systemd 管理下なら何もしない）
#   KIZUNA_FORCE_MANUAL=1 ./start.sh
#                         # systemd 管理下でも手動起動する（二重起動注意）
# ============================================================
set -u
umask 077  # New files/dirs: 0600/0700 (secrets, pid, logs)
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
# 表示言語（install.sh から export される。未設定は日本語）。
UI_LANG="${UI_LANG:-ja}"

resolve_run_dir() {
    local d="${KIZUNA_RUN_DIR:-/opt/kizuna-eye/run}"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    d="$PWD/logs"
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then echo "$d"; return; fi
    echo "/tmp"
}
RUN_DIR="$(resolve_run_dir)"

# 起動対象（all | dashboard | agent）。既定は all（従来どおり）。
WANT="${1:-all}"
case "$WANT" in
    all|dashboard|agent) ;;
    -h|--help)
        echo "usage: $0 [all|dashboard|agent]"
        if [ "$UI_LANG" = "en" ]; then
            echo "  When the agent is managed by systemd (kizuna-eye-agent), it is not started."
            echo "  Restart the agent: sudo systemctl restart kizuna-eye-agent"
        else
            echo "  agent が systemd (kizuna-eye-agent) 管理下のときは agent を起動しません。"
            echo "  agent の再起動: sudo systemctl restart kizuna-eye-agent"
        fi
        exit 0 ;;
    *)
        echo "usage: $0 [all|dashboard|agent]" >&2
        exit 2 ;;
esac

# 設定・秘密・鍵の置き場。共有上（./）から共有外（移行後）へ切り替える。
#   SMB 共有は force user=user で uid 1000 になるため chmod 600 では守れない。
#   よって users.json（bcrypt）/ sessions.json / agent_token / chain.key は
#   共有の外へ出す。判定は次の順で自動:
#     1. KIZUNA_DATA_DIR が指定されていればそれを使う
#     2. 移行先に dashboard_config.json があればそこを使う
#     3. どちらも無ければ従来どおり ./（共有上）
#   ロールバックは共有へ設定を戻すだけでよい（自動で ./ に戻る）。
DATA_DIR="${KIZUNA_DATA_DIR:-}"
if [ -z "$DATA_DIR" ]; then
    if [ -f "$HOME/.kizuna-eye/data/dashboard_config.json" ]; then
        DATA_DIR="$HOME/.kizuna-eye/data"
    else
        DATA_DIR="$PWD"
    fi
fi

for bin in dashboard_linux agent_linux; do
    if [ ! -x "$BIN_DIR/$bin" ]; then
        if [ "$UI_LANG" = "en" ]; then
            echo "❌ $BIN_DIR/$bin not found. Run ./build.sh first."
        else
            echo "❌ $BIN_DIR/$bin が見つかりません。./build.sh を実行してください。"
        fi
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

# start_one <binary-name> <config> <logfile> [extra args...]
# 起動した場合は 0、既に起動中だった場合は 1 を返す。
# 個別の出力はせず、呼び出し側でまとめて表示する。
start_one() {
    local name="$1" cfg="$2" log="$3"
    shift 3
    if is_running "$name"; then
        return 1
    fi
    # nohup: SSH セッション終了時の SIGHUP を無視。stdin を /dev/null に。
    nohup "$BIN_DIR/$name" -config "$cfg" "$@" >> "$log" 2>&1 < /dev/null &
    local pid=$!
    echo "$pid" > "$RUN_DIR/$name.pid"
    disown 2>/dev/null || true
    return 0
}

# agent が systemd (kizuna-eye-agent.service / 旧 kizuna-agent.service) の管理下か。
#   active : 今まさに systemd が動かしている
#   enabled: systemd が起動時に立ち上げる（= 手動管理から外れている）
# どちらの場合も手動起動は二重管理になるため避ける。
systemd_manages_agent() {
    [ "${KIZUNA_FORCE_MANUAL:-0}" = "1" ] && return 1
    command -v systemctl >/dev/null 2>&1 || return 1
    systemctl is-active --quiet kizuna-agent 2>/dev/null && return 0
    systemctl is-enabled --quiet kizuna-agent 2>/dev/null && return 0
    systemctl is-active --quiet kizuna-eye-agent 2>/dev/null && return 0
    systemctl is-enabled --quiet kizuna-eye-agent 2>/dev/null && return 0
    return 1
}

new_started=0
already_running=0
skipped_systemd=0

if [ "$WANT" != "agent" ]; then
    start_one dashboard_linux "$DATA_DIR/dashboard_config.json" logs/dashboard.log && new_started=1 || already_running=1
    sleep 1
fi

if [ "$WANT" != "dashboard" ]; then
    if systemd_manages_agent; then
        skipped_systemd=1
    else
        start_one agent_linux "$DATA_DIR/agent_config.json" logs/agent.log -modules "$DATA_DIR/modules.json" && new_started=1 || already_running=1
    fi
fi

echo ""
if [ "$skipped_systemd" -eq 1 ]; then
    if [ "$UI_LANG" = "en" ]; then
        echo "ℹ️  agent is managed by systemd (kizuna-eye-agent); not starting it manually."
        echo "    Status: systemctl status kizuna-eye-agent / Restart: sudo systemctl restart kizuna-eye-agent"
    else
        echo "ℹ️  agent は systemd (kizuna-eye-agent) が管理中です。手動起動はしません。"
        echo "    状態: systemctl status kizuna-eye-agent / 再起動: sudo systemctl restart kizuna-eye-agent"
    fi
fi
if [ "$new_started" -eq 1 ]; then
    if [ "$UI_LANG" = "en" ]; then echo "✅ Started Kizuna-Eye."; else echo "✅ Kizuna-Eyeを起動しました。"; fi
elif [ "$already_running" -eq 1 ]; then
    if [ "$UI_LANG" = "en" ]; then echo "ℹ️  Kizuna-Eye is already running."; else echo "ℹ️  Kizuna-Eyeは既に起動しています。"; fi
fi
echo ""
echo "🌐 http://localhost:8080"
exit 0
