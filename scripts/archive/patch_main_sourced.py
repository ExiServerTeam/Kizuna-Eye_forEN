#!/usr/bin/env python3
"""Stage1: add source/trusted to ReportAlert calls in dashboard main.go."""
import shutil
import sys

PATH = "/samba/share/Kizuna-Eye/cmd/dashboard/main.go"
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

# 167: security_ event from Agent (log-derived) -> agent, untrusted
rep(
    '\tengine.ReportAlert(&notify.Alert{\n'
    '\t\tType:      "security_" + env.Category,\n'
    '\t\tLevel:     level,\n'
    '\t\tIcon:      icon,\n'
    '\t\tTitle:     env.Title,\n'
    '\t\tMessage:   body,\n'
    '\t\tTimestamp: time.Now(),\n'
    '\t})',
    '\tengine.ReportAlert(&notify.Alert{\n'
    '\t\tType:      "security_" + env.Category,\n'
    '\t\tLevel:     level,\n'
    '\t\tIcon:      icon,\n'
    '\t\tTitle:     env.Title,\n'
    '\t\tMessage:   body,\n'
    '\t\tTimestamp: time.Now(),\n'
    '\t}, "agent", false) // agent-reported: derived from a forgeable log line',
    "security-agent")

# 796: connection_flood -> dashboard, trusted
rep(
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      "connection_flood",\n'
    '\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\tIcon:      "🚨",\n'
    '\t\t\tTitle:     "接続フラッドを検知",\n'
    '\t\t\tMessage:   fmt.Sprintf("短時間に %d 件の接続を拒否しました（%s）。接続枯渇型のDoS攻撃の可能性があります。", rejected, detail),\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t})',
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      "connection_flood",\n'
    '\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\tIcon:      "🚨",\n'
    '\t\t\tTitle:     "接続フラッドを検知",\n'
    '\t\t\tMessage:   fmt.Sprintf("短時間に %d 件の接続を拒否しました（%s）。接続枯渇型のDoS攻撃の可能性があります。", rejected, detail),\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t}, "dashboard", true)',
    "flood")

# 817: agent_token_mismatch / agent_duplicate -> dashboard, trusted
rep(
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      reason,\n'
    '\t\t\tLevel:     notify.LevelWarning,\n'
    '\t\t\tIcon:      "⚠️",\n'
    '\t\t\tTitle:     title,\n'
    '\t\t\tMessage:   fmt.Sprintf("短時間に %d 回の不正なAgent接続を拒否しました（%s）。", count, reason),\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t})',
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      reason,\n'
    '\t\t\tLevel:     notify.LevelWarning,\n'
    '\t\t\tIcon:      "⚠️",\n'
    '\t\t\tTitle:     title,\n'
    '\t\t\tMessage:   fmt.Sprintf("短時間に %d 回の不正なAgent接続を拒否しました（%s）。", count, reason),\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t}, "dashboard", true)',
    "auth-reject")

# 1043: audit_ -> dashboard, trusted
rep(
    '\t\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\t\tType:      "audit_" + kind,\n'
    '\t\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\t\tIcon:      "🚨",\n'
    '\t\t\t\tTitle:     "プラグイン操作を検知",\n'
    '\t\t\t\tMessage:   detail,\n'
    '\t\t\t\tTimestamp: time.Now(),\n'
    '\t\t\t})',
    '\t\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\t\tType:      "audit_" + kind,\n'
    '\t\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\t\tIcon:      "🚨",\n'
    '\t\t\t\tTitle:     "プラグイン操作を検知",\n'
    '\t\t\t\tMessage:   detail,\n'
    '\t\t\t\tTimestamp: time.Now(),\n'
    '\t\t\t}, "dashboard", true)',
    "audit")

# 1224: auth_ -> dashboard, trusted
rep(
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      "auth_" + kind,\n'
    '\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\tIcon:      icon,\n'
    '\t\t\tTitle:     title,\n'
    '\t\t\tMessage:   msg,\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t})',
    '\t\tengine.ReportAlert(&notify.Alert{\n'
    '\t\t\tType:      "auth_" + kind,\n'
    '\t\t\tLevel:     notify.LevelCritical,\n'
    '\t\t\tIcon:      icon,\n'
    '\t\t\tTitle:     title,\n'
    '\t\t\tMessage:   msg,\n'
    '\t\t\tTimestamp: time.Now(),\n'
    '\t\t}, "dashboard", true)',
    "auth")

if src == orig:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: main.go ReportAlert patch matched (5 edits)")
    sys.exit(0)

shutil.copy2(PATH, PATH + ".prepatch-src")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: main.go")
