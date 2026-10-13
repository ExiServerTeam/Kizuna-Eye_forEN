#!/bin/sh
# ============================================================
# Kizuna-Eye .deb prerm
# 削除の直前にサービスを停止する。
# ============================================================
set -e

case "$1" in
  remove|deconfigure)
    if command -v systemctl >/dev/null 2>&1; then
      systemctl stop kizuna-action.socket 2>/dev/null || true
      systemctl stop kizuna-action.service 2>/dev/null || true
      systemctl stop kizuna-dashboard.service 2>/dev/null || true
      systemctl stop kizuna-eye-agent.service 2>/dev/null || true
      systemctl disable kizuna-action.socket 2>/dev/null || true
      systemctl disable kizuna-dashboard.service 2>/dev/null || true
      systemctl disable kizuna-eye-agent.service 2>/dev/null || true
    fi
    # install.sh 経路が残した /etc/systemd/system の unit も止めて取り除く。
    # これが残ると systemd が /lib 版より優先し、remove 後もゾンビ化する。
    for u in kizuna-eye-agent kizuna-dashboard kizuna-action; do
      systemctl stop "$u.socket" 2>/dev/null || true
      systemctl stop "$u.service" 2>/dev/null || true
      systemctl disable "$u.socket" 2>/dev/null || true
      systemctl disable "$u.service" 2>/dev/null || true
    done
    rm -f /etc/systemd/system/kizuna-eye-agent.service \n          /etc/systemd/system/kizuna-dashboard.service \n          /etc/systemd/system/kizuna-action.service \n          /etc/systemd/system/kizuna-action.socket 2>/dev/null || true
    # NOPASSWD の sudoers ルールを残すと、存在しない/差し替えられた
    # ヘルパーへの root 実行許可が居座る。削除前に必ず外す。
    rm -f /etc/sudoers.d/kizuna-security-action \
          /etc/sudoers.d/kizuna-security-cron 2>/dev/null || true
    rm -f /usr/local/bin/kizuna-action.sh \
          /usr/local/bin/kizuna-cron-read.sh 2>/dev/null || true
    rm -f /usr/lib/kizuna-eye/kizuna-action.sh \
          /usr/lib/kizuna-eye/kizuna-action-server 2>/dev/null || true
    ;;
esac

exit 0
