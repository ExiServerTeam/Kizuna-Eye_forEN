#!/usr/bin/env python3
"""Fix false positives: only flag entries that CLAIM to be sshd."""
import shutil
import sys

SP = "/samba/share/Kizuna-Security/plugin/monitor_spoof.go"
dry = "--dry-run" in sys.argv

with open(SP, encoding="utf-8") as f:
    src = f.read()
orig = src

old = (
    "\t\t// A genuine sshd entry: _COMM=sshd and _UID=0. Anything else that\n"
    "\t\t// claims SYSLOG_IDENTIFIER=sshd is a forgery (e.g. logger -t sshd).\n"
    "\t\tif e.Comm != \"sshd\" || e.UID != \"0\" {\n"
)
new = (
    "\t\t// Only entries that CLAIM to be sshd (SYSLOG_IDENTIFIER=sshd) but\n"
    "\t\t// whose real origin is not the privileged sshd process are forgeries\n"
    "\t\t// (e.g. logger -t sshd). Legitimate sudo/systemd auth entries have a\n"
    "\t\t// different SYSLOG_IDENTIFIER and must NOT be flagged.\n"
    "\t\tif e.SyslogIdentifier == \"sshd\" && (e.Comm != \"sshd\" || e.UID != \"0\") {\n"
)
if src.count(old) != 1:
    raise SystemExit("ERROR: filter anchor count=%d" % src.count(old))
src = src.replace(old, new, 1)

if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: spoof filter fix matched")
    sys.exit(0)
shutil.copy2(SP, SP + ".prepatch-filter")
with open(SP, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: monitor_spoof.go")
