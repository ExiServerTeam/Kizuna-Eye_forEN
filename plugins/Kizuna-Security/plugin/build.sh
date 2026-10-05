#!/bin/bash
# Kizuna-Security/plugin/build.sh
# セキュリティ監視プラグインをビルドして /opt/kizuna-eye/bin/plugins に配備する。
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o kizuna_security.so .

DEPLOY_DIR="/opt/kizuna-eye/bin/plugins"
if [ -d "$DEPLOY_DIR" ]; then
    cp kizuna_security.so "$DEPLOY_DIR/"
    echo "✅ 配備完了: $DEPLOY_DIR/kizuna_security.so"
else
    echo "⚠️ 配備先が見つかりません: $DEPLOY_DIR"
fi

ls -la kizuna_security.so
