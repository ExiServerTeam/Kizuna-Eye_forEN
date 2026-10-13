#!/usr/bin/env python3
"""Test isolation fix: newTestMonitor() used DefaultConfig(), whose
SSHLoginBaselinePath is the relative "./logs/kizuna-security-logins.json".
Tests run in the package directory, so every test shared one on-disk
baseline. A test that records a "known IP" made a later test (which
expects that IP to be unknown) fail depending on run order.
Disable persistence in the test helper so each Monitor is self-contained."""
import shutil
import sys

PATH = "/samba/share/Kizuna-Security/plugin/monitor_test.go"
dry = "--dry-run" in sys.argv

with open(PATH, encoding="utf-8") as f:
    src = f.read()
orig = src

old = (
    "func newTestMonitor(minLevel string) *Monitor {\n"
    "\tcfg := DefaultConfig()\n"
    "\tcfg.NotifyMinimal = minLevel\n"
    "\treturn NewMonitor(cfg, nil, nil)\n"
    "}"
)
new = (
    "func newTestMonitor(minLevel string) *Monitor {\n"
    "\tcfg := DefaultConfig()\n"
    "\tcfg.NotifyMinimal = minLevel\n"
    "\t// Do not touch the shared on-disk login baseline: tests run in the\n"
    "\t// package directory, so the default relative path points at one shared\n"
    "\t// file. A test that records a \"known IP\" there would make a later test\n"
    "\t// (which expects that IP to be unknown) fail depending on run order.\n"
    "\tcfg.SSHLoginBaselinePath = \"\"\n"
    "\treturn NewMonitor(cfg, nil, nil)\n"
    "}"
)
if src.count(old) != 1:
    raise SystemExit("ERROR: newTestMonitor anchor count=%d" % src.count(old))
src = src.replace(old, new, 1)

if src == orig:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: monitor_test.go isolation patch matched")
    sys.exit(0)

shutil.copy2(PATH, PATH + ".prepatch-isolation")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: monitor_test.go")
