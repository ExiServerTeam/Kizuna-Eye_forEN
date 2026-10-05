#!/usr/bin/env python3
"""Stage2 (V5): detect forged auth.log lines via journald origin check."""
import shutil
import sys

MON = "/samba/share/Kizuna-Security/plugin/monitor.go"
dry = "--dry-run" in sys.argv

with open(MON, encoding="utf-8") as f:
    src = f.read()
orig = src


def rep(old, new, label):
    global src
    n = src.count(old)
    if n != 1:
        raise SystemExit("ERROR: anchor %s count=%d" % (label, n))
    src = src.replace(old, new, 1)

# 1) struct fields
rep(
    "\tloginCounts   map[string]*loginTracker\n"
    "\tloginKnownIPs map[string]bool\n}",
    "\tloginCounts   map[string]*loginTracker\n"
    "\tloginKnownIPs map[string]bool\n"
    "\n"
    "\t// spoof holds recent auth lines whose journald origin is not the real\n"
    "\t// sshd (e.g. logger -t sshd). A matching auth.log line is forged (V5).\n"
    "\tspoof       *spoofCache\n"
    "\tspoofCancel chan struct{}\n}",
    "struct")

# 2) NewMonitor init
rep(
    "\t\tloginKnownIPs:     make(map[string]bool),\n"
    "\t\tloginBaselinePath: cfg.SSHLoginBaselinePath,\n"
    "\t}\n"
    "\tm.loadLoginState()\n"
    "\treturn m\n}",
    "\t\tloginKnownIPs:     make(map[string]bool),\n"
    "\t\tloginBaselinePath: cfg.SSHLoginBaselinePath,\n"
    "\t\tspoof:             newSpoofCache(),\n"
    "\t}\n"
    "\tm.loadLoginState()\n"
    "\treturn m\n}",
    "newmonitor")

# 3) classify: before the auth.log block, check the spoof cache
rep(
    "\tif strings.Contains(path, \"auth.log\") || strings.Contains(path, \"secure\") {\n"
    "\t\tif mm := reSudo.FindStringSubmatch(trimmed); mm != nil {",
    "\tif strings.Contains(path, \"auth.log\") || strings.Contains(path, \"secure\") {\n"
    "\t\t// V5: if this exact line was seen in journald with a non-sshd\n"
    "\t\t// origin (logger -t sshd), it is forged. Report and still process\n"
    "\t\t// it so the operator sees the forged content too.\n"
    "\t\tif m.spoof != nil && m.spoof.contains(trimmed) {\n"
    "\t\t\tm.emit(module.SecurityEvent{\n"
    "\t\t\t\tCategory:  \"spoofed_log\",\n"
    "\t\t\t\tLevel:     \"critical\",\n"
    "\t\t\t\tTitle:     m.tr(\"spoofed.title\"),\n"
    "\t\t\t\tMessage:   m.tr(\"spoofed.msg\", trimmed),\n"
    "\t\t\t\tSource:    path,\n"
    "\t\t\t\tTimestamp: now,\n"
    "\t\t\t})\n"
    "\t\t}\n"
    "\t\tif mm := reSudo.FindStringSubmatch(trimmed); mm != nil {",
    "classify")

# 4) Stop method: append after Scan()
rep(
    "func (m *Monitor) Scan() {\n"
    "\tnow := time.Now()\n"
    "\tfor _, path := range m.cfg.WatchFiles {\n"
    "\t\tm.scanFile(path, now)\n"
    "\t}\n"
    "}",
    "func (m *Monitor) Scan() {\n"
    "\tnow := time.Now()\n"
    "\tfor _, path := range m.cfg.WatchFiles {\n"
    "\t\tm.scanFile(path, now)\n"
    "\t}\n"
    "}\n"
    "\n"
    "// Stop terminates the journald spoof watch, if running.\n"
    "func (m *Monitor) Stop() {\n"
    "\tif m.spoofCancel != nil {\n"
    "\t\tclose(m.spoofCancel)\n"
    "\t\tm.spoofCancel = nil\n"
    "\t}\n"
    "}",
    "stop")

if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: monitor.go spoof patch matched")
    sys.exit(0)
shutil.copy2(MON, MON + ".prepatch-v5")
with open(MON, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: monitor.go")
