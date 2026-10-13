#!/bin/sh
# ============================================================
# Kizuna-Eye .deb postrm
# unit を消す。purge 時はデータとユーザーも消す。
# ============================================================
set -e

PKG_USER="kizuna-eye"
DATA_ROOT="/var/lib/kizuna-eye"

case "$1" in
  remove)
    # unit ファイルは prerm ではなくここで消す（remove 時に残骸を残さない）
    rm -f /lib/systemd/system/kizuna-eye-agent.service \
          /lib/systemd/system/kizuna-dashboard.service \
          /lib/systemd/system/kizuna-action.socket \
          /lib/systemd/system/kizuna-action.service \
          /usr/lib/tmpfiles.d/kizuna-eye.conf 2>/dev/null || true
    command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload || true
    ;;
  purge)
    rm -f /lib/systemd/system/kizuna-eye-agent.service \
          /lib/systemd/system/kizuna-dashboard.service \
          /lib/systemd/system/kizuna-action.socket \
          /lib/systemd/system/kizuna-action.service \
          /usr/lib/tmpfiles.d/kizuna-eye.conf 2>/dev/null || true
    command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload || true
    # ヘルパーと sudoers も確実に除去する（prerm が失敗した場合の保険）。
    rm -f /etc/sudoers.d/kizuna-security-action \
          /etc/sudoers.d/kizuna-security-cron 2>/dev/null || true
    rm -f /usr/local/bin/kizuna-action.sh \
          /usr/local/bin/kizuna-cron-read.sh 2>/dev/null || true
    rm -f /usr/lib/kizuna-eye/kizuna-action.sh \
          /usr/lib/kizuna-eye/kizuna-action-server 2>/dev/null || true
    # データ・ユーザーを完全削除
    rm -rf "$DATA_ROOT" 2>/dev/null || true
    if getent passwd "$PKG_USER" >/dev/null 2>&1; then
      deluser "$PKG_USER" >/dev/null 2>&1 || true
    fi
    if getent group "$PKG_USER" >/dev/null 2>&1; then
      delgroup "$PKG_USER" >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
