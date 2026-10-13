#!/bin/bash
# ============================================================
# Kizuna-Eye install.sh
# 初回セットアップ: パッケージ導入 → ビルド → 初回起動
#
#   1. Ubuntu 判定・sudo 権限チェック
#   2. 監視に必要なパッケージを「足りないものだけ」導入
#      (smartmontools rsync git curl build-essential ca-certificates bubblewrap)
#   3. Go 1.27.1 を確認（無ければ公式導入を案内）
#   4. ディレクトリ準備（/opt/kizuna-eye/bin{,/plugins}, logs, plugins）
#   5. 本体をビルド（plugin-inspect → dashboard → agent → plugin-sign）
#   6. プラグイン(.so)をビルド＋署名（ソースがあれば / 純正のみ）
#   7. smartctl / cron / ワンクリック対処の sudoers を導入
#   8. dashboard と agent を systemd unit として登録（--no-systemd でスキップ）
#      agent は専用ユーザー kizuna-eye で動かす（H-1 権限分離）
#   9. systemd 未使用時のみ start.sh を自動実行
#
# 使い方:
#   ./install.sh              # 全部やる（systemd 登録込み）
#   ./install.sh --no-install # パッケージ導入をスキップ
#   ./install.sh --no-build   # ビルドをスキップ
#   ./install.sh --no-start   # 起動をスキップ
#   ./install.sh --no-systemd # systemd 登録をスキップ（start.sh で手動管理）
#   ./install.sh --force-start # 2回目以降でも起動する
# ============================================================
set -u
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
PLUGIN_OUT_DIR="${KIZUNA_PLUGIN_OUT_DIR:-/opt/kizuna-eye/bin/plugins}"
GO_REQUIRED="1.27.1"
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-/samba/share/Kizuna-Backup/Kizuna-Backup-LITE/plugin}"
SEC_PLUGIN_DIR="${KIZUNA_SEC_PLUGIN_DIR:-/samba/share/Kizuna-Security/plugin}"

DO_INSTALL=1
DO_BUILD=1
DO_START=1
FORCE_START=0
SETUP_SUDOERS=1
# systemd 登録は既定で行う（install.sh だけで初期セットアップを完結させる）。
# 手動管理（start.sh / stop.sh）にしたい場合のみ --no-systemd を付ける。
INSTALL_SYSTEMD=1
for arg in "$@"; do
    case "$arg" in
        --no-install) DO_INSTALL=0 ;;
        --no-build)   DO_BUILD=0 ;;
        --no-start)   DO_START=0 ;;
        --force-start) FORCE_START=1 ;;
        --no-sudoers) SETUP_SUDOERS=0 ;;
        --systemd)    INSTALL_SYSTEMD=1 ;;
        --no-systemd) INSTALL_SYSTEMD=0 ;;
        -h|--help) echo "使い方: $0 [--no-install] [--no-build] [--no-start] [--force-start] [--no-sudoers] [--no-systemd]"; exit 0 ;;
        *) echo "不明な引数: $arg"; exit 1 ;;
    esac
done

log()  { echo "$*"; }
warn() { echo "⚠️  $*"; }
err()  { echo "❌ $*"; }
ok()   { echo "✅ $*"; }

log "============================================================"
log " Kizuna-Eye セットアップ"
log "============================================================"

# ---- 1. 前提チェック ----
# os-release は「必要な値だけ」サブシェルで取り出す。`. /etc/os-release` を
# 親シェルで実行すると VERSION 等が上書きされ、後段の ldflags が壊れる
# （Ubuntu では VERSION="26.04.1 LTS ..." のような空白入りの値になる）。
if [ -r /etc/os-release ]; then
    OS_PRETTY="$( . /etc/os-release 2>/dev/null; printf '%s' "${PRETTY_NAME:-unknown}" )"
    OS_ID="$( . /etc/os-release 2>/dev/null; printf '%s' "${ID:-}" )"
    log "OS: ${OS_PRETTY}"
    case "$OS_ID" in
        ubuntu|debian) : ;;
        *) warn "Ubuntu/Debian 以外です（${OS_ID:-unknown}）。続行します。" ;;
    esac
else
    warn "/etc/os-release が読めません。続行します。"
fi

if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
    ok "root で実行中"
elif command -v sudo >/dev/null 2>&1; then
    if sudo -n true >/dev/null 2>&1; then
        SUDO="sudo"; ok "sudo (NOPASSWD) 利用可"
    else
        SUDO="sudo"; warn "sudo にパスワードが必要です。NOPASSWD を設定すると無人実行できます（任意）。"
    fi
else
    SUDO=""; warn "sudo がありません。パッケージ導入や /usr/local への Go 導入は手動になります。"
fi

# ---- 2. パッケージ導入（差分） ----
PM=""; INSTALL_CMD=""
if command -v apt-get >/dev/null 2>&1; then PM="apt"; INSTALL_CMD="apt-get install -y"
elif command -v dnf >/dev/null 2>&1; then PM="dnf"; INSTALL_CMD="dnf install -y"
elif command -v yum >/dev/null 2>&1; then PM="yum"; INSTALL_CMD="yum install -y"
elif command -v pacman >/dev/null 2>&1; then PM="pacman"; INSTALL_CMD="pacman -S --noconfirm"
fi

if [ "$PM" = "apt" ]; then
    # A-3: bubblewrap (bwrap) is the isolation wrapper the dashboard/agent use
    # to inspect an uploaded .so. Without it inspection fails closed
    # (plugins.inspect_isolation="bwrap"), so it is a real dependency rather
    # than an optional nicety.
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:build-essential" "bwrap:bubblewrap" )
    EXTRA_PKGS=( "ca-certificates" )
elif [ "$PM" = "pacman" ]; then
    # A-3: bubblewrap (bwrap) は .so 検査の隔離に必須（詳細は apt 側のコメント参照）。
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:base-devel" "bwrap:bubblewrap" )
    EXTRA_PKGS=()
else
    # A-3: bubblewrap (bwrap) は .so 検査の隔離に必須（詳細は apt 側のコメント参照）。
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:gcc" "bwrap:bubblewrap" )
    EXTRA_PKGS=( "ca-certificates" )
fi

log ""
log "▶ パッケージ確認 (PM=${PM:-none})"
# pkg_installed reports whether a package is already installed, using the
# detected package manager. Without this check EXTRA_PKGS (e.g.
# ca-certificates) were appended unconditionally, so `apt-get install` ran on
# every invocation even when nothing was missing.
pkg_installed() {
    local pkg="$1"
    case "$PM" in
        apt)    dpkg -s "$pkg" >/dev/null 2>&1 ;;
        dnf|yum) rpm -q "$pkg" >/dev/null 2>&1 ;;
        pacman) pacman -Q "$pkg" >/dev/null 2>&1 ;;
        *)      return 1 ;;
    esac
}

MISSING_PKGS=()
for entry in "${REQUIRED[@]}"; do
    cmd="${entry%%:*}"; pkg="${entry##*:}"
    if command -v "$cmd" >/dev/null 2>&1; then ok "$cmd"; else err "$cmd （未導入 → $pkg）"; MISSING_PKGS+=("$pkg"); fi
done
# Only add an extra package when it is not already installed. Guard the loop
# against an empty EXTRA_PKGS under `set -u`: `"${arr[@]}"` on an empty array
# is an unbound-variable error on bash < 4.4.
if [ "${#EXTRA_PKGS[@]}" -gt 0 ]; then
    for pkg in "${EXTRA_PKGS[@]}"; do
        if pkg_installed "$pkg"; then ok "$pkg"; else err "$pkg （未導入）"; MISSING_PKGS+=("$pkg"); fi
    done
fi

if [ ${#MISSING_PKGS[@]} -gt 0 ]; then
    if [ -z "$PM" ]; then
        warn "パッケージマネージャ未検出。手動で導入してください: ${MISSING_PKGS[*]}"
    elif [ "$DO_INSTALL" -eq 1 ]; then
        log "▶ 不足分をインストール: $SUDO $INSTALL_CMD ${MISSING_PKGS[*]}"
        if ! $SUDO $INSTALL_CMD "${MISSING_PKGS[@]}"; then err "パッケージ導入に失敗しました。"; exit 1; fi
        ok "パッケージ導入完了"
    else
        warn "不足分があります（--no-install のためスキップ）: ${MISSING_PKGS[*]}"
    fi
else
    ok "必要なパッケージは揃っています"
fi

# ---- 3. Go 1.27.1 ----
log ""
log "▶ Go の確認（必要: ${GO_REQUIRED}）"
GO_BIN="$(command -v go || true)"
if [ -n "$GO_BIN" ]; then
    GOVER="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
    log "検出: go ${GOVER} (${GO_BIN})"
    if [ "$(printf '%s\n%s\n' "$GO_REQUIRED" "$GOVER" | sort -V | head -n1)" != "$GO_REQUIRED" ]; then
        warn "go ${GOVER} は ${GO_REQUIRED} 未満。GOTOOLCHAIN=auto で ${GO_REQUIRED} を自動取得します（初回ネット必須）。"
    else
        ok "Go バージョン OK"
    fi
else
    err "go が見つかりません。Go ${GO_REQUIRED}+ を導入してください: https://go.dev/dl/"
    warn "例: curl -LO https://go.dev/dl/go${GO_REQUIRED}.linux-amd64.tar.gz && sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go*.tar.gz"
    DO_BUILD=0
fi

# ---- 4. ディレクトリ ----
log ""
log "▶ ディレクトリ準備"
mkdir -p "$BIN_DIR" "$PLUGIN_OUT_DIR" logs plugins || { err "ディレクトリ作成に失敗"; exit 1; }
RUN_DIR="${KIZUNA_RUN_DIR:-/opt/kizuna-eye/run}"
mkdir -p "$RUN_DIR" 2>/dev/null || true
TARGET_UID="${SUDO_UID:-$(id -u)}"; TARGET_GID="${SUDO_GID:-$(id -g)}"
if [ "$(id -u)" -eq 0 ]; then chown -R "$TARGET_UID:$TARGET_GID" "$BIN_DIR" "$RUN_DIR" logs plugins 2>/dev/null || true; fi
ok "$BIN_DIR / $PLUGIN_OUT_DIR / logs / plugins"

# ---- 5. 本体ビルド ----
if [ "$DO_BUILD" -eq 1 ]; then
    # Prefer the repo VERSION file (single source of truth), then the git tag.
    # 変数名は KVERSION にして、環境変数 VERSION（os-release 等）との衝突を避ける。
    KVERSION="${KIZUNA_VERSION:-$(cat VERSION 2>/dev/null || git describe --tags --always 2>/dev/null || echo v0.7.1)}"
    BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    log ""
    log "▶ 本体をビルド中... (version=${KVERSION})"
    LDFLAGS="-X Kizuna-Eye/internal/api.Version=${KVERSION} -X Kizuna-Eye/internal/api.BuildTime=${BUILD_TIME}"
    export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
    build_one() {
        local out="$1" pkg="$2"
        if CGO_ENABLED=1 go build -buildvcs=false -ldflags "$LDFLAGS" -o "$BIN_DIR/$out" "$pkg"; then ok "build $out"; else err "build 失敗: $out"; exit 1; fi
    }
    build_one plugin-inspect  ./cmd/plugin-inspect
    build_one dashboard_linux ./cmd/dashboard
    build_one agent_linux     ./cmd/agent
    # A-3: plugin-sign は .so の分離署名（Ed25519）を作る運用ツール。
    build_one plugin-sign     ./cmd/plugin-sign
fi

# ---- 6. プラグイン ----
build_plugin() {
    local dir="$1" name="$2"
    [ -d "$dir" ] || { log "ℹ️  プラグインソース無し: $dir"; return 0; }
    if ! grep -rqsE '^package main' "$dir"/*.go; then
        warn "$dir は main パッケージではないためスキップ"; return 0
    fi
    local host_sys plugin_sys
    host_sys="$(grep -E '^[[:space:]]*golang.org/x/sys ' go.mod | awk '{print $2}')"
    plugin_sys="$(grep -E '^[[:space:]]*golang.org/x/sys ' "$dir/go.mod" 2>/dev/null | awk '{print $2}')"
    if [ -n "$host_sys" ] && [ -n "$plugin_sys" ] && [ "$host_sys" != "$plugin_sys" ]; then
        warn "x/sys 不一致: 本体=$host_sys / $name=$plugin_sys（ロード失敗の原因）"
    fi
    log "🔌 ビルド: $dir → $PLUGIN_OUT_DIR/${name}.so"
    if ! ( cd "$dir" && GOWORK=off GOTOOLCHAIN=auto CGO_ENABLED=1 go build -buildvcs=false -buildmode=plugin -o "$PLUGIN_OUT_DIR/${name}.so" . ); then
        err "プラグインビルド失敗: $dir"; return 1
    fi
    ok "plugin ${name}.so"
}
if [ "$DO_BUILD" -eq 1 ]; then
    log ""
    log "▶ プラグインをビルド中..."
    build_plugin "$LITE_PLUGIN_DIR" "kizuna_backup_lite" || true
    build_plugin "$SEC_PLUGIN_DIR"  "kizuna_security"    || true
fi

# ---- 6b. プラグイン署名（純正プラグインのみ） ----
# require_signature=true の環境では、署名の無い .so はロードされない
# （fail-closed）。初回追加時に純正プラグインへ署名しておくことで、
# 「ビルドしたのに .sig を忘れてプラグインが動かない」事故を防ぐ。
# 再ビルド時の再署名は update.sh が担当する（install.sh では初回に鍵生成 + 既存 .so へ署名。
# Ed25519 署名は決定的なので -force で再署名しても同じ .so なら同一の .sig になる）。
# 署名鍵は「実行ユーザー（SUDO_USER）」のホームに置く。sudo 実行時に
# $HOME をそのまま使うと /root 配下になり、後段の migrate-agent-user.sh が
# 公開鍵を見つけられない。
RUN_HOME="$(getent passwd "${SUDO_USER:-$(id -un)}" | cut -d: -f6)"
[ -n "$RUN_HOME" ] || RUN_HOME="$HOME"
SIGN_BIN="$BIN_DIR/plugin-sign"
SIGN_KEY_DIR="$RUN_HOME/.kizuna-eye/keys/plugin_signing"
SIGN_KEY="$SIGN_KEY_DIR/plugin_signing.key"
if [ "$DO_BUILD" -eq 1 ] && [ -x "$SIGN_BIN" ]; then
    log ""
    log "▶ プラグイン署名（純正）"
    if [ ! -f "$SIGN_KEY" ]; then
        log "   署名鍵が無いため生成します: $SIGN_KEY_DIR"
        if "$SIGN_BIN" -gen-key -key-dir "$SIGN_KEY_DIR"; then
            ok "署名鍵を生成しました（秘密鍵はオフホスト保管を推奨）"
        else
            warn "署名鍵の生成に失敗しました。署名をスキップします。"
        fi
    else
        log "   署名鍵: $SIGN_KEY"
    fi
    if [ -f "$SIGN_KEY" ]; then
        if "$SIGN_BIN" -sign-all "$PLUGIN_OUT_DIR" -private-key "$SIGN_KEY" -force; then
            ok "純正プラグインに署名しました: $PLUGIN_OUT_DIR"
        else
            warn "プラグイン署名に失敗しました（.so が無い場合は無視して構いません）。"
        fi
    fi
fi

# ---- 7. smartctl 用 sudoers（最小権限） ----
# agent を非 root で動かしつつ S.M.A.R.T を取得するため、smartctl だけに
# NOPASSWD を与える。agent 側は `sudo -n smartctl` を試し、失敗時は素の
# smartctl にフォールバックする（pkg/module/system.go）。
if [ "$SETUP_SUDOERS" -eq 1 ]; then
    log ""
    log "▶ smartctl の sudoers 設定"
    if [ "$(id -u)" -eq 0 ] || [ -n "$SUDO" ]; then
        if command -v smartctl >/dev/null 2>&1; then
            SMARTCTL_PATH="$(command -v smartctl)"
        else
            SMARTCTL_PATH=""
        fi
        RUN_USER="${SUDO_USER:-$(id -un)}"
        SUDOERS_FILE="/etc/sudoers.d/kizuna-smartctl"
        if [ -n "$SMARTCTL_PATH" ] && [ -d /etc/sudoers.d ]; then
            TMP="$(mktemp)"
            printf '%s ALL=(root) NOPASSWD: %s\n' "$RUN_USER" "$SMARTCTL_PATH" > "$TMP"
            if $SUDO install -m 0440 -o root -g root "$TMP" "$SUDOERS_FILE" 2>/dev/null \
               && $SUDO visudo -cf "$SUDOERS_FILE" >/dev/null 2>&1; then
                ok "sudoers 設定: $SUDOERS_FILE (user=$RUN_USER, $SMARTCTL_PATH)"
            else
                warn "sudoers 設定に失敗しました（visudo 検証 or 権限）。手動で設定してください:"
                warn "  echo '$RUN_USER ALL=(root) NOPASSWD: $SMARTCTL_PATH' | sudo tee $SUDOERS_FILE"
            fi
            rm -f "$TMP"
        elif [ -z "$SMARTCTL_PATH" ]; then
            warn "smartctl 未導入のため sudoers 設定をスキップします（smartmontools 導入後に ./install.sh を再実行してください）。"
        else
            warn "/etc/sudoers.d がありません。手動で設定してください。"
        fi
    else
        warn "sudo が使えないため sudoers 設定をスキップします。S.M.A.R.T には権限が必要です。"
    fi
fi

# ---- 7b. cron 読み取りヘルパーの導入（Medium-9） ----
# cron の変更検知は root しか読めない /var/spool/cron/crontabs を読む必要が
# ある。プラグインは `sudo -n /usr/local/bin/kizuna-cron-read.sh` を実行し、
# sudoers はその 1 コマンドだけに NOPASSWD を与える（引数なしのみ許可）。
# 以前はこの導入が install.sh に無く、再構築すると手動配置に依存して静かに
# cron 検知が縮退していた。
CRON_HELPER_SRC="scripts/kizuna-cron-read.sh"
CRON_HELPER_DST="/usr/local/bin/kizuna-cron-read.sh"
if [ "$SETUP_SUDOERS" -eq 1 ]; then
    log ""
    log "▶ cron 読み取りヘルパーの導入"
    CRON_USER="${SUDO_USER:-$(id -un)}"
    CRON_SUDOERS="/etc/sudoers.d/kizuna-security-cron"
    if [ -z "$SUDO" ] && [ "$(id -u)" -ne 0 ]; then
        warn "sudo が使えないため cron ヘルパーの導入をスキップします。手動で:"
        warn "  sudo install -m 0755 -o root -g root $CRON_HELPER_SRC $CRON_HELPER_DST"
        warn "  echo '$CRON_USER ALL=(root) NOPASSWD: $CRON_HELPER_DST \"\"' | sudo tee $CRON_SUDOERS"
    elif [ ! -f "$CRON_HELPER_SRC" ]; then
        warn "cron ヘルパーが見つかりません: $CRON_HELPER_SRC"
    elif [ ! -d /etc/sudoers.d ]; then
        warn "/etc/sudoers.d がありません。手動で cron 用 sudoers を設定してください。"
    else
        if $SUDO install -m 0755 -o root -g root "$CRON_HELPER_SRC" "$CRON_HELPER_DST"; then
            ok "cron ヘルパー: $CRON_HELPER_DST"
        else
            warn "cron ヘルパーの設置に失敗しました: $CRON_HELPER_DST"
        fi
        CRON_TMP="$(mktemp)"
        # 末尾の "" は「引数なしの実行だけを許可する」指定。引数付きの実行を
        # 許すと、sudo 経由で任意パスの読み取りに使われ得る。
        printf '%s ALL=(root) NOPASSWD: %s ""\n' "$CRON_USER" "$CRON_HELPER_DST" > "$CRON_TMP"
        if $SUDO install -m 0440 -o root -g root "$CRON_TMP" "$CRON_SUDOERS" 2>/dev/null \
           && $SUDO visudo -cf "$CRON_SUDOERS" >/dev/null 2>&1; then
            ok "sudoers 設定: $CRON_SUDOERS (user=$CRON_USER, 引数なしのみ)"
        else
            warn "sudoers 設定に失敗しました（visudo 検証 or 権限）。手動で設定してください:"
            warn "  echo '$CRON_USER ALL=(root) NOPASSWD: $CRON_HELPER_DST \"\"' | sudo tee $CRON_SUDOERS"
            warn "  sudo chmod 0440 $CRON_SUDOERS"
        fi
    fi
fi

# ---- 7c. ワンクリック対処ヘルパーの導入（タスク2） ----
# ダッシュボードは user 権限で動くため、kill / ファイアウォール操作 /
# cron 削除を直接は実行できない。kizuna-action.sh はアクション名を
# 列挙型に固定して引数を厳格に検証するので、sudoers は「このスクリプトを
# 引数付きで実行すること」だけを許可すればよい（引数の検証はスクリプト内）。
ACTION_HELPER_SRC="scripts/kizuna-action.sh"
ACTION_HELPER_DST="/usr/local/bin/kizuna-action.sh"
if [ "$SETUP_SUDOERS" -eq 1 ]; then
    log ""
    log "▶ ワンクリック対処ヘルパーの導入"
    ACTION_USER="${SUDO_USER:-$(id -un)}"
    ACTION_SUDOERS="/etc/sudoers.d/kizuna-security-action"
    if [ -z "$SUDO" ] && [ "$(id -u)" -ne 0 ]; then
        warn "sudo が使えないため対処ヘルパーの導入をスキップします。手動で:"
        warn "  sudo install -m 0755 -o root -g root $ACTION_HELPER_SRC $ACTION_HELPER_DST"
        warn "  echo '$ACTION_USER ALL=(root) NOPASSWD: $ACTION_HELPER_DST' | sudo tee $ACTION_SUDOERS"
    elif [ ! -f "$ACTION_HELPER_SRC" ]; then
        warn "対処ヘルパーが見つかりません: $ACTION_HELPER_SRC"
    elif [ ! -d /etc/sudoers.d ]; then
        warn "/etc/sudoers.d がありません。手動で対処用 sudoers を設定してください。"
    else
        if $SUDO install -m 0755 -o root -g root "$ACTION_HELPER_SRC" "$ACTION_HELPER_DST"; then
            ok "対処ヘルパー: $ACTION_HELPER_DST"
        else
            warn "対処ヘルパーの設置に失敗しました: $ACTION_HELPER_DST"
        fi
        ACTION_TMP="$(mktemp)"
        # 引数付きでも許可する（検証はスクリプト内）。cron ヘルパーの "" と
        # 異なり、こちらは引数が必須のため引数固定はできない。
        printf '%s ALL=(root) NOPASSWD: %s\n' "$ACTION_USER" "$ACTION_HELPER_DST" > "$ACTION_TMP"
        if $SUDO install -m 0440 -o root -g root "$ACTION_TMP" "$ACTION_SUDOERS" 2>/dev/null \
           && $SUDO visudo -cf "$ACTION_SUDOERS" >/dev/null 2>&1; then
            ok "sudoers 設定: $ACTION_SUDOERS (user=$ACTION_USER)"
        else
            warn "対処用 sudoers の設定に失敗しました（visudo 検証 or 権限）。手動で設定してください:"
            warn "  echo '$ACTION_USER ALL=(root) NOPASSWD: $ACTION_HELPER_DST' | sudo tee $ACTION_SUDOERS"
            warn "  sudo chmod 0440 $ACTION_SUDOERS"
        fi
        rm -f "$CRON_TMP"
    fi
fi

# ---- 8. systemd 登録（dashboard + agent / 既定で実施） ----
# install.sh だけで初期セットアップが完了するよう、dashboard と agent を
# それぞれ systemd unit として登録する。systemd を使わず手動管理
# （start.sh / stop.sh）にしたい場合は --no-systemd を付ける。
#
#   - agent    : 専用ユーザー kizuna-eye で動かす（H-1 権限分離）。
#                systemd/migrate-agent-user.sh がユーザー作成・鍵/状態の
#                引っ越し・権限調整・unit 導入・起動までを一括で行う。
#   - dashboard: 実行ユーザーのまま kizuna-dashboard.service で管理する。
#
# 旧 kizuna-eye.service（start.sh を呼ぶ system service）や旧
# kizuna-agent.service は二重起動防止のため disable する。
if [ "$INSTALL_SYSTEMD" -eq 1 ]; then
    log ""
    log "▶ systemd 登録（dashboard + agent）"
    RUN_USER="${SUDO_USER:-$(id -un)}"
    RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
    [ -n "$RUN_HOME" ] || RUN_HOME="$HOME"
    # dashboard の設定・ログの基準ディレクトリ（H-1 後は ~/.kizuna-eye/data）。
    if [ -f "$RUN_HOME/.kizuna-eye/data/dashboard_config.json" ]; then
        DATA_DIR="$RUN_HOME/.kizuna-eye/data"
    else
        DATA_DIR="$PWD"
    fi

    if [ -z "$SUDO" ] && [ "$(id -u)" -ne 0 ]; then
        warn "sudo が使えないため systemd 登録をスキップします。手動で: sudo systemd/migrate-agent-user.sh $RUN_USER の後、dashboard unit を導入してください。"
    else
        # 1) 旧 unit を disable（二重起動防止）
        for old in kizuna-eye kizuna-agent; do
            if $SUDO systemctl is-enabled --quiet "$old" 2>/dev/null; then
                warn "旧 unit $old を disable（二重起動防止）"
                $SUDO systemctl disable --now "$old" || true
            fi
        done

        # 2) agent を H-1（専用ユーザー）で移行。migrate-agent-user.sh は
        #    手動プロセス（dashboard 含む）を停止するため、dashboard unit の
        #    登録・起動より先に実行する。
        MIGRATE_SCRIPT="systemd/migrate-agent-user.sh"
        if [ ! -f "$MIGRATE_SCRIPT" ]; then
            warn "$MIGRATE_SCRIPT が無いため agent の H-1 移行をスキップします。"
        elif $SUDO "$MIGRATE_SCRIPT" "$RUN_USER"; then
            ok "agent を専用ユーザー (kizuna-eye) で systemd 管理下に移行しました"
        else
            warn "H-1 移行に失敗しました。手動で再実行してください: sudo $MIGRATE_SCRIPT $RUN_USER"
        fi

        # 3) dashboard unit を導入（__USER__/__DIR__/__BIN__/__DATA__ を置換）
        DASH_SRC="systemd/kizuna-dashboard.service"
        DASH_DST="/etc/systemd/system/kizuna-dashboard.service"
        if [ -f "$DASH_SRC" ]; then
            sed -e "s|__USER__|$RUN_USER|g" \
                -e "s|__DIR__|$PWD|g" \
                -e "s|__BIN__|$BIN_DIR|g" \
                -e "s|__DATA__|$DATA_DIR|g" \
                "$DASH_SRC" | $SUDO tee "$DASH_DST" >/dev/null
            chmod 0644 "$DASH_DST" 2>/dev/null || true
            ok "dashboard unit 導入: $DASH_DST"
        else
            warn "$DASH_SRC が無いため dashboard unit をスキップします。"
        fi

        # 4) dashboard を systemd で起動（agent は H-1 で起動済み）
        $SUDO systemctl daemon-reload
        if $SUDO systemctl enable --now kizuna-dashboard; then
            ok "dashboard を systemd で起動しました"
            DO_START=0   # systemd 管理になったため start.sh は使わない
        else
            warn "dashboard の systemd 起動に失敗しました。手動で: sudo systemctl enable --now kizuna-dashboard"
        fi
    fi
fi

# ---- 9. 初回起動（systemd 未導入時のみ） ----
MARKER="${KIZUNA_INSTALL_MARKER:-$HOME/.kizuna-eye-installed}"
if [ "$DO_START" -eq 1 ]; then
    log ""
    if [ -f "$MARKER" ] && [ "$FORCE_START" -eq 0 ]; then
        log "ℹ️  2回目以降の実行です。起動する場合は ./start.sh を実行してください。"
    else
        if pgrep -f "/opt/kizuna-eye/bin/dashboard_linux" >/dev/null 2>&1 || pgrep -f "/opt/kizuna-eye/bin/agent_linux" >/dev/null 2>&1; then
            warn "既に動作中です。./stop.sh で停止してから起動します。"
            ./stop.sh || true; sleep 1
        fi
        if [ -x ./start.sh ]; then
            log "▶ 起動します..."
            ./start.sh || { err "起動に失敗しました"; exit 1; }
            mkdir -p "$(dirname "$MARKER")" 2>/dev/null || true
            touch "$MARKER" 2>/dev/null || true
        else
            warn "start.sh が見つかりません。手動で起動してください。"
        fi
    fi
else
    log "ℹ️  起動をスキップしました（--no-start）"
fi

# ---- 10. 完了 ----
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
log ""
log "============================================================"
ok "セットアップ完了"
log "  アクセス: http://${LAN_IP:-<server-ip>}:8080"
log "  ログ    : logs/dashboard.log, logs/agent.log"
log "  更新    : ./update.sh  （GitHub から取得→再ビルド→再起動）"
log "  停止    : ./stop.sh  （H-1 導入時: dashboard は ./stop.sh、agent は sudo systemctl stop kizuna-eye-agent）"
log "============================================================"
