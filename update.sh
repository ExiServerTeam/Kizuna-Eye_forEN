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
SIGN_BIN="$BIN_DIR/plugin-sign"
# 署名鍵は「実行ユーザー（SUDO_USER）」のホーム基準で探す。sudo 実行時に
# $HOME をそのまま使うと /root 配下になり、install.sh が作った鍵を見つけられない。
RUN_HOME="$HOME"
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    RH="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
    [ -n "$RH" ] && RUN_HOME="$RH"
fi
SIGNING_KEY="${KIZUNA_SIGNING_KEY:-$RUN_HOME/.kizuna-eye/keys/plugin_signing/plugin_signing.key}"
# プラグインのソース位置（install.sh と同じ規則）。Kizuna-Security も
# Kizuna-Backup LITE も本体とは別の場所に置く独立モジュールのため、
# 対応する環境変数を指定したときのみ扱う（opt-in）。
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-}"
SEC_PLUGIN_DIR="${KIZUNA_SEC_PLUGIN_DIR:-}"

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
#
# H-1 以降は agent 専用ユーザー版の kizuna-eye-agent.service が正。旧
# kizuna-agent.service / 旧 system service kizuna-eye.service も後方互換で見る。
# 再起動に使うユニット名は SYSTEMD_UNIT に格納する。
# dashboard と agent は別 unit。両方を検出して再起動しないと、新バイナリを
# 配置しても片方が古いまま動き続ける。検出した unit 名は SYSTEMD_UNITS に入れる。
SYSTEMD_UNITS=""
detect_systemd_units() {
    command -v systemctl >/dev/null 2>&1 || return 1
    local u found=""
    for u in kizuna-dashboard kizuna-eye-agent kizuna-agent kizuna-eye; do
        if $SUDO systemctl is-active --quiet "$u" 2>/dev/null \
           || $SUDO systemctl is-enabled --quiet "$u" 2>/dev/null; then
            found="$found $u"
        fi
    done
    [ -n "$found" ] || return 1
    SYSTEMD_UNITS="$found"
    return 0
}
systemd_active() { detect_systemd_units; }

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
for f in agent_linux dashboard_linux plugin-inspect plugin-sign; do
    [ -f "$BIN_DIR/$f" ] && cp -a "$BIN_DIR/$f" "$ROLLBACK_DIR/$f" || true
done
if [ -d "$PLUGIN_OUT_DIR" ]; then
    mkdir -p "$ROLLBACK_DIR/plugins"
    cp -a "$PLUGIN_OUT_DIR"/*.so "$ROLLBACK_DIR/plugins/" 2>/dev/null || true
    # 署名ファイル (.so.sig) も必ず退避する。rollback が .so だけ戻して新しい
    # .sig を残すと、旧 .so と新 .sig が不一致になり、require_signature=true の
    # 環境ではロールバック後のプラグインが署名検証に失敗してロードされない
    # （エラーログだけを残して静かに監視が止まる）。
    cp -a "$PLUGIN_OUT_DIR"/*.so.sig "$ROLLBACK_DIR/plugins/" 2>/dev/null || true
fi
ok "バックアップ: $ROLLBACK_DIR"
PREV_VERSION="$(git describe --tags --always 2>/dev/null || echo unknown)"

rollback() {
    warn "ロールバックします: $ROLLBACK_DIR"
    for f in agent_linux dashboard_linux plugin-inspect plugin-sign; do
        [ -f "$ROLLBACK_DIR/$f" ] && cp -a "$ROLLBACK_DIR/$f" "$BIN_DIR/$f"
    done
    if [ -d "$ROLLBACK_DIR/plugins" ]; then
        cp -a "$ROLLBACK_DIR/plugins"/*.so "$PLUGIN_OUT_DIR/" 2>/dev/null || true
        # .so と .sig は必ずペアで戻す（片方だけだと署名検証が失敗し、
        # fail-closed でプラグインがロードされない）。
        cp -a "$ROLLBACK_DIR/plugins"/*.so.sig "$PLUGIN_OUT_DIR/" 2>/dev/null || true
    fi
}

# ---- 3. 変更検出 ----
NEED_PLUGIN_REBUILD=0
if [ "$DO_GIT" -eq 1 ]; then
    CHANGED="$(git diff --name-only HEAD "$REMOTE_NAME/$BRANCH" 2>/dev/null || true)"
    # プラグインが import する共有パッケージは pkg/module だけではない。
    # 出荷している Kizuna-Security は pkg/fsutil も import しており、そこを
    # 変更すると .so のビルドハッシュが変わる（plugin was built with a
    # different version of package Kizuna-Eye/pkg/fsutil）。一覧に無いと
    # 「変更検出漏れ → 再ビルドせず → ABI 不一致で全プラグインがロード不能」
    # になるため、プラグインが import する共有パッケージを網羅する。
    if echo "$CHANGED" | grep -qE '^pkg/(module|status|fsutil)/'; then
        NEED_PLUGIN_REBUILD=1
        warn "共有パッケージ (pkg/module, pkg/status, pkg/fsutil) が変更 → 全プラグイン再ビルドが必要"
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
# Prefer the repo VERSION file (single source of truth), then the git tag.
# 変数名は KVERSION にする。環境によっては VERSION が別用途で設定されており
# （例: . /etc/os-release）、その値を拾うと ldflags が壊れてビルドに失敗する。
KVERSION="${KIZUNA_VERSION:-$(cat VERSION 2>/dev/null || git describe --tags --always 2>/dev/null || echo v0.8.0)}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X Kizuna-Eye/internal/api.Version=${KVERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

log ""
log "▶ 本体（Eye）を再ビルド中... (version=${KVERSION})"

# ----------------------------------------------------------------------------
# ABI: build host AND plugins under ONE combined go.work.
#
# ホストは go.work 有効でビルドされ、プラグインは GOWORK=off（=プラグイン側の
# replace Kizuna-Eye => ...）でビルドされると、共有パッケージ
# (pkg/module, pkg/status, pkg/fsutil) の build ID が食い違う。同じソースでも
# モジュール解決方法が違えばハッシュが変わるため、同一ツールチェーン・同一
# フラグでも plugin.Open が
#     plugin was built with a different version of package Kizuna-Eye/pkg/fsutil
# で失敗し、更新後に全プラグインがロード不能になる（実機 2026-10-10 で発生）。
# build-all.sh と同じく、ホストとプラグインを 1 つの combined go.work の下で
# ビルドしてハッシュを一致させる。
# ----------------------------------------------------------------------------
WORKFILE=""
cleanup_work() { [ -n "$WORKFILE" ] && rm -f "$WORKFILE"; }
trap cleanup_work EXIT
if [ -n "$SEC_PLUGIN_DIR" ] || [ -n "$LITE_PLUGIN_DIR" ]; then
    WORKFILE="$(mktemp /tmp/kizuna-update-XXXXXX.work)"
    {
        printf 'go 1.27.1\n'
        printf 'toolchain go1.27.2\n\n'
        printf 'use (\n'
        printf '\t%s\n' "$PWD"
        [ -n "$SEC_PLUGIN_DIR" ]  && [ -d "$SEC_PLUGIN_DIR" ]  && printf '\t%s\n' "$SEC_PLUGIN_DIR"
        [ -n "$LITE_PLUGIN_DIR" ] && [ -d "$LITE_PLUGIN_DIR" ] && printf '\t%s\n' "$LITE_PLUGIN_DIR"
        printf ')\n'
    } > "$WORKFILE"
    log "ℹ️  combined go.work を使用: $WORKFILE"
fi
build_one() { CGO_ENABLED=1 GOWORK=${WORKFILE:-auto} go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$BIN_DIR/$1" "$2"; }
if ! build_one plugin-inspect ./cmd/plugin-inspect \
   || ! build_one dashboard_linux ./cmd/dashboard \
   || ! build_one agent_linux ./cmd/agent \
   || ! build_one plugin-sign ./cmd/plugin-sign; then
    rollback; err "本体ビルド失敗。ロールバックしました。"; exit 1
fi
ok "本体再ビルド完了"

# ---- 5.5 ビルド後の権限正規化 (A-4) ----
# update.sh を sudo で実行すると、go build が生成する agent_linux は
# root:root 0755 になり、install.sh / 運用が設定する「0750 root:kizuna-eye
# （agent 専用ユーザーのみ実行可）」から後退する（実機で発生）。agent は
# kizuna-eye で動くため、グループ実行権を保った最小権限に戻す
# （install.sh:903 の推奨と同じ）。kizuna-eye グループが無い環境
# （A-4 未適用）では何もしない。
if [ "$(id -u)" -eq 0 ] && getent group kizuna-eye >/dev/null 2>&1; then
    if [ -f "$BIN_DIR/agent_linux" ]; then
        chown root:kizuna-eye "$BIN_DIR/agent_linux" 2>/dev/null || true
        chmod 0750 "$BIN_DIR/agent_linux" 2>/dev/null || true
    fi
fi
ok "権限を正規化しました (agent_linux: 0750 root:kizuna-eye)"

# ワンクリック対処サービス（特権ソケット）の unit / server を配備する。
# 既存インストールでも socket 方式へ移行できるよう、更新時に流す（冪等）。
ACTION_SERVER_SRC="scripts/kizuna-action-server"
ACTION_HELPER_SRC="scripts/kizuna-action.sh"
if [ -f "$ACTION_SERVER_SRC" ] && [ -f systemd/kizuna-action.socket ]; then
    if [ -n "$SUDO" ] || [ "$(id -u)" -eq 0 ]; then
        $SUDO install -d -m 0755 /usr/lib/kizuna-eye 2>/dev/null || true
        $SUDO install -m 0755 -o root -g root "$ACTION_HELPER_SRC" /usr/lib/kizuna-eye/kizuna-action.sh 2>/dev/null || true
        $SUDO install -m 0755 -o root -g root "$ACTION_SERVER_SRC" /usr/lib/kizuna-eye/kizuna-action-server 2>/dev/null || true
        $SUDO install -m 0644 -o root -g root systemd/kizuna-action.socket /lib/systemd/system/kizuna-action.socket 2>/dev/null || true
        $SUDO install -m 0644 -o root -g root systemd/kizuna-action.service /lib/systemd/system/kizuna-action.service 2>/dev/null || true
        if [ -f systemd/kizuna-eye-tmpfiles.conf ]; then
            $SUDO install -m 0644 -o root -g root systemd/kizuna-eye-tmpfiles.conf /usr/lib/tmpfiles.d/kizuna-eye.conf 2>/dev/null || true
            $SUDO systemd-tmpfiles --create /usr/lib/tmpfiles.d/kizuna-eye.conf 2>/dev/null || true
        fi
        $SUDO rm -f /etc/sudoers.d/kizuna-security-action /usr/local/bin/kizuna-action.sh 2>/dev/null || true
        $SUDO systemctl daemon-reload 2>/dev/null || true
        $SUDO systemctl enable --now kizuna-action.socket 2>/dev/null || true
        ok "ワンクリック対処サービス（特権ソケット）を配備しました"
    fi
fi

# PLUGINS_REBUILT は実際に再ビルドできたプラグイン .so の数を数える。
# 共有パッケージが変わったのにプラグインが1つも再ビルドできなかった場合、
# 再起動を拒否するために使う（配備済み .so はロードに失敗するため）。
# PLUGINS_REBUILT counts how many plugin .so files were actually rebuilt.
# It is used below to refuse a restart when a shared package changed but no
# plugin could be rebuilt (the deployed .so would then fail to load).
PLUGINS_REBUILT=0
build_plugin() {
    local dir="$1" name="$2"
    [ -d "$dir" ] || { log "ℹ️  プラグインソース無し: $dir"; return 0; }
    grep -rqsE '^package main' "$dir"/*.go || { warn "$name: main パッケージでないためスキップ"; return 0; }
    log "🔌 ビルド: $name"
    # ホストと同じ combined go.work でビルドする（ABI 一致）。WORKFILE が無い
    # 場合（プラグインソース未設定）はプラグイン自身のモジュールへフォールバック。
    if ( cd "$dir" && GOWORK=${WORKFILE:-off} GOTOOLCHAIN=auto CGO_ENABLED=1 go build -trimpath -buildvcs=false -buildmode=plugin -o "$PLUGIN_OUT_DIR/${name}.so" . ); then
        PLUGINS_REBUILT=$((PLUGINS_REBUILT + 1))
        return 0
    fi
    return 1
}
# ホストを再ビルドしたら、プラグインも必ず同じツールチェーン／依存で
# 再ビルドする。Go プラグインは共有パッケージのハッシュ完全一致が必須で、
# ツールチェーンや x/sys が 1 つでもずれるとロードに失敗する
# （plugin was built with a different version of package ...）。
# NEED_PLUGIN_REBUILD は「pkg/module|pkg/status が変わった」場合の目印だが、
# それ以外でもホスト再ビルドでハッシュが変わり得るため常に再ビルドする。
if [ "$BUILD_PLUGINS" -eq 1 ]; then
    log ""
    log "▶ プラグインを再ビルド中（ホストとハッシュを揃えるため常に実施）..."
    if [ "$NEED_PLUGIN_REBUILD" -eq 1 ]; then
        log "ℹ️  pkg/module または pkg/status の変更を検出済み"
    fi
    if ! build_plugin "$LITE_PLUGIN_DIR" "kizuna_backup_lite"; then rollback; err "LITE プラグインビルド失敗。ロールバックしました。"; exit 1; fi
    if ! build_plugin "$SEC_PLUGIN_DIR" "kizuna_security"; then rollback; err "Security プラグインビルド失敗。ロールバックしました。"; exit 1; fi

    # fail-closed: 共有パッケージが変わったのに 1 つもプラグインを再ビルド
    # できなかった場合、このまま再起動すると配置済み .so は ABI 不一致で
    # ロードできず、全プラグインの監視が静かに停止する。ソースが手元に無い
    # （KIZUNA_*_PLUGIN_DIR 未設定）等の理由でこの状態になったら、更新を
    # 中止してロールバックする。
    if [ "$NEED_PLUGIN_REBUILD" -eq 1 ] && [ "$PLUGINS_REBUILT" -eq 0 ]; then
        if ls "$PLUGIN_OUT_DIR"/*.so >/dev/null 2>&1; then
            rollback
            err "共有パッケージが変更されましたがプラグインを再ビルドできませんでした（KIZUNA_SEC_PLUGIN_DIR / KIZUNA_LITE_PLUGIN_DIR を設定してください）。ABI 不一致で全プラグインがロード不能になるため更新を中止しました。"
            exit 1
        fi
    fi

    # 再ビルドした .so は内容が変わるため、既存の .sig は無効になる。
    # require_signature=true の環境では署名不一致でプラグインがロードされず
    # fail-closed で静かに止まるので、ここで必ず再署名する。
    if [ -x "$SIGN_BIN" ] && [ -r "$SIGNING_KEY" ]; then
        log "▶ プラグインに再署名中..."
        if ! "$SIGN_BIN" -sign-all "$PLUGIN_OUT_DIR" -private-key "$SIGNING_KEY" -force; then
            rollback; err "プラグイン署名失敗。ロールバックしました。"; exit 1
        fi
        ok "プラグイン再署名完了"
    else
        warn "plugin-sign または署名鍵が見つからないため、プラグイン署名をスキップしました。"
        warn "  plugin-sign: $SIGN_BIN"
        warn "  署名鍵:      $SIGNING_KEY (KIZUNA_SIGNING_KEY で上書き可)"
        warn "  require_signature=true の場合、agent がプラグインをロードできない可能性があります。"
    fi
fi

# ---- 6. 再起動 & 生存確認 ----
# 運用モードに応じて再起動方法を切り替える:
#   - systemd 管理下 → systemctl restart <検出した unit> (kizuna-eye-agent 等)
#   - 手動管理       → stop.sh → start.sh
if [ "$DO_RESTART" -eq 1 ]; then
    log ""
    USE_SYSTEMD=0
    if systemd_active; then USE_SYSTEMD=1; fi

    restart_manual() { ./stop.sh || true; sleep 1; ./start.sh; }
    restart_systemd() {
        local u
        for u in $SYSTEMD_UNITS; do
            $SUDO systemctl restart "$u"
        done
    }
    rollback_restart() {
        rollback
        if [ "$USE_SYSTEMD" -eq 1 ]; then restart_systemd || true; else ./stop.sh || true; ./start.sh || true; fi
    }

    if [ "$USE_SYSTEMD" -eq 1 ]; then
        log "▶ systemd 管理下 ($SYSTEMD_UNITS) を検知。systemctl restart で再起動します..."
        if ! restart_systemd; then
            rollback_restart
            err "systemd 再起動に失敗。ロールバックして再起動しました。"; exit 1
        fi
        sleep 3
        if ! systemd_active; then
            rollback_restart
            err "再起動後 $SYSTEMD_UNITS が active ではありません。ロールバックしました。"; exit 1
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
NEW_VERSION="$(git describe --tags --always 2>/dev/null || echo "$KVERSION")"
log ""
log "============================================================"
ok "アップデート成功"
log "  バージョン: ${PREV_VERSION} → ${NEW_VERSION}"
log "  バックアップ: $ROLLBACK_DIR"
log "============================================================"
