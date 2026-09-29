#!/bin/bash
# ============================================================
# Kizuna-Eye update.sh
# アップデート用。以下を行う:
#   1. 必要なパッケージのうち「足りないものだけ」を差分インストール
#   2. 本体（agent/dashboard/plugin-inspect）を再ビルド
#   3. プラグイン（.so）を同一ソースから自動ビルド
#   4. 失敗時は前のバイナリへロールバック（safe_update 相当）
#
# 重要: Go プラグインは共有パッケージのハッシュ完全一致が必須。
#       pkg/status / pkg/module を変更した場合は、本体と全プラグインを
#       同一ソースから「同時に」再ビルドする必要がある。
#
# 使い方:
#   ./update.sh                 # パッケージ確認＋ビルド＋プラグインビルド
#   ./update.sh --install       # 足りないパッケージも実際に導入
#   ./update.sh --no-plugin     # プラグインビルドをスキップ
#   ./update.sh --plugin-dir DIR  # プラグインソースのルートを指定
# ============================================================
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

BIN_DIR="/opt/kizuna-eye/bin"
PLUGIN_OUT_DIR="/opt/kizuna-eye/bin/plugins"
BACKUP_DIR="/opt/kizuna-eye/backup"

DO_INSTALL=0
BUILD_PLUGINS=1
PLUGIN_SRC_ROOT="${KIZUNA_PLUGIN_SRC_ROOT:-}"

while [ $# -gt 0 ]; do
    case "$1" in
        --install|-i) DO_INSTALL=1 ;;
        --no-plugin)  BUILD_PLUGINS=0 ;;
        --plugin-dir) PLUGIN_SRC_ROOT="$2"; shift ;;
        -h|--help)
            echo "使い方: $0 [--install] [--no-plugin] [--plugin-dir DIR]"
            exit 0
            ;;
        *) echo "不明な引数: $1"; exit 1 ;;
    esac
    shift
done

# ---- パッケージマネージャ判定（apt / dnf / yum / pacman）----
PM=""
INSTALL_CMD=""
if command -v apt-get >/dev/null 2>&1; then
    PM="apt"; INSTALL_CMD="apt-get install -y"
elif command -v dnf >/dev/null 2>&1; then
    PM="dnf"; INSTALL_CMD="dnf install -y"
elif command -v yum >/dev/null 2>&1; then
    PM="yum"; INSTALL_CMD="yum install -y"
elif command -v pacman >/dev/null 2>&1; then
    PM="pacman"; INSTALL_CMD="pacman -S --noconfirm"
fi

# ---- 必須コマンドと提供パッケージ ----
# cmd:package の対応で持つ。
REQUIRED=(
    "rsync:rsync"
    "smartctl:smartmontools"
    "git:git"
    "curl:curl"
)

MISSING_PKGS=()
for entry in "${REQUIRED[@]}"; do
    cmd="${entry%%:*}"
    pkg="${entry##*:}"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "❌ $cmd （未導入 → $pkg）"
        MISSING_PKGS+=("$pkg")
    else
        echo "✅ $cmd"
    fi
done

if [ ${#MISSING_PKGS[@]} -gt 0 ]; then
    echo ""
    if [ -z "$PM" ]; then
        echo "⚠️  パッケージマネージャ未検出。手動で導入してください: ${MISSING_PKGS[*]}"
    elif [ "$DO_INSTALL" -eq 1 ]; then
        echo "▶ 不足分のみインストール: sudo $INSTALL_CMD ${MISSING_PKGS[*]}"
        sudo $INSTALL_CMD "${MISSING_PKGS[@]}"
    else
        echo "不足分があります。導入するには:"
        echo "    sudo $INSTALL_CMD ${MISSING_PKGS[*]}"
        echo "または --install を付けて再実行してください。"
    fi
fi

# ============================================================
# ビルド（失敗時にロールバックできるよう、事前にバックアップ）
# ============================================================
mkdir -p "$BIN_DIR" "$BACKUP_DIR" "$PLUGIN_OUT_DIR"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ROLLBACK_DIR="$BACKUP_DIR/$STAMP"
mkdir -p "$ROLLBACK_DIR"

backup_existing() {
    for f in agent_linux dashboard_linux plugin-inspect; do
        if [ -f "$BIN_DIR/$f" ]; then
            cp -a "$BIN_DIR/$f" "$ROLLBACK_DIR/$f"
        fi
    done
}

rollback() {
    echo "⚠️  ビルド失敗。前のバイナリへロールバックします..."
    for f in agent_linux dashboard_linux plugin-inspect; do
        if [ -f "$ROLLBACK_DIR/$f" ]; then
            cp -a "$ROLLBACK_DIR/$f" "$BIN_DIR/$f"
            echo "  復元: $f"
        fi
    done
}

backup_existing

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo v0.0.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"

echo ""
echo "🔨 本体をビルド中... (version=${VERSION})"
if ! CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/plugin-inspect" ./cmd/plugin-inspect \
   || ! CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/dashboard_linux" ./cmd/dashboard \
   || ! CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$BIN_DIR/agent_linux" ./cmd/agent; then
    rollback
    exit 1
fi
echo "✅ 本体ビルド完了"

# ============================================================
# プラグイン（.so）を同一ソースから自動ビルド
# pkg/status / pkg/module の変更時は必須。
# ============================================================
build_plugins() {
    # 探索ルート（優先順）:
    #   1. --plugin-dir / KIZUNA_PLUGIN_SRC_ROOT
    #   2. リポジトリ隣接の Kizuna-Security/plugin など
    local roots=()
    [ -n "$PLUGIN_SRC_ROOT" ] && roots+=("$PLUGIN_SRC_ROOT")
    roots+=("../Kizuna-Security/plugin" "../kizuna-plugins" "./plugins-src")

    local found=0
    for root in "${roots[@]}"; do
        [ -d "$root" ] || continue
        # main パッケージ（plugin ビルド可能）なディレクトリを探す。
        while IFS= read -r dir; do
            [ -f "$dir/main.go" ] || continue
            # buildmode=plugin でビルドできるのは main パッケージのみ。
            local name
            name="$(basename "$dir")"
            local out="$PLUGIN_OUT_DIR/${name}.so"
            echo "🔌 プラグインをビルド: $dir → $out"
            if ! ( cd "$dir" && GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o "$out" . ); then
                echo "❌ プラグインビルド失敗: $dir"
                return 1
            fi
            found=1
        done < <(find "$root" -maxdepth 3 -type d 2>/dev/null)
    done

    if [ "$found" -eq 0 ]; then
        echo "ℹ️  プラグインソースが見つかりませんでした（--plugin-dir で指定できます）。"
    fi
    return 0
}

if [ "$BUILD_PLUGINS" -eq 1 ]; then
    echo ""
    echo "🔌 プラグインをビルド中..."
    if ! build_plugins; then
        echo "⚠️  プラグインのビルドに失敗しました。本体は更新済みです。"
        echo "    plugin was built with a different version of package ... を避けるため、"
        echo "    pkg/status / pkg/module を変更した場合は必ず全プラグインを再ビルドしてください。"
        exit 2
    fi
fi

echo ""
echo "✅ アップデート完了 (version=${VERSION})"
echo "   バックアップ: $ROLLBACK_DIR"
echo "   再起動するには: ./stop.sh && ./start.sh"
