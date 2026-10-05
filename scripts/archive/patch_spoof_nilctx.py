#!/usr/bin/env python3
"""Fix: startSpoofWatch(nil ctx) panicked in exec.CommandContext."""
import shutil
import sys

SP = "/samba/share/Kizuna-Security/plugin/monitor_spoof.go"
PL = "/samba/share/Kizuna-Security/plugin/plugin.go"
dry = "--dry-run" in sys.argv


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


def write(p, s):
    with open(p, "w", encoding="utf-8") as f:
        f.write(s)

# 1) nil-ctx guard in startSpoofWatch
sp = read(SP)
old = ("func (m *Monitor) startSpoofWatch(ctx context.Context) {\n"
       "\tif _, err := exec.LookPath(\"journalctl\"); err != nil {")
new = ("func (m *Monitor) startSpoofWatch(ctx context.Context) {\n"
       "\t// Configure may run before Init, so ctx can be nil here. Never pass a\n"
       "\t// nil context to exec.CommandContext (it panics).\n"
       "\tif ctx == nil {\n"
       "\t\tctx = context.Background()\n"
       "\t}\n"
       "\tif _, err := exec.LookPath(\"journalctl\"); err != nil {")
if sp.count(old) != 1:
    raise SystemExit("ERROR: spoof anchor count=%d" % sp.count(old))
sp = sp.replace(old, new, 1)

# 2) plugin.go: pass context.Background() (Init-time ctx is not available yet)
pl = read(PL)
old2 = "\tmon.startSpoofWatch(p.ctx)\n"
new2 = ("\t// Configure runs before Init, so p.ctx may be nil; pass a safe\n"
        "\t// background context and rely on Stop() for cancellation.\n"
        "\tmon.startSpoofWatch(context.Background())\n")
if pl.count(old2) != 1:
    raise SystemExit("ERROR: plugin anchor count=%d" % pl.count(old2))
pl = pl.replace(old2, new2, 1)

if sp == read(SP) and pl == read(PL):
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: nil-ctx fix matched")
    sys.exit(0)

shutil.copy2(SP, SP + ".prepatch-nilctx")
shutil.copy2(PL, PL + ".prepatch-nilctx")
write(SP, sp)
write(PL, pl)
print("PATCHED: monitor_spoof.go, plugin.go")
