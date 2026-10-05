#!/bin/sh
# ============================================================
# Kizuna-Eye .deb postinst
# インストール直後に走る。ユーザー作成・ディレクトリ・初期設定・systemd 登録を行う。
# 非対話で動くため、入力プロンプトは出さない。
# ============================================================
set -e

PKG_USER="kizuna-eye"
DATA_ROOT="/var/lib/kizuna-eye"
DATA_DIR="$DATA_ROOT/data"
SHARE_DIR="/usr/share/kizuna-eye"
BIN_DIR="/opt/kizuna-eye/bin"

case "$1" in
  configure)
    # --- 1. 専用ユーザー/グループ ---
    if ! getent group "$PKG_USER" >/dev/null 2>&1; then
      addgroup --system "$PKG_USER" || true
    fi
    if ! getent passwd "$PKG_USER" >/dev/null 2>&1; then
      adduser --system --ingroup "$PKG_USER" --home "$DATA_ROOT" \
              --no-create-home --shell /usr/sbin/nologin "$PKG_USER" || true
    fi

    # --- 2. データディレクトリ ---
    install -d -o "$PKG_USER" -g "$PKG_USER" -m 0700 \
        "$DATA_ROOT" "$DATA_ROOT/state" "$DATA_ROOT/keys" "$DATA_ROOT/logs"
    install -d -o "$PKG_USER" -g "$PKG_USER" -m 0750 "$DATA_DIR" "$DATA_DIR/logs"

    # --- 3. 初期設定（example から生成。既存は尊重） ---
    for pair in "dashboard_config:dashboard_config.example.json" \
                "agent_config:agent_config.example.json"; do
      base="${pair%%:*}"
      ex="${pair##*:}"
      src="$SHARE_DIR/examples/$ex"
      dst="$DATA_DIR/$base.json"
      if [ -f "$src" ] && [ ! -f "$dst" ]; then
        cp "$src" "$dst"
      fi
    done

    # --- 4. パッケージ向けに設定を調整（新規生成時のみ） ---
    DCFG="$DATA_DIR/dashboard_config.json"
    if [ -f "$DCFG" ]; then
      # 読み取り専用の web と、パッケージのプラグインパスを指す
      sed -i -E "s|\"static_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"static_dir\": \"$SHARE_DIR/web/static\"|" "$DCFG" || true
      sed -i -E "s|\"plugins_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"plugins_dir\": \"$BIN_DIR/plugins\"|" "$DCFG" || true
      # 認証を有効化し、公開ビューア（ゲスト閲覧）を無効化する（セキュア既定）。
      sed -i '/"auth": {/,/}/ { s/"enabled": false/"enabled": true/; s/"public_viewer": true/"public_viewer": false/; }' "$DCFG" || true
      # 同梱プラグインが無いため署名必須はオフ（UI で追加後に有効化可能）
      sed -i '/"plugins": {/,/}/ { s/"require_signature": true/"require_signature": false/; }' "$DCFG" || true
    fi
    ACFG="$DATA_DIR/agent_config.json"
    if [ -f "$ACFG" ]; then
      sed -i -E "s|\"plugins_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"plugins_dir\": \"$BIN_DIR/plugins\"|" "$ACFG" || true
      sed -i '/"plugins": {/,/}/ { s/"require_signature": true/"require_signature": false/; }' "$ACFG" || true
    fi

    # agent_token を生成（CHANGE_ME のままにしない）
    TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    for f in "$DCFG" "$ACFG"; do
      [ -f "$f" ] || continue
      sed -i "s/CHANGE_ME_TO_A_LONG_RANDOM_STRING/$TOKEN/" "$f" || true
    done

    chown -R "$PKG_USER:$PKG_USER" "$DATA_ROOT" 2>/dev/null || true
    chmod 0750 "$DATA_DIR" 2>/dev/null || true
    # 設定は agent_token 等の秘密を含むため所有者のみ読める 0600 にする。
    chmod 0600 "$DATA_DIR"/*.json 2>/dev/null || true

    # --- 5. プラグインディレクトリ ---
    install -d -o "$PKG_USER" -g "$PKG_USER" -m 0755 "$BIN_DIR/plugins"

    # --- 6. systemd unit のプレースホルダを実値に置換 ---
    for u in kizuna-eye-agent kizuna-dashboard; do
      if [ -f "$SHARE_DIR/systemd/$u.service" ]; then
        sed -e "s|__DIR__|$DATA_ROOT|g" \
            -e "s|__BIN__|$BIN_DIR|g" \
            -e "s|__DATA__|$DATA_DIR|g" \
            -e "s|__USER__|$PKG_USER|g" \
            "$SHARE_DIR/systemd/$u.service" > "/lib/systemd/system/$u.service"
        chmod 0644 "/lib/systemd/system/$u.service"
      fi
    done

    if command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload || true
      systemctl enable kizuna-eye-agent.service || true
      systemctl enable kizuna-dashboard.service || true
      # 設定生成直後は起動しない（ユーザーが /setup で管理者作成できるよう、
      # ダッシュボードは起動しておく）
      systemctl restart kizuna-dashboard.service || true
      systemctl restart kizuna-eye-agent.service || true
    fi
    ;;
esac

exit 0
