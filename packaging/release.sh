#!/bin/bash
# ============================================================
# Kizuna-Eye リリース: ソースから検証済み APT リポジトリまで 1 コマンド。
#
# やること:
#   1. 前提チェック（docker / dpkg-dev / apt-utils / gpg）
#   2. 署名鍵が無ければ生成（apt-key.sh）
#   3. ubuntu:20.04 コンテナ内で本体をビルド → .deb 生成
#      （CGO のため、サポートする最古の 20.04 = glibc 2.31 でリンクする）
#   4. APT リポジトリを構築 + 署名（apt-repo.sh）
#   5. ubuntu:20.04 コンテナで `apt install kizuna-eye` を実検証
#
# これ 1 本で、ユーザー側は
#   sudo apt install kizuna-eye
# の一行だけで完結する（postinst がユーザー作成・設定生成・systemd 登録を全部やる）。
# One command from source to a verified apt repo: build the .deb inside
# ubuntu:20.04 (so it links against glibc 2.31 and runs on 20.04+), build and
# sign the apt repo, then install-by-name in a throwaway 20.04 container.
#
# 使い方:
#   sudo ./packaging/release.sh              # 20.04 でビルド + 検証
#   sudo ./packaging/release.sh ubuntu:22.04 # 検証イメージを指定
#   KIZUNA_SKIP_VERIFY=1 sudo ./packaging/release.sh  # 検証をスキップ
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ビルドは「サポートする最古の Ubuntu」で行う。20.04 = glibc 2.31 で
# リンクすれば、20.04 以降のすべてで動く（新しい libc でビルドすると
# 古い側で GLIBC_x.y not found になる）。
BUILD_IMAGE="${KIZUNA_BUILD_IMAGE:-ubuntu:20.04}"
VERIFY_IMAGE="${1:-${KIZUNA_VERIFY_IMAGE:-ubuntu:20.04}}"
SKIP_VERIFY="${KIZUNA_SKIP_VERIFY:-0}"

log() { echo "[release] $*"; }
die() { echo "❌ [release] $*" >&2; exit 1; }

# --- 1. 前提チェック ---
[ "$(id -u)" -eq 0 ] || die "root で実行してください（docker とリポジトリ署名に必要）: sudo $0"
command -v docker >/dev/null 2>&1 || die "docker が見つかりません（20.04 コンテナでのビルド・検証に必須）"
# dpkg-scanpackages は dpkg-dev、apt-ftparchive は apt-utils 由来。
command -v dpkg-scanpackages >/dev/null 2>&1 || die "dpkg-scanpackages がありません: sudo apt install dpkg-dev"
command -v gpg >/dev/null 2>&1 || die "gpg がありません: sudo apt install gnupg"

# --- 2. 署名鍵（初回のみ生成） ---
GNUPGHOME_DIR="${KIZUNA_APT_GNUPGHOME:-$ROOT/.gnupg-apt}"
if [ ! -d "$GNUPGHOME_DIR" ] || ! GNUPGHOME="$GNUPGHOME_DIR" gpg --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec:'; then
    log "署名鍵が無いため生成します（初回のみ）"
    ./packaging/apt-key.sh
else
    log "既存の署名鍵を使用: $GNUPGHOME_DIR"
fi

# --- 3. 20.04 コンテナで本体ビルド + .deb 生成 ---
log "ビルド: $BUILD_IMAGE 内で本体をビルド → .deb を生成"
./packaging/docker-build-deb.sh "$BUILD_IMAGE"

# --- 4. APT リポジトリ構築 + 署名 ---
log "APT リポジトリを構築 + 署名"
./packaging/apt-repo.sh

# --- 5. 隔離コンテナで apt install を実検証 ---
if [ "$SKIP_VERIFY" = "1" ]; then
    log "検証をスキップしました（KIZUNA_SKIP_VERIFY=1）"
else
    log "検証: $VERIFY_IMAGE で apt install kizuna-eye を実行"
    ./packaging/test-apt-repo.sh "$VERIFY_IMAGE"
fi

log "完了"
log "  リポジトリ: $ROOT/apt-repo"
log "  ユーザー側  : sudo apt install kizuna-eye"
log "  公開手順    : packaging/README.md の「公開（GitHub Pages の例）」を参照"
