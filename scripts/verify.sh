#!/bin/bash
# ============================================================
# Kizuna-Eye 総合検証スクリプト（サーバー / Ubuntu 想定）
#
#   ./scripts/verify.sh          : 静的検証（gofmt/vet/test/race/i18n）
#   ./scripts/verify.sh --smoke  : 上記 + ビルド + 起動 + API/WS スモーク
#   ./scripts/verify.sh --full   : --smoke + プラグイン .so 検査（あれば）
#
# 前提: go / curl が利用可能であること。race 検出器は cgo/gcc 必須のため、
#       gcc が無い環境では自動的にスキップする（Linux サーバーでの実行を推奨）。
# ============================================================
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

MODE="static"
case "${1:-}" in
    --smoke) MODE="smoke" ;;
    --full)  MODE="full"  ;;
    "")      MODE="static";;
    *) echo "usage: $0 [--smoke|--full]" >&2; exit 2 ;;
esac

BASE_URL="${BASE_URL:-http://localhost:8080}"
BIN_DIR="${BIN_DIR:-/opt/kizuna-eye/bin}"

# 設定の置き場。start.sh と同じ自動判定（共有外へ移行済みならそちらを使う）。
DATA_DIR="${KIZUNA_DATA_DIR:-}"
if [ -z "$DATA_DIR" ]; then
    if [ -f "$HOME/.kizuna-eye/data/dashboard_config.json" ]; then
        DATA_DIR="$HOME/.kizuna-eye/data"
    else
        DATA_DIR="$PWD"
    fi
fi

PASS=0
FAIL=0
SKIP=0
ok()   { echo "  [ OK ] $1"; PASS=$((PASS+1)); }
ng()   { echo "  [ NG ] $1"; FAIL=$((FAIL+1)); }
skip() { echo "  [skip] $1"; SKIP=$((SKIP+1)); }
head_() { echo ""; echo "============================================================"; echo " $1"; echo "============================================================"; }

# kill_by_exe <path-to-binary>
# コマンドライン文字列の部分一致（pkill -f）は、自分自身や無関係なプロセスを
# 巻き込む恐れがある。ここでは /proc/PID/exe が対象バイナリの実体と一致する
# プロセスだけを停止する（Linux 前提）。自分自身と親は除外する。
kill_by_exe() {
    local target="$1"
    [ -d /proc ] || return 0
    local self=$$ ppid="$PPID" pid exe
    for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
        [ "$pid" = "$self" ] && continue
        [ "$pid" = "$ppid" ] && continue
        exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
        # 起動中にバイナリが再ビルド（上書き）されると、そのプロセスの
        # /proc/PID/exe は "/path/to/bin (deleted)" を指す。サフィックスを
        # 取り除かないと target と一致せず、旧 dashboard/agent を停止でき
        # ないままポート 8080 を握られ、新ビルドが bind に失敗する。
        exe="${exe% (deleted)}"
        [ -n "$exe" ] || continue
        [ "$exe" = "$target" ] || continue
        kill "$pid" 2>/dev/null || true
    done
}

# wait_exe_gone <path-to-binary>: 対象バイナリを実行中のプロセスが消えるまで
# 待つ（"(deleted)" の exe も一致とみなす）。最大20秒。
wait_exe_gone() {
    local target="$1" tries=0 pid exe
    [ -d /proc ] || return 0
    while [ "$tries" -lt 40 ]; do
        local found=0
        for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
            [ "$pid" = "$$" ] && continue
            [ "$pid" = "$PPID" ] && continue
            exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
            exe="${exe% (deleted)}"
            if [ "$exe" = "$target" ]; then found=1; break; fi
        done
        [ "$found" -eq 0 ] && return 0
        tries=$((tries+1))
        sleep 0.5
    done
    return 1
}

# ------------------------------------------------------------
# 1. 静的検証
# ------------------------------------------------------------
head_ "1. 静的検証"

if command -v gofmt >/dev/null 2>&1; then
    unformatted="$(gofmt -l ./cmd ./internal ./pkg 2>/dev/null || true)"
    if [ -z "$unformatted" ]; then ok "gofmt"; else ng "gofmt: $unformatted"; fi
else
    ng "gofmt が見つかりません"
fi

if go vet ./... >/tmp/ke_vet.log 2>&1; then ok "go vet"; else ng "go vet (詳細: /tmp/ke_vet.log)"; cat /tmp/ke_vet.log; fi

if go test ./... >/tmp/ke_test.log 2>&1; then ok "go test"; else ng "go test (詳細: /tmp/ke_test.log)"; cat /tmp/ke_test.log; fi

# race 検出器（cgo/gcc 必須）
if command -v gcc >/dev/null 2>&1; then
    if CGO_ENABLED=1 go test -race ./... >/tmp/ke_race.log 2>&1; then
        ok "go test -race"
    else
        ng "go test -race (詳細: /tmp/ke_race.log)"; cat /tmp/ke_race.log
    fi
else
    # gcc が無い環境（Windows 等）では race を「失敗」ではなく「スキップ」とする。
    # race は cgo 必須で、Windows の既定ツールチェーンには gcc が無いことが多い。
    # サーバー(Linux) で ./scripts/verify.sh を実行すれば race まで検証できる。
    echo "  [skip] gcc 未検出のため go test -race をスキップ（race は cgo/gcc 必須。Linux サーバーで実行してください）"
fi

# i18n 整合性（スクリプト自身を走査対象に含めない）
if command -v node >/dev/null 2>&1; then
    tmp="$(mktemp -d)"
    mkdir -p "$tmp/static"
    cp scripts/check_i18n.js "$tmp/"
    cp web/static/*.js web/static/*.html "$tmp/static/" 2>/dev/null || true
    rm -f "$tmp/static/check_i18n.js"
    if (cd "$tmp" && node check_i18n.js static >/tmp/ke_i18n.log 2>&1); then
        ok "i18n 整合性 ($(cat /tmp/ke_i18n.log))"
    else
        ng "i18n 整合性 (詳細: /tmp/ke_i18n.log)"; cat /tmp/ke_i18n.log
    fi
    rm -rf "$tmp"
else
    echo "  [skip] node 未検出のため i18n チェックをスキップ"
fi

if [ "$MODE" = "static" ]; then
    head_ "結果: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
    [ "$FAIL" -eq 0 ] && exit 0 || exit 1
fi

# ------------------------------------------------------------
# 2. ビルド
# ------------------------------------------------------------
head_ "2. ビルド"
mkdir -p "$BIN_DIR"
VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo v0.7.1)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

build_one() {
    local out="$1" pkg="$2"
    if CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/$out" "$pkg" >/tmp/ke_build.log 2>&1; then
        ok "build $out"
    else
        ng "build $out (詳細: /tmp/ke_build.log)"; cat /tmp/ke_build.log
    fi
}
build_one plugin-inspect ./cmd/plugin-inspect
build_one dashboard_linux ./cmd/dashboard
build_one agent_linux     ./cmd/agent

[ "$FAIL" -ne 0 ] && { head_ "ビルド失敗のため中断"; exit 1; }

# ------------------------------------------------------------
# 3. 起動 + API スモーク
# ------------------------------------------------------------
head_ "3. 起動 + API スモーク"

mkdir -p logs plugins
kill_by_exe "$BIN_DIR/dashboard_linux"
kill_by_exe "$BIN_DIR/agent_linux"
# 再ビルドで上書きされたバイナリを実行中の旧プロセスは exe が detached に
# なる。ポートを握ったまま新プロセスが bind に失敗し、スモークが旧サーバーに
# 当たるのを防ぐため、実際に消えるまで待つ。
wait_exe_gone "$BIN_DIR/dashboard_linux" || true
wait_exe_gone "$BIN_DIR/agent_linux" || true

"$BIN_DIR/dashboard_linux" -config "$DATA_DIR/dashboard_config.json" > logs/dashboard.log 2>&1 &
DPID=$!
"$BIN_DIR/agent_linux" -config "$DATA_DIR/agent_config.json" -modules "$DATA_DIR/modules.json" > logs/agent.log 2>&1 &
APID=$!
echo "  dashboard PID=$DPID / agent PID=$APID"

# 起動直後に落ちていないか確認する。bind 失敗や設定不正で即終了した場合、
# 以降のチェックが古いサーバーに対して誤って成功するのを防ぐ。
sleep 1
if kill -0 "$DPID" 2>/dev/null; then ok "dashboard 起動 (PID=$DPID)"; else ng "dashboard が即時終了"; tail -5 logs/dashboard.log; fi
if kill -0 "$APID" 2>/dev/null; then ok "agent 起動 (PID=$APID)"; else ng "agent が即時終了"; tail -5 logs/agent.log; fi

cleanup() {
    kill "$DPID" "$APID" 2>/dev/null || true
    # 取りこぼしがあれば実体一致で確実に落とす（文字列一致は使わない）。
    kill_by_exe "$BIN_DIR/dashboard_linux"
    kill_by_exe "$BIN_DIR/agent_linux"
}
trap cleanup EXIT

# サーバー起動待ち
for i in $(seq 1 20); do
    if curl -s --connect-timeout 2 "$BASE_URL/health" >/dev/null 2>&1; then break; fi
    sleep 0.5
done

# Agent 接続待ち（health が 200 になるまで最大20秒）
agent_up=0
for i in $(seq 1 40); do
    if curl -s "$BASE_URL/health" | grep -q '"agent_connected":true'; then agent_up=1; break; fi
    sleep 0.5
done
if [ "$agent_up" -eq 1 ]; then ok "Agent 接続 (health=healthy)"; else ng "Agent が接続されません（20秒待機）"; fi

check_json() {
    local path="$1" key="$2"
    local resp body code
    resp="$(curl -s -w '\n%{http_code}' "$BASE_URL$path")"
    code="$(printf '%s' "$resp" | tail -n1)"
    body="$(printf '%s' "$resp" | sed '$d')"
    # 401 は auth.enabled=true でエンドポイントが正しくセッションを要求して
    # いる状態。スモークは資格情報なしで実行するため、失敗ではなくスキップ
    # として扱う。
    if [ "$code" = "401" ]; then
        skip "GET $path (401: 認証必須。auth.enabled=true)"
        return
    fi
    if printf '%s' "$body" | grep -q "$key"; then
        ok "GET $path ($key)"
    else
        ng "GET $path ($key が見つからない, HTTP $code)"
    fi
}

check_json /api/status        'cpu_usage'
check_json /api/history       'samples'
check_json /api/alerts        'alerts'
check_json /api/modules       'name'
check_json /api/version       'version'
check_json /api/alert-config  'memory_warn_pct'
check_json /api/logs?type=agent\&lines=5 ''
check_json /api/metrics       'kizuna_up'

# 履歴が実際に伸びているか。Agent は interval ごとに1件追加するため、
# 起動直後は1件しか無いことがある。数秒待って増加を確認する。
hist_code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/api/history")"
if [ "$hist_code" = "401" ]; then
    skip "メトリクス履歴の増加確認 (401: 認証必須)"
else
    before="$(curl -s "$BASE_URL/api/history" | grep -o '"timestamp"' | wc -l)"
    cnt="$before"
    for _ in $(seq 1 10); do
        sleep 1
        cnt="$(curl -s "$BASE_URL/api/history" | grep -o '"timestamp"' | wc -l)"
        if [ "$cnt" -ge 3 ] && [ "$cnt" -gt "$before" ]; then
            break
        fi
    done
    if [ "$cnt" -ge 3 ] && [ "$cnt" -gt "$before" ]; then
        ok "メトリクス履歴サンプル数=$cnt (増加を確認)"
    else
        ng "メトリクス履歴サンプルが増えない (before=$before, now=$cnt)"
    fi
fi

# ------------------------------------------------------------
# 4. プラグイン検査（--full のみ）
# ------------------------------------------------------------
if [ "$MODE" = "full" ]; then
    head_ "4. プラグイン検査"
    so="$(ls -1 "$BIN_DIR"/plugins/*.so 2>/dev/null | head -1 || true)"
    if [ -n "$so" ]; then
        if "$BIN_DIR/plugin-inspect" --so "$so" >/tmp/ke_inspect.log 2>&1; then
            ok "plugin-inspect $(basename "$so")"
        else
            ng "plugin-inspect $(basename "$so") (詳細: /tmp/ke_inspect.log)"; cat /tmp/ke_inspect.log
        fi
    else
        echo "  [skip] .so が見つからないためスキップ ($BIN_DIR/plugins)"
    fi
fi

head_ "結果: PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
