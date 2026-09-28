#!/usr/bin/env python3
"""SSHログイン失敗を意図的に発生させてセキュリティプラグインを検証する。

使い方:
  KE_PASS=... python scripts/test_ssh_fail.py [回数]

存在しないユーザー名で SSH 接続を試み、auth.log に失敗ログを発生させる。
"""
import os
import sys

import paramiko


def _load_env_file():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), '.ssh_env')
    if not os.path.isfile(path):
        return
    with open(path, encoding='utf-8') as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            key, _, value = line.partition('=')
            os.environ.setdefault(key.strip(), value.strip())


_load_env_file()

HOST = os.environ.get('KE_HOST', '192.168.0.233')
PORT = int(os.environ.get('KE_PORT', '22'))


def main():
    attempts = int(sys.argv[1]) if len(sys.argv) > 1 else 6
    ok = 0
    for i in range(attempts):
        client = paramiko.SSHClient()
        client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
        try:
            client.connect(
                hostname=HOST, port=PORT,
                username='nonexistent_probe_%d' % i,
                password='invalid-password',
                timeout=8, banner_timeout=8, auth_timeout=8,
                look_for_keys=False, allow_agent=False,
            )
        except paramiko.AuthenticationException:
            ok += 1
        except Exception as e:
            print('attempt %d: %s' % (i, e), file=sys.stderr)
        finally:
            client.close()
    print('auth failures triggered: %d/%d' % (ok, attempts))
    return 0


if __name__ == '__main__':
    sys.exit(main())
