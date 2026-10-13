#!/usr/bin/env python3
"""Regression-test fixes:
1. suid.go: hasBaseline was len(known)>0, so a system with zero SUID files
t   never has a baseline and silently absorbs every new SUID file.
   Use a dedicated hasBaseline flag (also restored from the baseline file).
2. security_extra_test.go: the FIM "new watch target" now intentionally emits
a warning (by design), so the test must expect it, not silence."""
import shutil
import sys

DIR = "/samba/share/Kizuna-Security/plugin"
SUID = DIR + "/suid.go"
TEST = DIR + "/security_extra_test.go"
dry = "--dry-run" in sys.argv


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


def write(p, s):
    with open(p, "w", encoding="utf-8") as f:
        f.write(s)


def rep(src, old, new, label):
    n = src.count(old)
    if n != 1:
        raise SystemExit("ERROR: anchor %s count=%d" % (label, n))
    return src.replace(old, new, 1)

# ---- suid.go ----
suid = read(SUID)
suid = rep(suid,
           "\tinitialized bool\n}",
           "\tinitialized bool\n\t// hasBaseline is true once a baseline has been recorded (or loaded\n"
           "\t// from disk). len(known) must not be used for this: a system with zero\n"
           "\t// SUID files has an empty baseline, and that would make every new SUID\n"
           "\t// file look like \"the first baseline\" and be absorbed silently.\n"
           "\thasBaseline bool\n}",
           "struct")
suid = rep(suid,
           "\thasBaseline := len(s.known) > 0\n",
           "\thasBaseline := s.hasBaseline\n",
           "check-hasbaseline")
suid = rep(suid,
           "\tif !hasBaseline {\n\t\ts.known = current\n\t\ts.saveBaselineLocked()",
           "\tif !hasBaseline {\n\t\ts.known = current\n\t\ts.hasBaseline = true\n\t\ts.saveBaselineLocked()",
           "set-hasbaseline")
suid = rep(suid,
           "\tfor k, v := range st.Files {\n\t\ts.known[k] = v\n\t}\n}",
           "\tfor k, v := range st.Files {\n\t\ts.known[k] = v\n\t}\n\ts.hasBaseline = true\n}",
           "load-hasbaseline")

# ---- security_extra_test.go ----
test = read(TEST)
test = rep(test,
           '\tfim2.Check()\n\tif len(events) != 0 {\n\t\tt.Fatalf("newly added watch target should be silent, got: %+v", events)\n\t}',
           '\tfim2.Check()\n'
           '\t// A newly added watch target is reported (level=warning) on purpose:\n'
           '\t// the operator must be able to confirm the addition, and an attacker\n'
           '\t// must not be able to slip a file into the baseline unnoticed. This is\n'
           '\t// NOT a critical change alert.\n'
           '\tif len(events) != 1 || events[0].Level != "warning" || events[0].Category != "integrity" {\n'
           '\t\tt.Fatalf("newly added watch target should emit one integrity warning, got: %+v", events)\n'
           '\t}',
           "fim-test")

if suid == read(SUID) or test == read(TEST):
    raise SystemExit("ERROR: no change produced")

if dry:
    print("DRY-RUN OK: suid.go + security_extra_test.go patches matched")
    sys.exit(0)

shutil.copy2(SUID, SUID + ".prepatch-regr")
shutil.copy2(TEST, TEST + ".prepatch-regr")
write(SUID, suid)
write(TEST, test)
print("PATCHED: suid.go, security_extra_test.go")
