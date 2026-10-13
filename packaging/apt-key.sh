#!/bin/bash
# ============================================================
# Kizuna-Eye APT 署名鍵の生成
#
# 専用の GNUPGHOME (.gnupg-apt/) に署名鍵を作る（ユーザーの鍵束を汚さない）。
# 秘密鍵はリポジトリにコミットしない（.gitignore 済み）。安全にバックアップすること。
#
# 使い方:
#   ./packaging/apt-key.sh
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GNUPGHOME_DIR="${KIZUNA_APT_GNUPGHOME:-$ROOT/.gnupg-apt}"
NAME="${KIZUNA_APT_KEY_NAME:-Kizuna-Eye APT Signing}"
EMAIL="${KIZUNA_APT_KEY_EMAIL:-kizuna-eye@example.com}"

mkdir -p "$GNUPGHOME_DIR"
chmod 700 "$GNUPGHOME_DIR"
export GNUPGHOME="$GNUPGHOME_DIR"

echo "[apt-key] GNUPGHOME=$GNUPGHOME_DIR"

if gpg --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec:'; then
    echo "[apt-key] 既存の署名鍵を使用します（新規生成しません）"
else
    echo "[apt-key] 署名鍵を生成します: $NAME <$EMAIL>"
    # パスフレーズ無し（無人署名のため）。秘密鍵はオフホストで厳重に保管すること。
    gpg --batch --yes --pinentry-mode loopback --passphrase '' \
        --quick-generate-key "$NAME <$EMAIL>" rsa2048 sign 0
fi

echo "[apt-key] 現在の鍵:"
gpg --list-secret-keys --keyid-format=long
