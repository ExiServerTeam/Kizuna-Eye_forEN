#!/bin/bash
# Kizuna-Eye 静的チェック（gofmt / go vet / i18n 整合性）
set -e

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail=0

# 1) gofmt: 差分があれば失敗
unformatted="$(gofmt -l ./cmd ./internal ./pkg 2>/dev/null || true)"
if [ -n "$unformatted" ]; then
    echo "[lint] gofmt が必要なファイル:"
    echo "$unformatted"
    fail=1
else
    echo "[lint] gofmt: OK"
fi

# 2) go vet
if go vet ./...; then
    echo "[lint] go vet: OK"
else
    echo "[lint] go vet: NG"
    fail=1
fi

# 3) i18n キー整合性（node がある場合のみ）
if command -v node >/dev/null 2>&1; then
    # チェッカー自身を走査対象に含めないよう、一時ディレクトリへコピーして実行する。
    tmp="$(mktemp -d)"
    mkdir -p "$tmp/static"
    cp scripts/check_i18n.js "$tmp/"
    cp web/static/*.js web/static/*.html "$tmp/static/" 2>/dev/null || true
    rm -f "$tmp/static/check_i18n.js"
    if (cd "$tmp" && node check_i18n.js static); then
        echo "[lint] i18n: OK"
    else
        echo "[lint] i18n: NG"
        fail=1
    fi
    rm -rf "$tmp"
else
    echo "[lint] node 未検出のため i18n チェックをスキップ"
fi

exit "$fail"
