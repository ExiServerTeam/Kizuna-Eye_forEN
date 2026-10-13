#!/bin/bash
# ============================================================
# Kizuna-Eye update.sh
# GitHub から更新し、本体とプラグインを再ビルドして再起動する。
#   1. git fetch で比較 → 同じなら「すでに最新版です」で終了
#   2. 更新前バイナリ/.so をバックアップ
#   3. pkg/module, pkg/status の変更を検出（全プラグイン再ビルド要否）
#   4. git pull（コンフリクト時は中止）
#   5. 本体＋（必要なら）プラグインを再ビルド
#   6. 再起動しプロセス生存を確認、失敗時はロールバック
#
# 使い方:
#   ./update.sh / --no-git / --no-restart / --no-plugin / --force-rebuild / --install
# ============================================================
set -u
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
PLUGIN_OUT_DIR="${KIZUNA_PLUGIN_OUT_DIR:-/opt/kizuna-eye/bin/plugins}"
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-/samba/share/Kizuna-Backup/Kizuna-Backup-LITE/plugin}"
SEC_PLUGIN_DIR="${KIZUNA_SEC_PLUGIN_DIR:-/samba/share/Kizuna-Security/plugin}"

DO_GIT=1; DO_RESTART=1; BUILD_PLUGINS=1; FORCE_REBUILD=0; DO_INSTALL=0
for arg in "$@"; do
    case "$arg" in
        --no-git) DO_GIT=0 ;;
        --no-restart) DO_RESTART=0 ;;
        --no-plugin) BUILD_PLUGINS=0 ;;
        --force-rebuild) FORCE_REBUILD=1 ;;
        --install|-i) DO_INSTALL=1 ;;
        -h|--help) echo "使い方: $0 [--no-git] [--no-restart] [--no-plugin] [--force-rebuild] [--install]"; exit 0 ;;
        *) echo "不明な引数: $arg"; exit 1 ;;
    esac
done

log()  { echo "$*"; }
warn() { echo "⚠️  $*"; }
err()  { echo "❌ $*"; }
ok()   { echo "✅ $*"; }

# root なら素で、そうでなければ sudo（NOPASSWD なら -n）で systemctl を叩く。
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then SUDO="sudo"; fi
fi

# systemd 管理下で動いているか。動いていれば stop.sh/start.sh ではなく
# systemctl restart を使う。そうしないと systemd の管理外でプロセスが
# 入れ替わり、「status は active なのに実体は別プロセス」という状態になる。
systemd_active() {
    command -v systemctl >/dev/null 2>&1 || return 1
    $SUDO systemctl is-active --quiet kizuna-eye 2>/dev/null
}

# ---- 0. --install 指定時は不足パッケージを install.sh に委譲して導入 ----
# （以前は --install を解析するだけで何もしていなかった＝無効なフラグだった）
if [ "$DO_INSTALL" -eq 1 ]; then
    if [ -x ./install.sh ]; then
        log "▶ 不足パッケージを確認/導入します (install.sh)..."
        ./install.sh --no-build --no-start --no-sudoers || warn "パッケージ導入で警告がありました（続行します）。"
    else
        warn "--install が指定されましたが install.sh が見つかりません。"
    fi
fi

# ---- 1. git チェック & 最新確認 ----
BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo master)"
REMOTE_NAME="$(git remote 2>/dev/null | head -n1)"
if [ "$DO_GIT" -eq 1 ]; then
    [ -d .git ] || { err "git リポジトリではありません: $PWD"; exit 1; }
    command -v git >/dev/null 2>&1 || { err "git が見つかりません。install.sh を実行してください。"; exit 1; }
    [ -n "$REMOTE_NAME" ] || { err "git リモートが設定されていません"; exit 1; }
    log "ブランチ: $BRANCH / リモート: $REMOTE_NAME"
    log "▶ GitHub から最新を確認中..."
    FETCH_OK=1
    if ! git fetch --prune "$REMOTE_NAME" "$BRANCH" >/dev/null 2>&1; then
        FETCH_OK=0
    fi
    LOCAL="$(git rev-parse HEAD)"
    REMOTE="$(git rev-parse --verify --quiet "$REMOTE_NAME/$BRANCH" 2>/dev/null || true)"

    # 最新を確認できないのに「すでに最新」と誤判定して更新をスキップすると、
    # サイレントに更新されない事故になる（fetch 失敗時に REMOTE を LOCAL へ
    # フォールバックしていたのが原因）。確認できない場合は中止する。
    # 再ビルドだけしたい場合は --force-rebuild / --no-git を使う。
    if [ "$FETCH_OK" -eq 0 ] || [ -z "$REMOTE" ]; then
        if [ "$FORCE_REBUILD" -eq 1 ]; then
            warn "最新を確認できませんでしたが --force-rebuild のため再ビルドを続行します。"
        else
            err "最新を取得できませんでした（fetch 失敗 or 追跡ブランチ $REMOTE_NAME/$BRANCH 無し）。"
            err "ネットワーク/認証を確認してください。再ビルドのみなら --force-rebuild を付けてください。"
            exit 1
        fi
    elif [ "$LOCAL" = "$REMOTE" ] && [ "$FORCE_REBUILD" -eq 0 ]; then
        ok "すでに最新版です ($(git describe --tags --always 2>/dev/null || echo "$LOCAL"))"
        exit 0
    else
        log "更新: $LOCAL → $REMOTE"
    fi
fi

# ---- 2. バックアップ ----
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_ROOT="${KIZUNA_BACKUP_DIR:-/var/backups/kizuna-eye}"
if ! mkdir -p "$BACKUP_ROOT" 2>/dev/null || [ ! -w "$BACKUP_ROOT" ]; then
    BACKUP_ROOT="$HOME/.kizuna-eye/backup"
    mkdir -p "$BACKUP_ROOT" 2>/dev/null || { err "バックアップ先を作成できません"; exit 1; }
    warn "/var/backups に書けないため $BACKUP_ROOT を使用します"
fi
ROLLBACK_DIR="$BACKUP_ROOT/$STAMP"
mkdir -p "$ROLLBACK_DIR" || { err "バックアップディレクトリ作成失敗"; exit 1; }
for f in agent_linux dashboard_linux plugin-inspect; do
    [ -f "$BIN_DIR/$f" ] && cp -a "$BIN_DIR/$f" "$ROLLBACK_DIR/$f" || true
done
if [ -d "$PLUGIN_OUT_DIR" ]; then
    mkdir -p "$ROLLBACK_DIR/plugins"
    cp -a "$PLUGIN_OUT_DIR"/*.so "$ROLLBACK_DIR/plugins/" 2>/dev/null || true
fi
ok "バックアップ: $ROLLBACK_DIR"
PREV_VERSION="$(git describe --tags --always 2>/dev/null || echo unknown)"

rollback() {
    warn "ロールバックします: $ROLLBACK_DIR"
    for f in agent_linux dashboard_linux plugin-inspect; do
        [ -f "$ROLLBACK_DIR/$f" ] && cp -a "$ROLLBACK_DIR/$f" "$BIN_DIR/$f"
    done
    [ -d "$ROLLBACK_DIR/plugins" ] && cp -a "$ROLLBACK_DIR/plugins"/*.so "$PLUGIN_OUT_DIR/" 2>/dev/null || true
}

# ---- 3. 変更検出 ----
NEED_PLUGIN_REBUILD=0
if [ "$DO_GIT" -eq 1 ]; then
    CHANGED="$(git diff --name-only HEAD "$REMOTE_NAME/$BRANCH" 2>/dev/null || true)"
    if echo "$CHANGED" | grep -qE '^pkg/(module|status)/'; then
        NEED_PLUGIN_REBUILD=1
        warn "pkg/module または pkg/status が変更 → 全プラグイン再ビルドが必要"
    fi
fi

# ---- 4. コード更新 ----
if [ "$DO_GIT" -eq 1 ]; then
    if ! git diff --quiet || ! git diff --cached --quiet; then
        err "ローカルに未コミットの変更があります。コミット/退避してから再実行してください。"; exit 1
    fi
    log "▶ git pull 中..."
    if ! git pull --ff-only "$REMOTE_NAME" "$BRANCH"; then
        err "git pull に失敗（コンフリクト/分岐）。更新を中止します。"; exit 1
    fi
    ok "コード更新完了"
fi

# ---- 5. 再ビルド ----
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo v0.7.1)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${VERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

log ""
log "▶ 本体（Eye）を再ビルド中... (version=${VERSION})"
build_one() { CGO_ENABLED=1 go build -buildvcs=false -ldflags "$LDFLAGS" -o "$BIN_DIR/$1" "$2"; }
if ! build_one plugin-inspect ./cmd/plugin-inspect \
   || ! build_one dashboard_linux ./cmd/dashboard \
   || ! build_one agent_linux ./cmd/agent; then
    rollback; err "本体ビルド失敗。ロールバックしました。"; exit 1
fi
ok "本体再ビルド完了"

build_plugin() {
    local dir="$1" name="$2"
    [ -d "$dir" ] || { log "ℹ️  プラグインソース無し: $dir"; return 0; }
    grep -rqsE '^package main' "$dir"/*.go || { warn "$name: main パッケージでないためスキップ"; return 0; }
    log "🔌 ビルド: $name"
    ( cd "$dir" && GOWORK=off GOTOOLCHAIN=auto CGO_ENABLED=1 go build -buildvcs=false -buildmode=plugin -o "$PLUGIN_OUT_DIR/${name}.so" . )
}
if [ "$BUILD_PLUGINS" -eq 1 ]; then
    log ""
    log "▶ プラグインを確認中..."
    if [ "$NEED_PLUGIN_REBUILD" -eq 1 ] || [ ! -f "$PLUGIN_OUT_DIR/kizuna_backup_lite.so" ]; then
        if ! build_plugin "$LITE_PLUGIN_DIR" "kizuna_backup_lite"; then rollback; err "LITE プラグインビルド失敗。ロールバックしました。"; exit 1; fi
    else
        log "ℹ️  kizuna_backup_lite.so は最新（再ビルド不要）"
    fi
    if [ "$NEED_PLUGIN_REBUILD" -eq 1 ] || [ ! -f "$PLUGIN_OUT_DIR/kizuna_security.so" ]; then
        if ! build_plugin "$SEC_PLUGIN_DIR" "kizuna_security"; then rollback; err "Security プラグインビルド失敗。ロールバックしました。"; exit 1; fi
    else
        log "ℹ️  kizuna_security.so は最新（再ビルド不要）"
    fi
fi

# ---- 6. 再起動 & 生存確認 ----
# 運用モードに応じて再起動方法を切り替える:
#   - systemd 管理下 → systemctl restart kizuna-eye
#   - 手動管理       → stop.sh → start.sh
if [ "$DO_RESTART" -eq 1 ]; then
    log ""
    USE_SYSTEMD=0
    if systemd_active; then USE_SYSTEMD=1; fi

    restart_manual() { ./stop.sh || true; sleep 1; ./start.sh; }
    restart_systemd() { $SUDO systemctl restart kizuna-eye; }
    rollback_restart() {
        rollback
        if [ "$USE_SYSTEMD" -eq 1 ]; then $SUDO systemctl restart kizuna-eye || true; else ./stop.sh || true; ./start.sh || true; fi
    }

    if [ "$USE_SYSTEMD" -eq 1 ]; then
        log "▶ systemd 管理下を検知。systemctl restart で再起動します..."
        if ! restart_systemd; then
            rollback_restart
            err "systemd 再起動に失敗。ロールバックして再起動しました。"; exit 1
        fi
        sleep 3
        if ! systemd_active; then
            rollback_restart
            err "再起動後 kizuna-eye が active ではありません。ロールバックしました。"; exit 1
        fi
    else
        log "▶ 手動管理モード。stop.sh → start.sh で再起動します..."
        if ! restart_manual; then
            rollback_restart
            err "起動に失敗。ロールバックして再起動しました。"; exit 1
        fi
        sleep 3
        if ! pgrep -f "$BIN_DIR/dashboard_linux" >/dev/null 2>&1 || ! pgrep -f "$BIN_DIR/agent_linux" >/dev/null 2>&1; then
            rollback_restart
            err "起動後の生存確認に失敗。ロールバックして再起動しました。"; exit 1
        fi
    fi
    ok "再起動完了"
fi

# ---- 7. 完了 ----
NEW_VERSION="$(git describe --tags --always 2>/dev/null || echo "$VERSION")"
log ""
log "============================================================"
ok "アップデート成功"
log "  バージョン: ${PREV_VERSION} → ${NEW_VERSION}"
log "  バックアップ: $ROLLBACK_DIR"
log "============================================================"
