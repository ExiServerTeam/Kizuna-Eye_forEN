#!/usr/bin/env python3
"""Run a command on the Kizuna-Eye server over SSH (password auth).

Usage:
  KE_PASS=... python scripts/ssh_run.py "<remote command>"

Connection settings are read from environment variables:
  KE_HOST (default 192.168.0.233), KE_USER (default user),
  KE_PASS (REQUIRED, no default), KE_PORT (default 22)

For convenience, a local file ``scripts/.ssh_env`` (git-ignored) is
loaded if present. Format: one KEY=VALUE per line, e.g.
  KE_PASS=yourpassword
"""
import os
import sys

import paramiko


def _load_env_file():
    """Load KEY=VALUE lines from scripts/.ssh_env if it exists."""
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
USER = os.environ.get('KE_USER', 'user')
PASS = os.environ.get('KE_PASS', '')
PORT = int(os.environ.get('KE_PORT', '22'))


def main():
    if len(sys.argv) < 2:
        print('usage: ssh_run.py "<remote command>"', file=sys.stderr)
        return 2

    if not PASS:
        print('KE_PASS is not set. Export it or create scripts/.ssh_env.', file=sys.stderr)
        return 2

    cmd = sys.argv[1]

    client = paramiko.SSHClient()
    # Load the user's known_hosts and require the server key to match.
    # AutoAddPolicy (the previous behaviour) accepted any host key, which
    # makes the connection trivially MITM-able. If the host is not yet
    # known, RejectPolicy aborts; add it once with `ssh-keyscan`.
    try:
        client.load_system_host_keys()
        known = os.path.join(os.path.expanduser('~'), '.ssh', 'known_hosts')
        if os.path.isfile(known):
            client.load_host_keys(known)
    except Exception:  # noqa: BLE001
        pass
    client.set_missing_host_key_policy(paramiko.RejectPolicy())
    try:
        client.connect(
            hostname=HOST, port=PORT, username=USER, password=PASS,
            timeout=10, banner_timeout=10, auth_timeout=10,
            look_for_keys=False, allow_agent=False,
        )
    except Exception as e:  # noqa: BLE001
        print('SSH connect failed: %s' % e, file=sys.stderr)
        return 1

    stdin, stdout, stderr = client.exec_command(cmd, timeout=60)
    out = stdout.read().decode('utf-8', 'replace')
    err = stderr.read().decode('utf-8', 'replace')
    rc = stdout.channel.recv_exit_status()
    client.close()

    # Write raw UTF-8 bytes so non-ASCII (e.g. emoji) never breaks on a
    # Windows console whose default codec (cp932) cannot encode them.
    sys.stdout.buffer.write(out.encode('utf-8', 'replace'))
    sys.stdout.buffer.write(b'[exit=%d]\n' % rc)
    sys.stdout.buffer.flush()
    if err:
        sys.stderr.buffer.write(err.encode('utf-8', 'replace'))
        sys.stderr.buffer.flush()
    return 0


if __name__ == '__main__':
    sys.exit(main())
