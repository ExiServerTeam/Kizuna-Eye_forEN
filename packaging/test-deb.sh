#!/bin/bash
# ============================================================
# Kizuna-Eye .deb を隔離コンテナで検証する。
#   1. クリーンな ubuntu コンテナに .deb を導入
#   2. ユーザー・ディレクトリ・設定・unit を確認
#   3. dashboard を起動して /setup に到達できるか確認
#
# 使い方（root の docker が必要）:
#   sudo ./packaging/test-deb.sh [ubuntu:24.04]
# ============================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${1:-ubuntu:24.04}"
DEB="$(ls -1 "$ROOT"/dist/kizuna-eye_*_amd64.deb 2>/dev/null | head -n1 || true)"
if [ -z "$DEB" ]; then
    echo "❌ dist/ に .deb がありません。先に ./packaging/build-deb.sh を実行してください。" >&2
    exit 1
fi

echo "▶ image=$IMAGE deb=$(basename "$DEB")"
docker run --rm -v "$ROOT/dist:/dist" "$IMAGE" bash -c '
set -e
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq /dist/kizuna-eye_*_amd64.deb >/dev/null 2>&1

echo "=== user ==="
id kizuna-eye

echo "=== data ==="
ls -l /var/lib/kizuna-eye/data

echo "=== config perms (expect 600) ==="
stat -c "%a %U:%G %n" /var/lib/kizuna-eye/data/*.json

echo "=== auth / language ==="
grep -E "\"enabled\"|\"language\"|\"require_signature\"|\"static_dir\"" /var/lib/kizuna-eye/data/dashboard_config.json | head -5

echo "=== units ==="
ls -l /lib/systemd/system/kizuna* 2>&1 | head
grep -h "ReadWritePaths" /usr/share/kizuna-eye/systemd/*.service

echo "=== run dashboard (as kizuna-eye) ==="
# コンテナには curl/sudo が無いため runuser + python3 で確認する。
if command -v runuser >/dev/null 2>&1; then
  RUN="runuser -u kizuna-eye --"
else
  RUN="su -s /bin/sh kizuna-eye -c"
fi
$RUN /opt/kizuna-eye/bin/dashboard_linux -config /var/lib/kizuna-eye/data/dashboard_config.json >/tmp/dash.log 2>&1 &
sleep 8
python3 - <<'PY'
import urllib.request, urllib.error
for name, url in [("root", "http://127.0.0.1:8080/"), ("setup", "http://127.0.0.1:8080/setup.html")]:
    try:
        r = urllib.request.urlopen(url, timeout=5)
        print(f"{name}={r.status}")
    except urllib.error.HTTPError as e:
        print(f"{name}={e.code}")
    except Exception as e:
        print(f"{name}=ERR {e}")
PY

echo "=== dashboard process ==="
ps -ef | grep -i dashboard_linux | grep -v grep || echo "(no process)"
echo "=== listening ports ==="
(ss -ltnp 2>/dev/null || netstat -ltnp 2>/dev/null) | head || true
echo "=== dashboard log (stdout) ==="
cat /tmp/dash.log || true
echo "=== dashboard.log (file) ==="
cat /var/lib/kizuna-eye/data/dashboard.log 2>/dev/null || echo "(none)"
echo "=== log_file setting ==="
grep -E "log_file|listen_addr" /var/lib/kizuna-eye/data/dashboard_config.json
'
