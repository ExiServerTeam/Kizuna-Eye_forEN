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
    # ディレクトリは空でも残るため、明示的に除去する（dpkg は空でない
    # ディレクトリを残す。「not empty so not removed」警告の原因）。
    # /opt/kizuna-eye は bin のみ梱包のため dpkg 管理外で、plugins 配下が
    # 残ると空にならず消えない。プラグインは利用者が追加するものなので、
    # パッケージ削除時に一緒に消す（設定・データは DATA_ROOT 側にある）。
    # dpkg leaves an empty-but-untracked directory behind, and /opt/kizuna-eye
    # is not dpkg-managed (only bin is packaged), so a leftover plugins/ dir
    # keeps it from being removed. Remove both explicitly on purge.
    rm -rf /usr/lib/kizuna-eye 2>/dev/null || true
    rm -rf /opt/kizuna-eye 2>/dev/null || true
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
