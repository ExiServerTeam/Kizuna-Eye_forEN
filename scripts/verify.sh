#!/bin/bash
# ============================================================
# Kizuna-Eye 総合検証スクリプト（サーバー / Ubuntu 想定）
#
#   ./scripts/verify.sh          : 静的検証（gofmt/vet/test/race/i18n）
#   ./scripts/verify.sh --smoke  : 上記 + ビルド + 起動 + API/WS スモーク
#   ./scripts/verify.sh --full   : --smoke + プラグイン .so 検査（あれば）
#
# 前提: go / gcc / curl が利用可能であること（race は cgo 必須）
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

PASS=0
FAIL=0
ok()   { echo "  [ OK ] $1"; PASS=$((PASS+1)); }
ng()   { echo "  [ NG ] $1"; FAIL=$((FAIL+1)); }
head_() { echo ""; echo "============================================================"; echo " $1"; echo "============================================================"; }

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
    ng "gcc 未検出のため go test -race を実行できません（サーバーで要確認）"
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
    head_ "結果: PASS=$PASS FAIL=$FAIL"
    [ "$FAIL" -eq 0 ] && exit 0 || exit 1
fi

# ------------------------------------------------------------
# 2. ビルド
# ------------------------------------------------------------
head_ "2. ビルド"
mkdir -p "$BIN_DIR"
VERSION="${VERSION:-v0.6.1}"
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
pkill -f dashboard_linux 2>/dev/null || true
pkill -f agent_linux 2>/dev/null || true
sleep 1

"$BIN_DIR/dashboard_linux" -config dashboard_config.json > logs/dashboard.log 2>&1 &
DPID=$!
"$BIN_DIR/agent_linux" -config agent_config.json > logs/agent.log 2>&1 &
APID=$!
echo "  dashboard PID=$DPID / agent PID=$APID"

cleanup() {
    kill "$DPID" "$APID" 2>/dev/null || true
    pkill -f dashboard_linux 2>/dev/null || true
    pkill -f agent_linux 2>/dev/null || true
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
    local body
    body="$(curl -s "$BASE_URL$path")"
    if printf '%s' "$body" | grep -q "$key"; then
        ok "GET $path ($key)"
    else
        ng "GET $path ($key が見つからない)"
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
before="$(curl -s "$BASE_URL/api/history" | grep -o '"timestamp"' | wc -l)"
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

head_ "結果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
