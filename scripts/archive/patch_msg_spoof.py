#!/usr/bin/env python3
"""Add spoofed.title / spoofed.msg (ja/en)."""
import shutil
import sys

PATH = "/samba/share/Kizuna-Security/plugin/messages.go"
dry = "--dry-run" in sys.argv

with open(PATH, encoding="utf-8") as f:
    src = f.read()
orig = src
lines = src.split("\n")
out = []
ja_done = en_done = False
for line in lines:
    out.append(line)
    if '"cron.unreadable.msg"' in line:
        is_ja = any('\u3040' <= ch <= '\u30ff' or '\u4e00' <= ch <= '\u9fff' for ch in line)
        indent = line[:len(line) - len(line.lstrip())]
        if is_ja and not ja_done:
            out.append(indent + '"spoofed.title": "偽装されたログを検知",')
            out.append(indent + '"spoofed.msg":   "auth.log に偽装された可能性のある行があります（journald の送信元が sshd 以外）: %s",')
            ja_done = True
        elif (not is_ja) and not en_done:
            out.append(indent + '"spoofed.title": "Forged log entry detected",')
            out.append(indent + '"spoofed.msg":   "auth.log contains a likely forged line (journald origin is not sshd): %s",')
            en_done = True
if not ja_done or not en_done:
    raise SystemExit("ERROR: anchor not found (ja=%s en=%s)" % (ja_done, en_done))
src = "\n".join(out)
if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: spoofed keys added")
    sys.exit(0)
shutil.copy2(PATH, PATH + ".prepatch-v5")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: messages.go")
