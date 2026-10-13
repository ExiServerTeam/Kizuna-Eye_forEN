#!/bin/bash
# ============================================================
# Kizuna-Eye APT リポジトリ構築
#
# dist/*.deb から APT リポジトリ（apt-repo/）を作る。
#   pool/           … .deb 本体
#   dists/stable/   … Packages / Release / 署名
#   kizuna.gpg      … 公開鍵（ユーザーが登録する）
#
# 前提: 先に ./packaging/build-deb.sh と ./packaging/apt-key.sh を実行。
# 使い方:
#   ./packaging/apt-repo.sh
#   → apt-repo/ を gh-pages ブランチや Web サーバーへ配置
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

REPO_DIR="${KIZUNA_APT_REPO_DIR:-$ROOT/apt-repo}"
GNUPGHOME_DIR="${KIZUNA_APT_GNUPGHOME:-$ROOT/.gnupg-apt}"
SUITE="${KIZUNA_APT_SUITE:-stable}"
COMPONENT="main"
ARCH="${KIZUNA_DEB_ARCH:-amd64}"
PKG="kizuna-eye"

log() { echo "[apt-repo] $*"; }

# --- .deb を集める ---
# compgen -G でグロブ展開する。パターンを 1 つの引用文字列として渡せるため、
# 意図しない単語分割・パス名展開を避けつつ mapfile で確実に配列化できる
# （`DEBS=( ... * ... )` 形式は shellcheck の SC2206/SC2140 を招く）。
mapfile -t DEBS < <(compgen -G "$ROOT/dist/${PKG}_*_${ARCH}.deb" || true)
if [ ${#DEBS[@]} -eq 0 ]; then
    echo "❌ dist/ に .deb がありません。先に ./packaging/build-deb.sh を実行してください。" >&2
    exit 1
fi

log "リポジトリ再構築: $REPO_DIR"
# 安全ガード: REPO_DIR を丸ごと消すため、空・"/"・ホーム直下・リポジトリ直下
# 以外の「想定外に浅いパス」を拒否する。KIZUNA_APT_REPO_DIR の誤設定で
# `rm -rf /` や `rm -rf $HOME` を実行しないようにする（過去に root の
# ワイルドカード操作で chmod 600 /* を踏んだ事故と同種の防止）。
case "$REPO_DIR" in
  ""|"/"|"$HOME"|"$ROOT")
    echo "❌ 安全のため REPO_DIR を削除しません（想定外のパス: '$REPO_DIR'）。" >&2
    echo "   KIZUNA_APT_REPO_DIR を確認してください（既定: <repo>/apt-repo）。" >&2
    exit 1
    ;;
esac
# 親ディレクトリが存在し、かつ書き込み可能であることも確認する。
if [ ! -w "$(dirname "$REPO_DIR")" ] && [ ! -e "$REPO_DIR" ]; then
    echo "❌ 出力先の親ディレクトリに書き込めません: $(dirname "$REPO_DIR")" >&2
    exit 1
fi
rm -rf "$REPO_DIR"
mkdir -p "$REPO_DIR/pool/$COMPONENT/k/$PKG"
mkdir -p "$REPO_DIR/dists/$SUITE/$COMPONENT/binary-$ARCH"

# --- pool へ配置 ---
for d in "${DEBS[@]}"; do
    cp "$d" "$REPO_DIR/pool/$COMPONENT/k/$PKG/"
    log "追加: $(basename "$d")"
done

# --- Packages 生成（dpkg-scanpackages） ---
# override ファイルは /dev/null なので "missing from override file" 警告が出るが、
# これは実害が無い（Packages 本体は stdout に正しく書かれる）。stderr を抑制する。
( cd "$REPO_DIR" && dpkg-scanpackages "pool/$COMPONENT" /dev/null 2>/dev/null \
    > "dists/$SUITE/$COMPONENT/binary-$ARCH/Packages" )
gzip -9c "$REPO_DIR/dists/$SUITE/$COMPONENT/binary-$ARCH/Packages" \
    > "$REPO_DIR/dists/$SUITE/$COMPONENT/binary-$ARCH/Packages.gz"

# --- Release 生成（apt-ftparchive が無いので手動） ---
PKG_REL="$COMPONENT/binary-$ARCH/Packages"
PKG_GZ_REL="$COMPONENT/binary-$ARCH/Packages.gz"
(
    cd "$REPO_DIR/dists/$SUITE"
    {
        echo "Origin: Kizuna-Eye"
        echo "Label: Kizuna-Eye"
        echo "Suite: $SUITE"
        echo "Codename: $SUITE"
        echo "Architectures: $ARCH"
        echo "Components: $COMPONENT"
        echo "Description: Kizuna-Eye APT repository"
        echo "Date: $(date -Ru)"
        echo "MD5Sum:"
        for f in "$PKG_REL" "$PKG_GZ_REL"; do
            printf ' %s %s %s\n' "$(md5sum "$f" | awk '{print $1}')" "$(stat -c %s "$f")" "$f"
        done
        echo "SHA256:"
        for f in "$PKG_REL" "$PKG_GZ_REL"; do
            printf ' %s %s %s\n' "$(sha256sum "$f" | awk '{print $1}')" "$(stat -c %s "$f")" "$f"
        done
    } > Release
)

# --- 署名 ---
if [ ! -d "$GNUPGHOME_DIR" ]; then
    echo "❌ 署名鍵がありません。先に ./packaging/apt-key.sh を実行してください。" >&2
    exit 1
fi
# gpg は homedir が「実行ユーザー所有・0700」でないと "unsafe ownership" 警告を
# 出す。Samba 共有など所有者がずれやすい環境で黙らせるため、ここで揃える。
# gpg warns on a homedir not owned by the running user; align it here so a
# Samba share (where ownership drifts) does not print the warning.
chmod 700 "$GNUPGHOME_DIR" 2>/dev/null || true
chown -R "$(id -u):$(id -g)" "$GNUPGHOME_DIR" 2>/dev/null || true
export GNUPGHOME="$GNUPGHOME_DIR"
KEYID="$(gpg --list-secret-keys --with-colons 2>/dev/null | awk -F: '/^sec:/ {print $5; exit}')"
if [ -z "$KEYID" ]; then
    echo "❌ 秘密鍵が見つかりません。./packaging/apt-key.sh を実行してください。" >&2
    exit 1
fi
gpg --batch --yes --armor --detach-sign -u "$KEYID" \
    -o "$REPO_DIR/dists/$SUITE/Release.gpg" "$REPO_DIR/dists/$SUITE/Release"
gpg --batch --yes --clearsign -u "$KEYID" \
    -o "$REPO_DIR/dists/$SUITE/InRelease" "$REPO_DIR/dists/$SUITE/Release"
gpg --armor --export "$KEYID" > "$REPO_DIR/kizuna.gpg"
gpg --export "$KEYID" > "$REPO_DIR/kizuna-keyring.gpg"

log "署名完了 (key $KEYID)"
log "公開鍵: $REPO_DIR/kizuna.gpg / kizuna-keyring.gpg"
log "完了: $REPO_DIR"
