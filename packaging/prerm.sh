#!/bin/sh
# ============================================================
# Kizuna-Eye .deb prerm
# 削除の直前にサービスを停止する。
# ============================================================
set -e

case "$1" in
  remove|deconfigure)
    if command -v systemctl >/dev/null 2>&1; then
      systemctl stop kizuna-dashboard.service 2>/dev/null || true
      systemctl stop kizuna-eye-agent.service 2>/dev/null || true
      systemctl disable kizuna-dashboard.service 2>/dev/null || true
      systemctl disable kizuna-eye-agent.service 2>/dev/null || true
    fi
    # NOPASSWD の sudoers ルールを残すと、存在しない/差し替えられた
    # ヘルパーへの root 実行許可が居座る。削除前に必ず外す。
    rm -f /etc/sudoers.d/kizuna-security-action \
          /etc/sudoers.d/kizuna-security-cron 2>/dev/null || true
    rm -f /usr/local/bin/kizuna-action.sh \
          /usr/local/bin/kizuna-cron-read.sh 2>/dev/null || true
    ;;
esac

exit 0
