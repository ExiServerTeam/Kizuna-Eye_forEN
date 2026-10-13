#!/usr/bin/env python3
"""Start the journald spoof watch from Configure (after the monitor exists)."""
import shutil
import sys

PATH = "/samba/share/Kizuna-Security/plugin/plugin.go"
dry = "--dry-run" in sys.argv

with open(PATH, encoding="utf-8") as f:
    src = f.read()
orig = src

old = "\tmon := NewMonitor(cfg, fl, p.logger)\n"
new = "\tmon := NewMonitor(cfg, fl, p.logger)\n"
if src.count(old) != 1:
    raise SystemExit("ERROR: NewMonitor anchor count=%d" % src.count(old))
src = src.replace(old, new, 1)

# Stop the previous monitor's spoof watch before replacing it, and start a
# new one for this monitor right after it is created.
old2 = "\tp.monitor = mon\n"
new2 = ("\t// Reconfigure: stop the previous journald spoof watch, if any.\n"
        "\tif p.monitor != nil {\n"
        "\t\tp.monitor.Stop()\n"
        "\t}\n"
        "\tp.monitor = mon\n"
        "\tmon.startSpoofWatch(p.ctx)\n")
if src.count(old2) != 1:
    raise SystemExit("ERROR: p.monitor anchor count=%d" % src.count(old2))
src = src.replace(old2, new2, 1)

if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: plugin.go spoof start matched")
    sys.exit(0)
shutil.copy2(PATH, PATH + ".prepatch-v5s")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: plugin.go")
