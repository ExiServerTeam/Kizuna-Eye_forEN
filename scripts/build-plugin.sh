#!/bin/bash
# ============================================================
# Kizuna plugin build -> sign -> deploy (one command)
#
# 前回のヒヤリハット（.so を再ビルドしたのに .sig を更新せず、
# 署名検証でプラグインのロードが拒否された）を構造的に防ぐ。
# 「ビルド → 署名 → 配置」を1コマンドで行い、各ステップで成否を表示し、
# 失敗したら即座に exit 1 する。
#
# 使い方:
#   ./scripts/build-plugin.sh kizuna_security
#   ./scripts/build-plugin.sh kizuna_backup_lite
#
# 署名鍵は環境変数 KIZUNA_SIGNING_KEY で上書きできる。
# ============================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
DEPLOY_DIR="$BIN_DIR/plugins"
SIGN_BIN="$BIN_DIR/plugin-sign"
SIGNING_KEY="${KIZUNA_SIGNING_KEY:-$HOME/.kizuna-eye/keys/plugin_signing/plugin_signing.key}"
# 外部プラグイン（別リポジトリ）のソース位置。install.sh / update.sh と同じ変数名。
# 未指定なら「そのプラグインは扱わない」（opt-in）。
LITE_PLUGIN_DIR="${KIZUNA_LITE_PLUGIN_DIR:-}"

# Go が PATH に無い環境（cron 等）でも動くように。
export PATH="$PATH:/usr/local/go/bin"

log()  { echo "[build-plugin] $*"; }
ok()   { echo "  [ OK ] $*"; }
ng()   { echo "  [ NG ] $*" >&2; }

NAME="${1:-}"
if [ -z "$NAME" ]; then
    echo "usage: $0 <plugin-name>" >&2
    echo "  plugin-name: kizuna_security | kizuna_backup_lite" >&2
    exit 2
fi

# プラグイン名 -> ソースディレクトリ。
case "$NAME" in
    kizuna_security)
        PLUGIN_DIR="$REPO_ROOT/plugins/Kizuna-Security/plugin"
        ;;
    kizuna_backup_lite)
        if [ -z "$LITE_PLUGIN_DIR" ]; then
            ng "kizuna_backup_lite は別リポジトリのプラグインです。"
            ng "ソースの場所を指定してください: KIZUNA_LITE_PLUGIN_DIR=/path/to/Kizuna-Backup-LITE/plugin $0 $NAME"
            exit 1
        fi
        PLUGIN_DIR="$LITE_PLUGIN_DIR"
        ;;
    *)
        # 未知の名前は <repo>/plugins/<name>/plugin を試す。
        PLUGIN_DIR="$REPO_ROOT/plugins/$NAME/plugin"
        ;;
esac

log "プラグイン: $NAME"
log "ソース:     $PLUGIN_DIR"
log "配置先:     $DEPLOY_DIR"
log "署名鍵:     $SIGNING_KEY"

# --- 前提チェック ---
[ -d "$PLUGIN_DIR" ] || { ng "ソースディレクトリがありません: $PLUGIN_DIR"; exit 1; }
[ -x "$SIGN_BIN" ]   || { ng "plugin-sign が見つかりません: $SIGN_BIN"; exit 1; }
[ -r "$SIGNING_KEY" ] || { ng "署名鍵が読めません: $SIGNING_KEY (KIZUNA_SIGNING_KEY で上書き可)"; exit 1; }
mkdir -p "$DEPLOY_DIR" || { ng "配置先を作成できません: $DEPLOY_DIR"; exit 1; }

SO="$NAME.so"
SIG="$NAME.so.sig"

# --- 1) ビルド ---
log "1/4 ビルド (GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin)"
if ( cd "$PLUGIN_DIR" && GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o "$SO" . ); then
    ok "ビルド成功: $PLUGIN_DIR/$SO"
else
    ng "ビルド失敗"
    exit 1
fi

# --- 2) 署名 ---
log "2/4 署名 (plugin-sign -sign)"
if "$SIGN_BIN" -sign "$PLUGIN_DIR/$SO" -private-key "$SIGNING_KEY"; then
    ok "署名成功: $PLUGIN_DIR/$SIG"
else
    ng "署名失敗"
    exit 1
fi

# 署名ファイルの存在確認（署名漏れの最終防波堤）。
if [ ! -f "$PLUGIN_DIR/$SIG" ]; then
    ng "署名ファイルが生成されていません: $PLUGIN_DIR/$SIG"
    exit 1
fi
ls -la "$PLUGIN_DIR/$SO" "$PLUGIN_DIR/$SIG" || { ng "ビルド成果物の確認に失敗"; exit 1; }

# --- 3) 配置 ---
log "3/4 配置 (.so と .sig を両方コピー)"
if cp "$PLUGIN_DIR/$SO" "$PLUGIN_DIR/$SIG" "$DEPLOY_DIR/"; then
    ok "配置成功: $DEPLOY_DIR/$SO, $DEPLOY_DIR/$SIG"
else
    ng "配置失敗"
    exit 1
fi

# --- 4) 配置確認 + 署名検証 ---
log "4/4 配置確認"
ls -la "$DEPLOY_DIR/$SO" "$DEPLOY_DIR/$SIG" || { ng "配置の確認に失敗"; exit 1; }
if "$SIGN_BIN" -verify "$DEPLOY_DIR/$SO" -public-key "${KIZUNA_PLUGIN_PUBKEY:-$(dirname "$SIGNING_KEY")/plugin_signing.pub}"; then
    ok "配置先の署名検証 OK"
else
    ng "配置先の署名検証に失敗"
    exit 1
fi

log "完了: $NAME をビルド・署名・配置しました。"
log "反映には agent の再起動が必要です（要許可）: systemctl restart kizuna-agent / ./stop.sh agent && ./start.sh agent"
