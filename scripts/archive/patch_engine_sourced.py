#!/usr/bin/env python3
"""Stage1: source/trusted for alert history (engine.go)."""
import re
import shutil
import sys

PATH = "/samba/share/Kizuna-Eye/pkg/alert/engine.go"
dry = "--dry-run" in sys.argv

with open(PATH, encoding="utf-8") as f:
    src = f.read()
orig = src


def rep(old, new, label):
    global src
    n = src.count(old)
    if n != 1:
        raise SystemExit("ERROR: anchor %s count=%d" % (label, n))
    src = src.replace(old, new, 1)

# 1) ReportAlert signature
rep(
    "// ReportAlert records an externally supplied alert to history and notifies.\n"
    "// Used by plugins (e.g. security) that detect their own events.\n"
    "func (e *Engine) ReportAlert(a *notify.Alert) {\n"
    "\te.dispatch(a)\n"
    "}",
    "// ReportAlert records an externally supplied alert to history and notifies.\n"
    "// Used by plugins (e.g. security) that detect their own events.\n"
    "// source identifies the origin; trusted is true only for alerts the\n"
    "// dashboard generated itself (not from a forgeable log line).\n"
    "func (e *Engine) ReportAlert(a *notify.Alert, source string, trusted bool) {\n"
    "\te.dispatch(a, source, trusted)\n"
    "}",
    "reportalert")

# 2) dispatch signature + history call
rep(
    "// dispatch records to history and then notifies.\n"
    "func (e *Engine) dispatch(a *notify.Alert) {\n"
    "\tif a == nil {\n"
    "\t\treturn\n"
    "\t}\n"
    "\tif e.history != nil {\n"
    "\t\te.history.Add(a)",
    "// dispatch records to history and then notifies.\n"
    "func (e *Engine) dispatch(a *notify.Alert, source string, trusted bool) {\n"
    "\tif a == nil {\n"
    "\t\treturn\n"
    "\t}\n"
    "\tif e.history != nil {\n"
    "\t\te.history.AddSourced(a, source, trusted)",
    "dispatch")

# 3) pending loop: e.dispatch(a) -> dashboard, trusted
rep("\t\te.dispatch(a)", '\t\te.dispatch(a, "dashboard", true)', "call-a")

# 4) inline dispatch(&notify.Alert{...}) calls: append args at their closing.
#    Match from 'e.dispatch(&notify.Alert{' to the first '})' after it.
pattern = re.compile(
    r'(e\.dispatch\(&notify\.Alert\{.*?\n)(\s*)\}\)',
    re.DOTALL)

count = [0]

def repl(m):
    count[0] += 1
    head, indent = m.group(1), m.group(2)
    # indent is the indentation of the closing })
    return head + indent + '}, "dashboard", true)'

src, n = pattern.subn(repl, src)
if n != 3:
    raise SystemExit("ERROR: inline dispatch replacements=%d (want 3)" % n)

if src == orig:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: engine.go patch matched (reportalert+dispatch+1+3)")
    sys.exit(0)

shutil.copy2(PATH, PATH + ".prepatch-src")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: engine.go")
