#!/usr/bin/env python3
"""Fix: classify gets the full auth.log line; match by substring."""
import shutil
import sys

SP = "/samba/share/Kizuna-Security/plugin/monitor_spoof.go"
dry = "--dry-run" in sys.argv

with open(SP, encoding="utf-8") as f:
    src = f.read()
orig = src

old = (
    "// contains reports whether msg was recently seen as a forged auth line.\n"
    "func (c *spoofCache) contains(msg string) bool {\n"
    "\tc.mu.Lock()\n"
    "\tdefer c.mu.Unlock()\n"
    "\tt, ok := c.entries[msg]\n"
    "\tif !ok {\n"
    "\t\treturn false\n"
    "\t}\n"
    "\tif time.Since(t) > spoofTTL {\n"
    "\t\tdelete(c.entries, msg)\n"
    "\t\treturn false\n"
    "\t}\n"
    "\treturn true\n"
    "}"
)
new = (
    "// contains reports whether line was recently seen as a forged auth line.\n"
    "// The journald MESSAGE is the body only, while the auth.log line carries a\n"
    "// timestamp/host/tag prefix, so match by substring.\n"
    "func (c *spoofCache) contains(line string) bool {\n"
    "\tc.mu.Lock()\n"
    "\tdefer c.mu.Unlock()\n"
    "\tnow := time.Now()\n"
    "\tfor msg, t := range c.entries {\n"
    "\t\tif now.Sub(t) > spoofTTL {\n"
    "\t\t\tdelete(c.entries, msg)\n"
    "\t\t\tcontinue\n"
    "\t\t}\n"
    "\t\tif msg != \"\" && strings.Contains(line, msg) {\n"
    "\t\t\treturn true\n"
    "\t\t}\n"
    "\t}\n"
    "\treturn false\n"
    "}"
)
if src.count(old) != 1:
    raise SystemExit("ERROR: contains anchor count=%d" % src.count(old))
src = src.replace(old, new, 1)

# add strings import if missing
if '"strings"' not in src:
    src = src.replace('import (\n\t"bufio"\n\t"context"', 'import (\n\t"bufio"\n\t"context"\n\t"strings"', 1)

if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: contains substring fix matched")
    sys.exit(0)
shutil.copy2(SP, SP + ".prepatch-substr")
with open(SP, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: monitor_spoof.go")
