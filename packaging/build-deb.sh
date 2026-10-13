#!/bin/bash
# ============================================================
# Kizuna-Eye .deb ビルドスクリプト（dpkg-deb 使用・nfpm 不要）
#
# 前提: 対象環境で ./build.sh 済み（/opt/kizuna-eye/bin にバイナリがある）。
# 使い方:
#   ./packaging/build-deb.sh
#   → dist/kizuna-eye_<version>_<arch>.deb を生成
#
# 注意: CGO のため、サポートする一番古い Ubuntu/Debian で実行すること。
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION_RAW="$(cat VERSION 2>/dev/null || echo v0.7.0)"
VERSION="${VERSION_RAW#v}"
ARCH="${KIZUNA_DEB_ARCH:-amd64}"
PKG="kizuna-eye"
BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
OUT_DIR="$ROOT/dist"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

log() { echo "[build-deb] $*"; }

# --- バイナリの存在確認 ---
for b in agent_linux dashboard_linux plugin-inspect plugin-sign; do
    if [ ! -x "$BIN_DIR/$b" ]; then
        echo "❌ $BIN_DIR/$b がありません。先に ./build.sh を実行してください。" >&2
        exit 1
    fi
done

log "version=$VERSION arch=$ARCH"

# --- DEBIAN メタデータ ---
mkdir -p "$STAGE/DEBIAN"
cat > "$STAGE/DEBIAN/control" <<EOF
Package: $PKG
Version: $VERSION
Section: admin
Priority: optional
Architecture: $ARCH
Depends: python3, smartmontools, rsync, bubblewrap, adduser
Maintainer: Kizuna-Eye Project
Description: Lightweight server monitoring tool
 Kizuna-Eye is a lightweight server monitoring tool written in Go,
 designed for low-spec servers. It provides a dashboard and an agent
 with plugin-based security monitoring.
EOF

install -m 0755 packaging/postinst.sh "$STAGE/DEBIAN/postinst"
install -m 0755 packaging/prerm.sh    "$STAGE/DEBIAN/prerm"
install -m 0755 packaging/postrm.sh   "$STAGE/DEBIAN/postrm"

# --- バイナリ ---
install -d "$STAGE/opt/kizuna-eye/bin"
for b in agent_linux dashboard_linux plugin-inspect plugin-sign; do
    install -m 0755 "$BIN_DIR/$b" "$STAGE/opt/kizuna-eye/bin/$b"
done

# --- 読み取り専用データ（web / example / systemd unit） ---
install -d "$STAGE/usr/share/kizuna-eye/web" "$STAGE/usr/share/kizuna-eye/examples" \
           "$STAGE/usr/share/kizuna-eye/systemd"
cp -a web/static "$STAGE/usr/share/kizuna-eye/web/static"
cp dashboard_config.example.json agent_config.example.json modules.json.example \
   "$STAGE/usr/share/kizuna-eye/examples/" 2>/dev/null || true
cp systemd/kizuna-eye-agent.service systemd/kizuna-dashboard.service \
   "$STAGE/usr/share/kizuna-eye/systemd/"

# パッケージ向けに unit を調整する。リポジトリ固有のパス（/samba/share/...）は
# パッケージ先に存在しないため、ReadWritePaths をデータ領域だけに絞り、
# Documentation を同梱ドキュメントへ向ける。
for u in "$STAGE/usr/share/kizuna-eye/systemd/"*.service; do
    sed -i -E 's|^ReadWritePaths=.*|ReadWritePaths=/var/lib/kizuna-eye|' "$u"
    sed -i -E 's|^Documentation=.*|Documentation=file:///usr/share/doc/kizuna-eye/README.md|' "$u"
done

# 権限の正規化。共有(SMB)上では 777/664 等になっているため、パッケージ内は
# ディレクトリ 0755 / ファイル 0644 に統一する（バイナリは後段で 0755）。
find "$STAGE/usr/share/kizuna-eye" -type d -exec chmod 0755 {} +
find "$STAGE/usr/share/kizuna-eye" -type f -exec chmod 0644 {} +
find "$STAGE/usr/share/doc/kizuna-eye" -type f -exec chmod 0644 {} + 2>/dev/null || true

# --- ドキュメント ---
install -d "$STAGE/usr/share/doc/$PKG"
cp README.md CHANGELOG.md LICENSE "$STAGE/usr/share/doc/$PKG/" 2>/dev/null || true

# --- 構築 ---
mkdir -p "$OUT_DIR"
DEB="$OUT_DIR/${PKG}_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$STAGE" "$DEB" >/dev/null

log "完了: $DEB"
log "確認: dpkg-deb -I $DEB"
log "導入: sudo apt install $DEB"
