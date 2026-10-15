#!/bin/bash
# ============================================================
# Kizuna-Eye スモーク用の最小設定を生成する。
# Generate a minimal smoke-test config set.
#
# 同梱の example 設定をコピーし、スモークに必要な最小限の編集だけを行う:
#   1. agent トークンを dashboard / agent で一致させる（不一致だと agent が接続できない）
#   2. plugins_dir を一時ディレクトリへ向ける（/opt に依存しない）
#   3. require_signature を false にする（署名鍵の無い環境で起動できるように）
#   4. notifications の enabled を false にする（プレースホルダ webhook を使わない）
#
# 使い方:
#   ./scripts/smoke-config.sh <出力ディレクトリ>
#
# Copy the shipped examples and apply only the minimal edits a smoke run
# needs: match the agent token, point plugins_dir at a temp dir, disable
# signature verification (no key in a fresh environment) and disable
# notifications (placeholder webhooks). Keep the examples as the source of
# truth so the smoke run tracks the real config schema.
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-}"
if [ -z "$OUT" ]; then
    echo "usage: $0 <output-dir>" >&2
    exit 2
fi

mkdir -p "$OUT" "$OUT/plugins"

DASH_EXAMPLE="$ROOT/dashboard_config.example.json"
AGENT_EXAMPLE="$ROOT/agent_config.example.json"
[ -r "$DASH_EXAMPLE" ]  || { echo "❌ $DASH_EXAMPLE がありません" >&2; exit 1; }
[ -r "$AGENT_EXAMPLE" ] || { echo "❌ $AGENT_EXAMPLE がありません" >&2; exit 1; }

# dashboard と agent で同じトークンを使う（32 バイトの乱数を hex 化）。
# Use the same token in both files (32 random bytes, hex-encoded).
TOKEN="smoke-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"

cp "$DASH_EXAMPLE"  "$OUT/dashboard_config.json"
cp "$AGENT_EXAMPLE" "$OUT/agent_config.json"

# dashboard: agent_token / plugins_dir / require_signature / 通知を調整。
# dashboard: adjust agent_token, plugins_dir, require_signature, notifications.
sed -i \
    -e "s|\"agent_token\": \"CHANGE_ME_TO_A_LONG_RANDOM_STRING\"|\"agent_token\": \"$TOKEN\"|" \
    -e "s|\"plugins_dir\": \"/opt/kizuna-eye/bin/plugins\"|\"plugins_dir\": \"$OUT/plugins\"|" \
    -e 's|"require_signature": true|"require_signature": false|' \
    -e 's|"enabled": true|"enabled": false|g' \
    "$OUT/dashboard_config.json"

# agent: token / plugins_dir / require_signature を調整。
# agent: adjust token, plugins_dir, require_signature.
sed -i \
    -e "s|\"token\": \"CHANGE_ME_TO_A_LONG_RANDOM_STRING\"|\"token\": \"$TOKEN\"|" \
    -e "s|\"plugins_dir\": \"/opt/kizuna-eye/bin/plugins\"|\"plugins_dir\": \"$OUT/plugins\"|" \
    -e 's|"require_signature": true|"require_signature": false|' \
    "$OUT/agent_config.json"

# 置換が実際に効いたか確認する（トークンが両方に入っていること）。
# Verify the substitutions actually applied (token present in both files).
if ! grep -q "$TOKEN" "$OUT/dashboard_config.json"; then
    echo "❌ dashboard_config.json の agent_token 置換に失敗しました" >&2
    exit 1
fi
if ! grep -q "$TOKEN" "$OUT/agent_config.json"; then
    echo "❌ agent_config.json の token 置換に失敗しました" >&2
    exit 1
fi

echo "✅ smoke config: $OUT"
