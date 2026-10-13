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
    ;;
esac

exit 0
