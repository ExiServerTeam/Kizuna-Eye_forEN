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
# プラグインのソース位置。Security は本リポジトリ同梱なのでリポジトリ内を指す。
# Kizuna-Backup LITE は別リポジトリのため既定では無効（使う場合のみ
# KIZUNA_LITE_PLUGIN_DIR で場所を指定する）。
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-}"
SEC_PLUGIN_DIR="${KIZUNA_SEC_PLUGIN_DIR:-$PWD/plugins/Kizuna-Security/plugin}"

DO_INSTALL=1
DO_BUILD=1
DO_START=1
FORCE_START=0
SETUP_SUDOERS=1
# UI の既定言語（ja | en）。--lang / KIZUNA_LANG で指定、未指定は en。
# 新規作成した dashboard_config.json の "language" に書き込む。
# LANG_SET=1 は「明示指定あり」。対話プロンプトを出すかどうかの判定に使う。
UI_LANG="${KIZUNA_LANG:-en}"
LANG_SET=0
[ -n "${KIZUNA_LANG:-}" ] && LANG_SET=1
# systemd 登録は既定で行う（install.sh だけで初期セットアップを完結させる）。
# 手動管理（start.sh / stop.sh）にしたい場合のみ --no-systemd を付ける。
INSTALL_SYSTEMD=1
# 起動をスキップした理由（ログ表示用）。--no-start と systemd 起動済みを区別する。
SKIP_START_REASON=""
# while ループで解析する（for ループだと --lang ja のように値を別引数で
# 取るオプションで shift が効かず、'ja' が不明な引数になって失敗する）。
while [ $# -gt 0 ]; do
    case "$1" in
        --no-install) DO_INSTALL=0 ;;
        --no-build)   DO_BUILD=0 ;;
        --no-start)   DO_START=0 ;;
        --force-start) FORCE_START=1 ;;
        --no-sudoers) SETUP_SUDOERS=0 ;;
        --systemd)    INSTALL_SYSTEMD=1 ;;
        --no-systemd) INSTALL_SYSTEMD=0 ;;
        --lang) shift; UI_LANG="${1:-en}"; LANG_SET=1 ;;
        --lang=*) UI_LANG="${1#--lang=}"; LANG_SET=1 ;;
        -h|--help) echo "使い方: $0 [--no-install] [--no-build] [--no-start] [--force-start] [--no-sudoers] [--no-systemd] [--lang ja|en]"; exit 0 ;;
        *) echo "不明な引数: $1"; exit 1 ;;
    esac
    shift
done
# 未知の言語は en にフォールバック（入力が無い/不正なら規定 EN）。
case "$UI_LANG" in
    ja|en) ;;
    *) warn "言語 '$UI_LANG' は未対応のため en を使用します（対応: ja / en）"; UI_LANG="en" ;;
esac
# 子プロセス（migrate-agent-user.sh）が表示言語を参照できるよう引き継ぐ。
export UI_LANG

# 表示言語。UI_LANG=ja 以外（en）は英語で出す。日本語の原文をキーに英語へ訳し、
# 訳が無いメッセージは日本語のまま出す（安全側）。
tr_msg() {
    local m="$1"
    [ "$UI_LANG" = "en" ] || { printf '%s' "$m"; return 0; }
    case "$m" in
        # --- 前提 ---
        "root で実行中") m="Running as root" ;;
        "sudo (NOPASSWD) 利用可") m="sudo (NOPASSWD) available" ;;
        "sudo にパスワードが必要です。NOPASSWD を設定すると無人実行できます（任意）。")
            m="sudo needs a password. Set NOPASSWD for unattended runs (optional)." ;;
        "sudo がありません。パッケージ導入や /usr/local への Go 導入は手動になります。")
            m="sudo not found. Install packages and Go under /usr/local manually." ;;
        "/etc/os-release が読めません。続行します。") m="Cannot read /etc/os-release. Continuing." ;;
        "Ubuntu/Debian 以外です（"*)
            m="Not Ubuntu/Debian (${m#Ubuntu/Debian 以外です（}"; m="${m%）。続行します。}). Continuing." ;;
        "言語 '"*"' は未対応のため en を使用します（対応: ja / en）")
            m="Unsupported language; using en (supported: ja / en)." ;;
        "不明な選択 '"*"' のため English (en) を使用します")
            m="Unknown choice; using English (en)." ;;
        "   言語: "*) m="   Language: ${m#   言語: }" ;;
        # --- パッケージ ---
        "パッケージ確認 (PM="*) m="Package check (PM=${m#パッケージ確認 (PM=}" ;;
        *" （未導入 → "*) m="${m% （未導入 → *} (not installed)" ;;
        *" （未導入）") m="${m% （未導入）} (not installed)" ;;
        "パッケージマネージャ未検出。手動で導入してください: "*)
            m="No package manager detected. Install manually: ${m#パッケージマネージャ未検出。手動で導入してください: }" ;;
        "不足分をインストール: "*) m="Installing missing packages: ${m#不足分をインストール: }" ;;
        "パッケージ導入に失敗しました。") m="Package installation failed." ;;
        "パッケージ導入完了") m="Packages installed" ;;
        "不足分があります（--no-install のためスキップ）: "*)
            m="Missing packages (skipped, --no-install): ${m#不足分があります（--no-install のためスキップ）: }" ;;
        "必要なパッケージは揃っています") m="All required packages are present" ;;
        # --- Go ---
        "Go の確認（必要: "*"）") m="Go check (required: ${m#Go の確認（必要: })" ;;
        "検出: "*) m="Detected: ${m#検出: }" ;;
        "Go バージョン OK") m="Go version OK" ;;
        "go が見つかりません。Go "*) m="go not found. Install Go: ${m#go が見つかりません。Go }" ;;
        "例: curl -LO "*) m="Example: curl -LO ${m#例: curl -LO }" ;;
        "go "*" は "*" 未満。GOTOOLCHAIN=auto で "*" を自動取得します（初回ネット必須）。")
            m="Go is older than required; GOTOOLCHAIN=auto will fetch it (network needed on first run)." ;;
        # --- ディレクトリ / ビルド ---
        "ディレクトリ準備") m="Preparing directories" ;;
        "ディレクトリ作成に失敗") m="Failed to create directories" ;;
        "本体をビルド中... (version="*) m="Building main binaries... (version=${m#本体をビルド中... (version=}" ;;
        "build 失敗: "*) m="build failed: ${m#build 失敗: }" ;;
        "プラグインをビルド中...") m="Building plugins..." ;;
        "プラグインソース無し: "*) m="No plugin source: ${m#プラグインソース無し: }" ;;
        *" は main パッケージではないためスキップ") m="${m% は main パッケージではないためスキップ} is not a main package; skipping" ;;
        "x/sys 不一致: "*) m="x/sys mismatch: ${m#x/sys 不一致: }" ;;
        "🔌 ビルド: "*) m="🔌 Building: ${m#🔌 ビルド: }" ;;
        "プラグインビルド失敗: "*) m="Plugin build failed: ${m#プラグインビルド失敗: }" ;;
        # --- 署名 ---
        "プラグイン署名（純正）") m="Signing plugins (official)" ;;
        "   署名鍵が無いため生成します: "*) m="   No signing key; generating: ${m#   署名鍵が無いため生成します: }" ;;
        "署名鍵を生成しました（秘密鍵はオフホスト保管を推奨）") m="Signing key generated (store the private key off-host)" ;;
        "署名鍵の生成に失敗しました。署名をスキップします。") m="Failed to generate signing key; skipping signing." ;;
        "   署名鍵: "*) m="   Signing key: ${m#   署名鍵: }" ;;
        "純正プラグインに署名しました: "*) m="Signed official plugins: ${m#純正プラグインに署名しました: }" ;;
        "プラグイン署名に失敗しました（.so が無い場合は無視して構いません）。") m="Plugin signing failed (ignore if there is no .so)." ;;
        # --- 設定初期化 ---
        "設定ファイルの初期化") m="Initializing config files" ;;
        "example が見つかりません: "*) m="example not found: ${m#example が見つかりません: }" ;;
        "   既存: "*) m="   existing: ${m#   既存: }" ;;
        "作成: "*) m="created: ${m#作成: }" ;;
        "認証を有効化: auth.enabled=true / public_viewer=false") m="Auth enabled: auth.enabled=true / public_viewer=false" ;;
        "   初回アクセス時に /setup で管理者アカウントを作成してください") m="   Create an admin account at /setup on first access" ;;
        "既定言語を設定: language="*) m="Default language set: language=${m#既定言語を設定: language=}" ;;
        "language の書き込みを確認できませんでした（dashboard_config.json を確認してください）")
            m="Could not confirm the language was written (check dashboard_config.json)" ;;
        "agent_token を生成: "*) m="Generated agent_token: ${m#agent_token を生成: }" ;;
        "   所有者: "*) m="   owner: ${m#   所有者: }" ;;
        # --- sudoers / ヘルパー ---
        "smartctl の sudoers 設定") m="Configuring sudoers for smartctl" ;;
        "sudoers 設定: "*) m="sudoers entry: ${m#sudoers 設定: }" ;;
        "sudoers 設定に失敗しました（visudo 検証 or 権限）。手動で設定してください:")
            m="Failed to write sudoers (visudo or permissions). Configure manually:" ;;
        "smartctl 未導入のため sudoers 設定をスキップします（smartmontools 導入後に ./install.sh を再実行してください）。")
            m="smartctl not installed; skipping sudoers (re-run ./install.sh after installing smartmontools)." ;;
        "/etc/sudoers.d がありません。手動で設定してください。") m="/etc/sudoers.d not found. Configure manually." ;;
        "sudo が使えないため sudoers 設定をスキップします。S.M.A.R.T には権限が必要です。")
            m="sudo unavailable; skipping sudoers. S.M.A.R.T needs privileges." ;;
        "cron 読み取りヘルパーの導入") m="Installing cron-read helper" ;;
        "sudo が使えないため cron ヘルパーの導入をスキップします。手動で:")
            m="sudo unavailable; skipping cron helper. Manually:" ;;
        "cron ヘルパーが見つかりません: "*) m="cron helper not found: ${m#cron ヘルパーが見つかりません: }" ;;
        "/etc/sudoers.d がありません。手動で cron 用 sudoers を設定してください。") m="/etc/sudoers.d not found. Configure the cron sudoers manually." ;;
        "cron ヘルパー: "*) m="cron helper: ${m#cron ヘルパー: }" ;;
        "cron ヘルパーの設置に失敗しました: "*) m="Failed to install the cron helper: ${m#cron ヘルパーの設置に失敗しました: }" ;;
        "ワンクリック対処ヘルパーの導入") m="Installing one-click action helper" ;;
        "sudo が使えないため対処ヘルパーの導入をスキップします。手動で:")
            m="sudo unavailable; skipping action helper. Manually:" ;;
        "対処ヘルパーが見つかりません: "*) m="action helper not found: ${m#対処ヘルパーが見つかりません: }" ;;
        "/etc/sudoers.d がありません。手動で対処用 sudoers を設定してください。") m="/etc/sudoers.d not found. Configure the action sudoers manually." ;;
        "対処ヘルパー: "*) m="action helper: ${m#対処ヘルパー: }" ;;
        "対処ヘルパーの設置に失敗しました: "*) m="Failed to install the action helper: ${m#対処ヘルパーの設置に失敗しました: }" ;;
        "対処用 sudoers の設定に失敗しました（visudo 検証 or 権限）。手動で設定してください:")
            m="Failed to write the action sudoers (visudo or permissions). Configure manually:" ;;
        # --- systemd ---
        "systemd 登録（dashboard + agent）") m="Registering systemd services (dashboard + agent)" ;;
        "sudo が使えないため systemd 登録をスキップします。手動で: sudo systemd/migrate-agent-user.sh "*)
            m="sudo unavailable; skipping systemd registration. Run manually." ;;
        "旧 unit "*" を disable（二重起動防止）") m="Disabling legacy unit ${m#旧 unit }" ;;
        "   ℹ️ agent は移行済み（"*) m="   agent already migrated; restarting only" ;;
        *" が無いため agent の H-1 移行をスキップします。") m="migration script missing; skipping H-1 migration." ;;
        "agent を専用ユーザー (kizuna-eye) で systemd 管理下に移行しました") m="Migrated agent to the dedicated kizuna-eye user under systemd" ;;
        "H-1 移行に失敗しました。手動で再実行してください: sudo "*) m="H-1 migration failed. Re-run manually: sudo ${m#H-1 移行に失敗しました。手動で再実行してください: sudo }" ;;
        "dashboard unit 導入: "*) m="Installed dashboard unit: ${m#dashboard unit 導入: }" ;;
        *" が無いため dashboard unit をスキップします。") m="dashboard unit source missing; skipping." ;;
        "dashboard を systemd で起動/再起動しました") m="Started/restarted dashboard via systemd" ;;
        "dashboard の systemd 起動に失敗しました。手動で: sudo systemctl enable --now kizuna-dashboard")
            m="Failed to start dashboard via systemd. Run: sudo systemctl enable --now kizuna-dashboard" ;;
        "agent を再起動しました") m="Restarted agent" ;;
        "agent の再起動に失敗しました: sudo systemctl restart kizuna-eye-agent") m="Failed to restart agent: sudo systemctl restart kizuna-eye-agent" ;;
        # --- 起動 ---
        "2回目以降の実行です。起動する場合は ./start.sh を実行してください。") m="Already installed. Run ./start.sh to start." ;;
        "既に動作中です。./stop.sh で停止してから起動します。") m="Already running; stopping with ./stop.sh first." ;;
        "起動します...") m="Starting..." ;;
        "起動に失敗しました") m="Failed to start" ;;
        "start.sh が見つかりません。手動で起動してください。") m="start.sh not found. Start manually." ;;
        "起動をスキップしました（--no-start）") m="Skipped start (--no-start)" ;;
        "起動をスキップしました（systemd で起動済み（dashboard + agent））") m="Skipped start (already started via systemd)" ;;
        # --- ℹ️ 付き（info）は絵文字ごと差し替える（パターンは絵文字込みで一致させる） ---
        "ℹ️  2回目以降の実行です。起動する場合は ./start.sh を実行してください。") m="ℹ️  Already installed. Run ./start.sh to start." ;;
        "ℹ️  起動をスキップしました（--no-start）") m="ℹ️  Skipped start (--no-start)" ;;
        "ℹ️  起動をスキップしました（systemd で起動済み（dashboard + agent））") m="ℹ️  Skipped start (already started via systemd)" ;;
        # --- 完了サマリ（末尾の日本語注記を含む具体形を先に置く） ---
        "  更新            : ./update.sh  （GitHub から取得→再ビルド→再起動）") m="  Update          : ./update.sh  (fetch from GitHub -> rebuild -> restart)" ;;
        "  停止            : ./stop.sh  （H-1 導入時: agent は sudo systemctl stop kizuna-eye-agent）") m="  Stop            : ./stop.sh  (with H-1: agent via sudo systemctl stop kizuna-eye-agent)" ;;
        "  アンインストール: ./uninstall.sh  （データ温存 / 完全削除は --purge）") m="  Uninstall       : ./uninstall.sh  (keep data / full removal with --purge)" ;;
        "  アクセス        : "*) m="  Access          : ${m#  アクセス        : }" ;;
        "  ── はじめにお読みください ──────────────────────") m="  ── Getting started ───────────────────────────────" ;;
        "  ブラウザで上記の URL にアクセスし、管理者アカウントを作成してください。") m="  Open the URL above in a browser and create an admin account." ;;
        "  （初回は自動でセットアップ画面 /setup が開きます）") m="  (The setup screen /setup opens automatically on first access.)" ;;
        "  UI 言語         : "*) m="  UI language     : ${m#  UI 言語         : }" ;;
        "  ログ            : "*) m="  Logs            : ${m#  ログ            : }" ;;
        "  更新            : "*) m="  Update          : ${m#  更新            : }" ;;
        "  停止            : "*) m="  Stop            : ${m#  停止            : }" ;;
        "  アンインストール: "*) m="  Uninstall       : ${m#  アンインストール: }" ;;
    esac
    m="${m//引数なしのみ/no-args only}"
    m="${m//（右上のボタンで JA \/ EN を切り替え可）/ (toggle JA\/EN top-right)}"
    printf '%s' "$m"
}

# ログ表示。"▶ 見出し" の行は手順として自動採番し、区切り線で囲む。
# 誰が見ても「今どの手順をやっているか」が分かるようにする。
STEP_NO=0
log() {
    case "${1:-}" in
        "▶ "*)
            STEP_NO=$((STEP_NO+1))
            echo "──────────────────────────────────────────────────"
            if [ "$UI_LANG" = "en" ]; then
                echo " [Step ${STEP_NO}] $(tr_msg "${1#▶ }")"
            else
                echo " 【手順 ${STEP_NO}】$(tr_msg "${1#▶ }")"
            fi
            echo "──────────────────────────────────────────────────"
            ;;
        *) echo "$(tr_msg "$*")" ;;
    esac
}
warn() { echo "⚠️  $(tr_msg "$*")"; }
err()  { echo "❌ $(tr_msg "$*")"; }
ok()   { echo "✅ $(tr_msg "$*")"; }

echo ""
echo "╔══════════════════════════════════════════════════╗"
if [ "$UI_LANG" = "en" ]; then
    echo "║              Kizuna-Eye Setup                    ║"
else
    echo "║          Kizuna-Eye セットアップ                  ║"
fi
echo "╚══════════════════════════════════════════════════╝"

# ---- 0. 言語の選択（対話時のみ） ----
# --lang / KIZUNA_LANG で明示されていればそれを尊重する。未指定で対話端末
# （TTY）なら EN / JA を尋ねる。パイプ・自動化（非対話）では尋ねず既定 EN。
if [ "$LANG_SET" -eq 0 ] && [ -t 0 ]; then
    echo ""
    echo "Select UI language / UI 言語を選択してください:"
    echo "  1) English (default)"
    echo "  2) 日本語"
    printf "Choice [1/2] (default: 1): "
    read -r lang_choice || lang_choice=""
    case "$lang_choice" in
        2|ja|JA|japanese|Japanese|日本語) UI_LANG="ja" ;;
        1|en|EN|english|English|"") UI_LANG="en" ;;
        *) warn "不明な選択 '$lang_choice' のため English (en) を使用します"; UI_LANG="en" ;;
    esac
    log "   言語: $UI_LANG"
fi

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
    # python3 は H-1 移行 (migrate-agent-user.sh) が agent_config.json /
    # modules.json を JSON として安全に書き換えるのに使う必須ツール。
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:build-essential" "bwrap:bubblewrap" "python3:python3" )
    EXTRA_PKGS=( "ca-certificates" )
elif [ "$PM" = "pacman" ]; then
    # A-3: bubblewrap (bwrap) は .so 検査の隔離に必須（詳細は apt 側のコメント参照）。
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:base-devel" "bwrap:bubblewrap" "python3:python" )
    EXTRA_PKGS=()
else
    # A-3: bubblewrap (bwrap) は .so 検査の隔離に必須（詳細は apt 側のコメント参照）。
    REQUIRED=( "smartctl:smartmontools" "rsync:rsync" "git:git" "curl:curl" "gcc:gcc" "bwrap:bubblewrap" "python3:python3" )
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

# モードは umask に依存させない。root の umask 077 で mkdir すると
# /opt/kizuna-eye が 0700 になり、非 root のサービスユーザー
# (dashboard = 実行ユーザー / agent = kizuna-eye) が本体を exec できず、
# systemd が status=203/EXEC で起動できなくなる（実機で発生した）。
# ディレクトリは「通り抜け (x) + 読み取り (r)」を全ユーザーへ許可しておく。
for d in "$(dirname "$BIN_DIR")" "$BIN_DIR" "$PLUGIN_OUT_DIR" "$RUN_DIR"; do
    [ -d "$d" ] || continue
    chmod o+rx "$d" 2>/dev/null || true
done

TARGET_UID="${SUDO_UID:-$(id -u)}"; TARGET_GID="${SUDO_GID:-$(id -g)}"
if [ "$(id -u)" -eq 0 ]; then
    # sudo 経由なら SUDO_UID は実行者。root シェル (su - / sudo -i) から実行すると
    # SUDO_UID が無く root 所有のままになり、dashboard からのプラグイン配置・署名が
    # 権限エラーになる。所有権を直す方法を案内する。
    if [ -z "${SUDO_UID:-}" ]; then
        warn "root シェルから実行されています（SUDO_UID なし）。$BIN_DIR が root 所有になり、dashboard からのプラグイン配置・署名が失敗します。sudo ./install.sh の形で実行し直すか、後で chown -R <ユーザー> $BIN_DIR を実行してください。"
    fi
    chown -R "$TARGET_UID:$TARGET_GID" "$BIN_DIR" "$RUN_DIR" logs plugins 2>/dev/null || true
fi
ok "$BIN_DIR / $PLUGIN_OUT_DIR / logs / plugins"

# ---- 5. 本体ビルド ----
if [ "$DO_BUILD" -eq 1 ]; then
    # Prefer the repo VERSION file (single source of truth), then the git tag.
    # 変数名は KVERSION にして、環境変数 VERSION（os-release 等）との衝突を避ける。
    KVERSION="${KIZUNA_VERSION:-$(cat VERSION 2>/dev/null || git describe --tags --always 2>/dev/null || echo v0.7.0)}"
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

# ---- 6c. 設定ファイルの初期化（example から実設定を作る） ----
# install.sh 一本で初期セットアップを完結させるため、初回は example をコピーして
# 実設定を作る。purge 後の再インストールでは設定が無く、これが無いと H-1 移行
# (migrate-agent-user.sh) が $HOME/.kizuna-eye/data 不在で失敗し、agent が
# systemd に登録されず dashboard だけが動く（メモリ等が 0% のまま）。
RUN_USER="${SUDO_USER:-$(id -un)}"
RUN_HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)"
[ -n "$RUN_HOME" ] || RUN_HOME="$HOME"
KIZUNA_DATA="$RUN_HOME/.kizuna-eye/data"
log ""
log "▶ 設定ファイルの初期化"
mkdir -p "$KIZUNA_DATA"
# example のファイル名は一定でない（dashboard_config.example.json /
# agent_config.example.json）。base:example の組で明示する。
#
# modules.json は作らない: モジュールは UI（モジュール管理）から追加するもので、
# 初回インストールでは存在しないのが正しい状態（無ければ「モジュール無し」と
# して扱われる）。example をコピーすると未設定のバックアップ等が有効になり、
# 空ファイルを作っても初回からモジュールの話が出てしまう。
CREATED_DASHBOARD=0
for pair in \
    "dashboard_config:dashboard_config.example.json" \
    "agent_config:agent_config.example.json"; do
    base="${pair%%:*}"
    ex="${pair##*:}"
    src="$PWD/$ex"
    dst="$KIZUNA_DATA/${base}.json"
    if [ ! -f "$src" ]; then
        warn "example が見つかりません: $src"
    elif [ -f "$dst" ]; then
        log "   既存: $dst"
    else
        cp "$src" "$dst"
        ok "作成: $dst"
        [ "$base" = "dashboard_config" ] && CREATED_DASHBOARD=1
    fi
done

# 新規作成時のみ認証を有効化する（セキュア既定）。example は「簡単優先」で
# auth.enabled=false のため、そのままだと初回にログイン画面が出ず、管理者
# アカウントを作る導線（/setup）に到達できない。既存設定は尊重して触らない。
# secure_cookies は HTTP 直アクセスでログインできなくなるため false のまま
# （HTTPS/リバースプロキシ運用時のみ true にする）。
if [ "$CREATED_DASHBOARD" -eq 1 ]; then
    DCFG="$KIZUNA_DATA/dashboard_config.json"
    sed -i '/"auth": {/,/}/ { s/"enabled": false/"enabled": true/; s/"public_viewer": true/"public_viewer": false/; }' "$DCFG"
    ok "認証を有効化: auth.enabled=true / public_viewer=false"
    log "   初回アクセス時に /setup で管理者アカウントを作成してください"
    # UI の既定言語を書き込む（--lang / KIZUNA_LANG、未指定は en）。
    if grep -q '"language"' "$DCFG" 2>/dev/null; then
        sed -i -E "s/\"language\"[[:space:]]*:[[:space:]]*\"[^\"]*\"/\"language\": \"$UI_LANG\"/" "$DCFG"
    elif grep -q '"listen_addr"' "$DCFG" 2>/dev/null; then
        # language キーが無ければ listen_addr の行の後に挿入する。
        sed -i -E "s|(\"listen_addr\"[[:space:]]*:[[:space:]]*\"[^\"]*\",)|\1\n    \"language\": \"$UI_LANG\",|" "$DCFG"
    fi
    if grep -q "\"language\": \"$UI_LANG\"" "$DCFG" 2>/dev/null; then
        ok "既定言語を設定: language=$UI_LANG"
    else
        warn "language の書き込みを確認できませんでした（dashboard_config.json を確認してください）"
    fi
fi
# agent_token を両設定に同じランダム値で入れる（CHANGE_ME のままにしない）。
# auth を有効化したときに agent が弾かれないようにする。
TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
for f in "$KIZUNA_DATA/dashboard_config.json" "$KIZUNA_DATA/agent_config.json"; do
    [ -f "$f" ] || continue
    if grep -q 'CHANGE_ME_TO_A_LONG_RANDOM_STRING' "$f" 2>/dev/null; then
        sed -i "s/CHANGE_ME_TO_A_LONG_RANDOM_STRING/$TOKEN/" "$f"
        ok "agent_token を生成: $(basename "$f")"
    fi
done
# .kizuna-eye 全体を実行ユーザー所有にする（sudo 実行で root 所有になり、
# dashboard/agent が書けなくなるのを防ぐ。署名鍵もここで一緒に直る）。
chown -R "$RUN_USER:$RUN_USER" "$RUN_HOME/.kizuna-eye" 2>/dev/null || true
log "   所有者: $(stat -c '%U:%G' "$RUN_HOME/.kizuna-eye") (data: $KIZUNA_DATA)"

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
        # 既インストール判定: agent の systemd unit があり、専用ユーザーが
        # 存在すれば移行済み。この場合は migrate-agent-user.sh を再実行しない。
        # 再実行すると「バックアップ → 停止 → 権限再調整 → 再起動」が毎回走り、
        # install.sh を叩くたびにサービスが落ちる（再インストールになってしまう）。
        ALREADY_INSTALLED=0
        if [ -f /etc/systemd/system/kizuna-eye-agent.service ] \
           && id "kizuna-eye" >/dev/null 2>&1; then
            ALREADY_INSTALLED=1
        fi

        # agent バイナリを kizuna-eye から実行できるようにする。ビルドのたびに
        # 所有者/グループが実行ユーザーへ戻るため、ALREADY_INSTALLED で
        # migrate-agent-user.sh（§3.5）をスキップしても毎回ここで適用する。
        # 漏れると agent が 203/EXEC (Permission denied) で起動不能になる。
        AGENT_BIN="$BIN_DIR/agent_linux"
        if [ -f "$AGENT_BIN" ] && id "kizuna-eye" >/dev/null 2>&1; then
            $SUDO chgrp kizuna-eye "$AGENT_BIN" 2>/dev/null || true
            $SUDO chmod 0750 "$AGENT_BIN" 2>/dev/null || true
        fi

        # 1) 旧 unit を disable（二重起動防止）
        for old in kizuna-eye kizuna-agent; do
            if $SUDO systemctl is-enabled --quiet "$old" 2>/dev/null; then
                warn "旧 unit $old を disable（二重起動防止）"
                $SUDO systemctl disable --now "$old" || true
            fi
        done

        MIGRATE_SCRIPT="systemd/migrate-agent-user.sh"
        if [ "$ALREADY_INSTALLED" -eq 1 ]; then
            # 2a) 移行済み: 破壊的な再移行はせず、agent を再起動して新バイナリを
            #     反映するだけにする。
            log "   ℹ️ agent は移行済み（$MIGRATE_SCRIPT をスキップ、再起動のみ）"
        elif [ ! -f "$MIGRATE_SCRIPT" ]; then
            warn "$MIGRATE_SCRIPT が無いため agent の H-1 移行をスキップします。"
        elif $SUDO "$MIGRATE_SCRIPT" "$RUN_USER"; then
            ok "agent を専用ユーザー (kizuna-eye) で systemd 管理下に移行しました"
        else
            warn "H-1 移行に失敗しました。手動で再実行してください: sudo $MIGRATE_SCRIPT $RUN_USER"
        fi

        # 2b) 移行済みでも agent unit は最新テンプレートへ追従させる。移行を
        #     スキップする分岐では unit が再生成されないため、テンプレート側の
        #     修正（例: ReadWritePaths の配備依存パス除去と "-" による不在許容）
        #     が既存環境へ永久に伝わらない。
        AGENT_SRC="systemd/kizuna-eye-agent.service"
        AGENT_DST="/etc/systemd/system/kizuna-eye-agent.service"
        if [ "$ALREADY_INSTALLED" -eq 1 ] && [ -f "$AGENT_SRC" ] && [ -f "$AGENT_DST" ]; then
            if ! $SUDO grep -q 'Kizuna-Eye Agent' "$AGENT_DST" 2>/dev/null; then
                warn "$AGENT_DST は Kizuna-Eye の unit ではないため更新しません（手動で確認してください）"
            else
                AGENT_TMP="$(mktemp)"
                sed -e "s|__DIR__|$PWD|g" \
                    -e "s|__BIN__|$BIN_DIR|g" \
                    -e "s|__DATA__|$DATA_DIR|g" \
                    "$AGENT_SRC" > "$AGENT_TMP"
                if grep -q '__[A-Z][A-Z_]*__' "$AGENT_TMP"; then
                    warn "agent unit に未置換のプレースホルダが残るため更新をスキップします: grep -n '__[A-Z][A-Z_]*__' $AGENT_TMP"
                elif $SUDO cmp -s "$AGENT_TMP" "$AGENT_DST"; then
                    log "   ℹ️ agent unit は最新です"
                else
                    # 手編集を失わないよう退避してから差し替える（migrate と同じ流儀）。
                    # 退避先は mktemp で確保する。時刻ベースの予測可能な名前で root が
                    # 書き込むと、同名の symlink を先に置かれた場合に書き込み先を
                    # 誘導され得るため（CWE-59）、O_EXCL で新規作成される mktemp を使う。
                    AGENT_BAK="$(mktemp /tmp/kizuna-eye-agent.service.bak-XXXXXXXX)" || AGENT_BAK=""
                    if [ -n "$AGENT_BAK" ]; then
                        $SUDO cp -a "$AGENT_DST" "$AGENT_BAK" 2>/dev/null || true
                    fi
                    $SUDO install -m 0644 -o root -g root "$AGENT_TMP" "$AGENT_DST"
                    ok "agent unit を最新テンプレートで更新しました（旧版は /tmp に退避）"
                    if [ -n "$AGENT_BAK" ]; then
                        log "   ℹ️ 旧版の退避先: $AGENT_BAK"
                    fi
                fi
                rm -f "$AGENT_TMP"
            fi
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

        # 4) systemd で起動/再起動（agent は移行時に起動済み）
        $SUDO systemctl daemon-reload
        $SUDO systemctl enable kizuna-dashboard >/dev/null 2>&1 || true
        if $SUDO systemctl restart kizuna-dashboard; then
            ok "dashboard を systemd で起動/再起動しました"
            DO_START=0   # systemd 管理になったため start.sh は使わない
            SKIP_START_REASON="systemd で起動済み（dashboard + agent）"
        else
            warn "dashboard の systemd 起動に失敗しました。手動で: sudo systemctl enable --now kizuna-dashboard"
        fi
        # restart が成功を返しても直後に落ちることがある（例: 203/EXEC =
        # バイナリ本体か親ディレクトリの権限不足）。数秒待って状態を確認し、
        # 落ちていれば journal の見方を案内する（「入ったつもり」を防ぐ）。
        sleep 2
        DASH_STATE="$($SUDO systemctl is-active kizuna-dashboard 2>/dev/null || true)"
        if [ "$DASH_STATE" = "active" ]; then
            ok "dashboard は稼働中 (systemd)"
        else
            warn "dashboard が active ではありません（state=$DASH_STATE）。確認: sudo journalctl -u kizuna-dashboard -n 50 --no-pager"
        fi
        # 移行済みの場合、agent も再起動して新バイナリを反映する（停止は伴わない）。
        if [ "$ALREADY_INSTALLED" -eq 1 ]; then
            # start-limit-hit（再起動の繰り返しで failed）だと restart が弾かれる
            # ため、先に failed 状態を解除してから再起動する。
            $SUDO systemctl reset-failed kizuna-eye-agent 2>/dev/null || true
            $SUDO systemctl restart kizuna-eye-agent 2>/dev/null \
                && ok "agent を再起動しました" \
                || warn "agent の再起動に失敗しました: sudo systemctl restart kizuna-eye-agent"
            sleep 1
            AGENT_STATE="$($SUDO systemctl is-active kizuna-eye-agent 2>/dev/null || true)"
            if [ "$AGENT_STATE" != "active" ]; then
                warn "agent が active ではありません（state=$AGENT_STATE）。確認: sudo journalctl -u kizuna-eye-agent -n 50 --no-pager"
                # 203/EXEC の典型原因（親ディレクトリ / バイナリの権限）を切り分けて案内する。
                # sudo のパスワード待ちで止まらないよう、非対話 (-n) が通るときだけ調べる。
                if getent passwd kizuna-eye >/dev/null 2>&1 && $SUDO -n true 2>/dev/null; then
                    if ! $SUDO -n -u kizuna-eye test -x "$BIN_DIR/agent_linux" 2>/dev/null; then
                        warn "kizuna-eye から $BIN_DIR/agent_linux を実行できません。: sudo chmod o+rx $(dirname "$BIN_DIR") $BIN_DIR && sudo chgrp kizuna-eye $BIN_DIR/agent_linux && sudo chmod 0750 $BIN_DIR/agent_linux"
                    fi
                fi
            fi
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
    if [ -n "$SKIP_START_REASON" ]; then
        log "ℹ️  起動をスキップしました（$SKIP_START_REASON）"
    else
        log "ℹ️  起動をスキップしました（--no-start）"
    fi
fi

# ---- 10. 完了 ----
LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
log ""
echo "╔══════════════════════════════════════════════════╗"
if [ "$UI_LANG" = "en" ]; then
    echo "║            ✅ Setup complete                      ║"
else
    echo "║            ✅ セットアップ完了                    ║"
fi
echo "╚══════════════════════════════════════════════════╝"
log ""
log "  アクセス        : http://${LAN_IP:-<server-ip>}:8080"
log ""
log "  ── はじめにお読みください ──────────────────────"
log "  ブラウザで上記の URL にアクセスし、管理者アカウントを作成してください。"
log "  （初回は自動でセットアップ画面 /setup が開きます）"
log "  UI 言語         : ${UI_LANG}（右上のボタンで JA / EN を切り替え可）"
log "  ログ            : logs/dashboard.log, logs/agent.log"
log "  更新            : ./update.sh  （GitHub から取得→再ビルド→再起動）"
log "  停止            : ./stop.sh  （H-1 導入時: agent は sudo systemctl stop kizuna-eye-agent）"
log "  アンインストール: ./uninstall.sh  （データ温存 / 完全削除は --purge）"
log ""
