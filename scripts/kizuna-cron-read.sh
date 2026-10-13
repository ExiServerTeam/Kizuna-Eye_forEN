#!/bin/bash
# Kizuna-Security: crontab のファイル名と内容ハッシュを出力（読み取り専用）
# V3: all external commands use absolute paths to avoid PATH hijacking
# when run under sudo (in case sudoers does not set secure_path).
DIR=/var/spool/cron/crontabs
[ -d "$DIR" ] || exit 0
for f in "$DIR"/*; do
  [ -f "$f" ] || continue
  base=$(/usr/bin/basename -- "$f")
  case "$base" in *[!A-Za-z0-9_.-]*) continue ;; esac
  h=$(/usr/bin/sha256sum -- "$f" 2>/dev/null | /usr/bin/cut -d' ' -f1)
  [ -n "$h" ] && /usr/bin/printf '%s\t%s\n' "$base" "$h"
done
