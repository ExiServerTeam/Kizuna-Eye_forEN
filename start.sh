#!/bin/bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BIN_DIR="/opt/kizuna-eye/bin"

pkill -f dashboard_linux 2>/dev/null
pkill -f agent_linux 2>/dev/null
sleep 1

for bin in dashboard_linux agent_linux plugin-inspect; do
    if [ ! -x "$BIN_DIR/$bin" ]; then
        echo "❌ $BIN_DIR/$bin が見つかりません。./build.sh を実行してください。"
        exit 1
    fi
done

mkdir -p logs
mkdir -p plugins

"$BIN_DIR/dashboard_linux" -config dashboard_config.json > logs/dashboard.log 2>&1 &
echo "✅ ダッシュボード起動 (PID: $!)"

"$BIN_DIR/agent_linux" -config agent_config.json > logs/agent.log 2>&1 &
echo "✅ エージェント起動 (PID: $!)"

echo ""
echo "🌐 http://$(hostname -I | awk '{print $1}'):8080"
