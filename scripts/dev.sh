#!/bin/bash
# Kizuna-Eye 開発用ヘルパー
#   ./scripts/dev.sh vet     : gofmt チェック + go vet
#   ./scripts/dev.sh test    : go test ./...
#   ./scripts/dev.sh build   : scripts/build.sh を実行
#   ./scripts/dev.sh run     : ローカルで dashboard を起動（agent は別途）
#   ./scripts/dev.sh clean   : ビルド成果物と一時ファイルを削除
set -e

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

cmd="${1:-test}"

case "$cmd" in
    vet)
        exec ./scripts/lint.sh
        ;;
    test)
        exec go test ./... -count=1
        ;;
    build)
        exec ./scripts/build.sh
        ;;
    run)
        echo "[dev] dashboard を起動します（Ctrl+C で停止）"
        exec go run ./cmd/dashboard -config dashboard_config.json
        ;;
    clean)
        rm -f plugin-inspect dashboard_linux agent_linux
        echo "[dev] クリーン完了"
        ;;
    *)
        echo "usage: $0 {vet|test|build|run|clean}" >&2
        exit 2
        ;;
esac
