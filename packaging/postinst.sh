#!/bin/sh
# ============================================================
# Kizuna-Eye .deb postinst
#
# インストール直後に走る。ユーザー作成・ディレクトリ・初期設定・systemd 登録を行う。
# 非対話で動くため、入力プロンプトは出さない。言語は Web UI の切替（JA/EN）で
# 選べるので、ここでは尋ねない（apt install を一行で完結させるため）。
#
# このスクリプトが成功すれば、ユーザーがやることはブラウザで
# http://<host>:8080 を開いて /setup で管理者を作るだけだ。
# postinst runs right after install: user, directories, initial config and
# systemd registration. It is non-interactive (no prompts) so
# `apt install kizuna-eye` completes in one line; the UI language is chosen
# later in the web UI. When this script succeeds, the only thing left for the
# operator is to open http://<host>:8080 and create the admin account.
#
# アップグレード時の方針: 既存の設定ファイルは尊重し、内容を一切書き換えない。
# 設定の調整（plugins_dir、auth 有効化、通知オフ等）は「新規作成時のみ」行う。
# これにより、ユーザーが Web UI で入れた変更が apt upgrade で壊れない。
# Upgrade policy: never rewrite an existing config; the config adjustments
# below run only when the file is created for the first time, so UI changes
# survive `apt upgrade`.
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

    # --- 3. 初期設定（example から生成。既存はそのまま残す） ---
    # 新規作成したかどうかを覚えておき、調整は新規のときだけ行う。
    CREATED=0
    for pair in "dashboard_config:dashboard_config.example.json" \
                "agent_config:agent_config.example.json"; do
      base="${pair%%:*}"
      ex="${pair##*:}"
      src="$SHARE_DIR/examples/$ex"
      dst="$DATA_DIR/$base.json"
      if [ -f "$src" ] && [ ! -f "$dst" ]; then
        cp "$src" "$dst"
        CREATED=1
      fi
    done

    # --- 4. パッケージ向けに設定を調整（新規作成時のみ） ---
    DCFG="$DATA_DIR/dashboard_config.json"
    ACFG="$DATA_DIR/agent_config.json"
    if [ "$CREATED" -eq 1 ]; then
      if [ -f "$DCFG" ]; then
        # 読み取り専用の web と、パッケージのプラグインパスを指す
        sed -i -E "s|\"static_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"static_dir\": \"$SHARE_DIR/web/static\"|" "$DCFG" || true
        sed -i -E "s|\"plugins_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"plugins_dir\": \"$BIN_DIR/plugins\"|" "$DCFG" || true
        # 認証を有効化し、公開ビューア（ゲスト閲覧）を無効化する（セキュア既定）。
        sed -i '/"auth": {/,/}/ { s/"enabled": false/"enabled": true/; s/"public_viewer": true/"public_viewer": false/; }' "$DCFG" || true
        # 同梱プラグインが無いため署名必須はオフ（UI で追加後に有効化可能）
        sed -i '/"plugins": {/,/}/ { s/"require_signature": true/"require_signature": false/; }' "$DCFG" || true
        # 初期状態では通知をオフにする。example の webhook はプレースホルダで、
        # 有効なチャンネルが無いのに notifications.enabled=true だと起動時に
        # 「有効なチャンネルがありません」警告が出る。ユーザーは Web UI で
        # 通知を設定したときに有効化する。
        sed -i '/"notifications": {/,/^    }/ { s/"enabled": true/"enabled": false/; }' "$DCFG" || true
      fi
      if [ -f "$ACFG" ]; then
        sed -i -E "s|\"plugins_dir\"[[:space:]]*:[[:space:]]*\"[^\"]*\"|\"plugins_dir\": \"$BIN_DIR/plugins\"|" "$ACFG" || true
        sed -i '/"plugins": {/,/}/ { s/"require_signature": true/"require_signature": false/; }' "$ACFG" || true
      fi
    fi

    # --- 5. agent_token の生成 ---
    # CHANGE_ME が残っているファイルにだけ、同じトークンを入れる。
    # dashboard と agent で同じ値を使うことで、認証有効時に agent が弾かれない。
    # アップグレード時、既に実トークンが入っていれば何もしない（sed が一致しない）。
    TOKEN=""
    for f in "$DCFG" "$ACFG"; do
      [ -f "$f" ] || continue
      if grep -q 'CHANGE_ME_TO_A_LONG_RANDOM_STRING' "$f" 2>/dev/null; then
        if [ -z "$TOKEN" ]; then
          TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
        fi
        sed -i "s/CHANGE_ME_TO_A_LONG_RANDOM_STRING/$TOKEN/" "$f" || true
      fi
    done

    chown -R "$PKG_USER:$PKG_USER" "$DATA_ROOT" 2>/dev/null || true
    chmod 0750 "$DATA_DIR" 2>/dev/null || true
    # 設定は agent_token 等の秘密を含むため所有者のみ読める 0600 にする。
    chmod 0600 "$DATA_DIR"/*.json 2>/dev/null || true

    # --- 6. プラグインディレクトリ ---
    install -d -o "$PKG_USER" -g "$PKG_USER" -m 0755 "$BIN_DIR/plugins"

    # --- 6b. 特権ヘルパーとワンクリック対処サービス（特権ソケット） ---
    # ダッシュボードは非 root (kizuna-eye) で動く。kill / ファイアウォール /
    # cron 削除 / ファイル復元は root で動く専用サービス (kizuna-action.service)
    # に委譲する。ダッシュボードは Unix ソケット /run/kizuna-eye/action.sock に
    # JSON を送るだけで sudo も sudoers も使わない。
    HELPERS_SRC="$SHARE_DIR/helpers"
    if [ -d "$HELPERS_SRC" ]; then
      install -d -m 0755 /usr/lib/kizuna-eye
      install -m 0755 -o root -g root "$HELPERS_SRC/kizuna-action.sh" /usr/lib/kizuna-eye/kizuna-action.sh 2>/dev/null || true
      install -m 0755 -o root -g root "$HELPERS_SRC/kizuna-action-server" /usr/lib/kizuna-eye/kizuna-action-server 2>/dev/null || true
      # cron 読み取りヘルパーは agent が使うため残す（CAP_DAC_READ_SEARCH が無い環境向け）。
      install -m 0755 -o root -g root "$HELPERS_SRC/kizuna-cron-read.sh" /usr/local/bin/kizuna-cron-read.sh 2>/dev/null || true
    fi

    # systemd unit / tmpfiles（action socket）を配置。
    if [ -f "$SHARE_DIR/systemd/kizuna-action.socket" ]; then
      install -m 0644 -o root -g root "$SHARE_DIR/systemd/kizuna-action.socket" /lib/systemd/system/kizuna-action.socket
      install -m 0644 -o root -g root "$SHARE_DIR/systemd/kizuna-action.service" /lib/systemd/system/kizuna-action.service
    fi
    if [ -f "$SHARE_DIR/systemd/kizuna-eye-tmpfiles.conf" ]; then
      install -m 0644 -o root -g root "$SHARE_DIR/systemd/kizuna-eye-tmpfiles.conf" /usr/lib/tmpfiles.d/kizuna-eye.conf
      command -v systemd-tmpfiles >/dev/null 2>&1 && systemd-tmpfiles --create /usr/lib/tmpfiles.d/kizuna-eye.conf >/dev/null 2>&1 || true
    fi
    # 旧 sudoers 方式の残骸を掃除（sudo → socket 方式への移行）。
    rm -f /etc/sudoers.d/kizuna-security-action /usr/local/bin/kizuna-action.sh 2>/dev/null || true

    # --- 7. systemd unit のプレースホルダを実値に置換 ---
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

    # --- 8. systemd 有効化・起動 ---
    # install.sh 経路は /etc/systemd/system に unit を置く。deb は /lib/systemd/system
    # に置くが、/etc の unit が残っていると systemd は /etc を優先するため、
    # deb の unit が無視され、prerm も停止できず二重管理になる（実機で発生）。
    # deb 導入時は /etc 側の残骸を先に退避して取り除く。
    # install.sh places units in /etc/systemd/system; the deb places them in
    # /lib/systemd/system. systemd prefers /etc, so a leftover /etc unit would
    # shadow the deb one and prerm could not stop it. Remove the /etc leftovers.
    for u in kizuna-eye-agent kizuna-dashboard kizuna-action kizuna-agent kizuna-eye kizuna-log-collector kizuna-watchdog; do
      for ext in service socket; do
        f="/etc/systemd/system/$u.$ext"
        if [ -f "$f" ] && ! dpkg -S "$f" >/dev/null 2>&1; then
          mv -f "$f" "$f.deb-bak" 2>/dev/null || rm -f "$f"
        fi
      done
    done

    # systemd が PID 1 でない環境（コンテナ等）では /run/systemd/system が無い。
    # その場合 systemctl が存在しても実体に繋がらないため、無条件に叩くと
    # 「System has not been booted with systemd」警告が出る。ここで検出して
    # 静かにスキップし、偽の「サービスが落ちている」警告を出さない。
    # Detect a real systemd init (PID 1). In a container it is absent, so the
    # enable/start step is skipped silently instead of emitting a misleading
    # "service is not active" warning.
    HAS_SYSTEMD=0
    if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
      HAS_SYSTEMD=1
    fi

    DASH_OK=0
    AGENT_OK=0
    if [ "$HAS_SYSTEMD" -eq 1 ]; then
      systemctl daemon-reload || true
      systemctl enable kizuna-eye-agent.service || true
      systemctl enable kizuna-dashboard.service || true
      # ワンクリック対処サービス（特権ソケット）を有効化。接続時に起動される。
      systemctl enable --now kizuna-action.socket || true
      # 設定生成直後は起動しない（ユーザーが /setup で管理者作成できるよう、
      # ダッシュボードは起動しておく）
      systemctl restart kizuna-dashboard.service || true
      systemctl restart kizuna-eye-agent.service || true
      # 起動直後に落ちることがある（例: 203/EXEC = 実行権限不足）。数秒待つ。
      sleep 2
      systemctl is-active --quiet kizuna-dashboard.service && DASH_OK=1 || true
      systemctl is-active --quiet kizuna-eye-agent.service && AGENT_OK=1 || true
    fi

    # --- 9. 完了メッセージ ---
    # 初回ユーザーが次に何をすればよいか（ブラウザで開くだけ）を示す。
    IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [ -n "$IP" ] || IP="<server-ip>"
    echo ""
    echo "================================================================"
    echo " Kizuna-Eye installed"
    echo "----------------------------------------------------------------"
    echo " Open the dashboard in your browser and create an admin account:"
    echo "     http://${IP}:8080"
    echo " (the first access is redirected to /setup automatically)"
    echo "----------------------------------------------------------------"
    echo " Configuration is done entirely in the web UI."
    echo " Config files (edit by hand only if needed):"
    echo "     $DATA_DIR/dashboard_config.json"
    echo "     $DATA_DIR/agent_config.json"
    if [ "$HAS_SYSTEMD" -eq 1 ]; then
      echo " Logs: journalctl -u kizuna-dashboard -u kizuna-eye-agent"
    else
      echo " systemd was not detected; start services manually if needed."
    fi
    echo "================================================================"
    if [ "$HAS_SYSTEMD" -eq 1 ]; then
      if [ "$DASH_OK" -ne 1 ]; then
        echo "WARNING: kizuna-dashboard.service is not active." >&2
        echo "  Check: journalctl -u kizuna-dashboard -n 50 --no-pager" >&2
      fi
      if [ "$AGENT_OK" -ne 1 ]; then
        echo "WARNING: kizuna-eye-agent.service is not active." >&2
        echo "  Check: journalctl -u kizuna-eye-agent -n 50 --no-pager" >&2
      fi
    fi
    echo ""
    ;;
esac

exit 0
