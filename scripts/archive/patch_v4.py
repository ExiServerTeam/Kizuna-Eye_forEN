#!/usr/bin/env python3
"""V4: alert on suspicious agent connections (token mismatch / duplicate).
Adds a per-reason rate-limited alert callback, mirroring noteRejected."""
import shutil
import sys

MAIN = "/samba/share/Kizuna-Eye/cmd/dashboard/main.go"
dry = "--dry-run" in sys.argv


def read():
    with open(MAIN, encoding="utf-8") as f:
        return f.read()


def write(s):
    with open(MAIN, "w", encoding="utf-8") as f:
        f.write(s)


def rep(src, old, new, label):
    n = src.count(old)
    if n != 1:
        raise SystemExit("ERROR: anchor %s count=%d" % (label, n))
    return src.replace(old, new, 1)


src = read()
orig = src

# 1) struct fields (after floodWindowAt)
src = rep(src,
          "\tfloodMu       sync.Mutex\n\tfloodCount    int\n\tfloodWindowAt time.Time\n}",
          "\tfloodMu       sync.Mutex\n\tfloodCount    int\n\tfloodWindowAt time.Time\n"
          "\n"
          "\t// onAuthReject is called (at most once per authRejectWindow and\n"
          "\t// per reason) when suspicious agent connections are rejected.\n"
          "\t// Without it, token brute-force / agent hijack attempts were only\n"
          "\t// WARN lines in the log and never reached the operator.\n"
          "\tonAuthReject func(reason string, count int)\n"
          "\t// authRejectMu guards authRejects below.\n"
          "\tauthRejectMu sync.Mutex\n"
          "\tauthRejects  map[string]*authRejectCounter\n}",
          "struct")

# 2) consts + noteAuthReject + SetAuthRejectCallback, before SetFloodCallback
src = rep(src,
          "// SetFloodCallback installs the callback invoked when a connection flood is\n",
          "// authRejectThreshold is how many suspicious agent connections with the\n"
          "// SAME reason within authRejectWindow raise one alert, and authRejectWindow\n"
          "// is the measurement window. 10 attempts in 60s is well above normal\n"
          "// (a healthy agent connects once) but below a real brute-force burst.\n"
          "const (\n"
          "\tauthRejectThreshold = 10\n"
          "\tauthRejectWindow    = 60 * time.Second\n"
          ")\n"
          "\n"
          "// authRejectCounter tracks one rejection reason's windowed count.\n"
          "type authRejectCounter struct {\n"
          "\tcount    int\n"
          "\twindowAt time.Time\n"
          "}\n"
          "\n"
          "// noteAuthReject records a suspicious agent connection and fires\n"
          "// onAuthReject when the threshold is reached. The window resets after\n"
          "// each alert so the alert is raised at most once per window per reason.\n"
          "func (h *Hub) noteAuthReject(reason string) {\n"
          "\th.authRejectMu.Lock()\n"
          "\tif h.authRejects == nil {\n"
          "\t\th.authRejects = make(map[string]*authRejectCounter)\n"
          "\t}\n"
          "\tnow := time.Now()\n"
          "\tc := h.authRejects[reason]\n"
          "\tif c == nil || now.Sub(c.windowAt) > authRejectWindow {\n"
          "\t\tc = &authRejectCounter{windowAt: now}\n"
          "\t\th.authRejects[reason] = c\n"
          "\t}\n"
          "\tc.count++\n"
          "\tcount := c.count\n"
          "\tfire := count == authRejectThreshold\n"
          "\tcb := h.onAuthReject\n"
          "\th.authRejectMu.Unlock()\n"
          "\tif fire && cb != nil {\n"
          "\t\tcb(reason, count)\n"
          "\t}\n"
          "}\n"
          "\n"
          "// SetAuthRejectCallback installs the callback invoked when suspicious\n"
          "// agent connections are detected. Call once during startup.\n"
          "func (h *Hub) SetAuthRejectCallback(fn func(reason string, count int)) {\n"
          "\th.authRejectMu.Lock()\n"
          "\th.onAuthReject = fn\n"
          "\th.authRejectMu.Unlock()\n"
          "}\n"
          "\n"
          "// SetFloodCallback installs the callback invoked when a connection flood is\n",
          "methods")

# 3) token mismatch: add noteAuthReject call
src = rep(src,
          "\t\t\t\tlg.Warn(\"Agent トークン不一致の接続を拒否: %s\", r.RemoteAddr)\n\t\t\t\treturn",
          "\t\t\t\tlg.Warn(\"Agent トークン不一致の接続を拒否: %s\", r.RemoteAddr)\n"
          "\t\t\t\thub.noteAuthReject(\"agent_token_mismatch\")\n"
          "\t\t\t\treturn",
          "token-mismatch")

# 4) duplicate agent: add noteAuthReject call
src = rep(src,
          "\t\t\t\t\tlg.Warn(\"既にAgent接続があるため、2つ目のAgent接続を拒否: %s\", conn.RemoteAddr())\n",
          "\t\t\t\t\tlg.Warn(\"既にAgent接続があるため、2つ目のAgent接続を拒否: %s\", conn.RemoteAddr())\n"
          "\t\t\t\t\thub.noteAuthReject(\"agent_duplicate\")\n",
          "duplicate")

# 5) main(): install the callback after the flood callback block
src = rep(src,
          "\t\tlg.Warn(\"接続フラッド検知: %d 件拒否 (%s)\", rejected, detail)\n\t})\n",
          "\t\tlg.Warn(\"接続フラッド検知: %d 件拒否 (%s)\", rejected, detail)\n\t})\n"
          "\t// Raise a warning when suspicious agent connections (token mismatch\n"
          "\t// or a duplicate agent) are rejected, so brute-force / hijack attempts\n"
          "\t// reach the operator instead of only the log.\n"
          "\thub.SetAuthRejectCallback(func(reason string, count int) {\n"
          "\t\ttitle := \"不正なAgent接続を検知\"\n"
          "\t\tswitch reason {\n"
          "\t\tcase \"agent_token_mismatch\":\n"
          "\t\t\ttitle = \"Agentトークン不一致（偽装の可能性）\"\n"
          "\t\tcase \"agent_duplicate\":\n"
          "\t\t\ttitle = \"2つ目のAgent接続（乗っ取りの可能性）\"\n"
          "\t\t}\n"
          "\t\tengine.ReportAlert(&notify.Alert{\n"
          "\t\t\tType:      reason,\n"
          "\t\t\tLevel:     notify.LevelWarning,\n"
          "\t\t\tIcon:      \"⚠️\",\n"
          "\t\t\tTitle:     title,\n"
          "\t\t\tMessage:   fmt.Sprintf(\"短時間に %d 回の不正なAgent接続を拒否しました（%s）。\", count, reason),\n"
          "\t\t\tTimestamp: time.Now(),\n"
          "\t\t})\n"
          "\t\tlg.Warn(\"不正Agent接続検知: %s x%d\", reason, count)\n"
          "\t})\n",
          "main-callback")

if src == orig:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: main.go V4 patch matched (5 edits)")
    sys.exit(0)

shutil.copy2(MAIN, MAIN + ".prepatch-v4")
write(src)
print("PATCHED: main.go")
