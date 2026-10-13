#!/usr/bin/env python3
"""Add alerts.source_trusted / alerts.source_untrusted (ja/en)."""
import shutil
import sys

PATH = "/samba/share/Kizuna-Eye/web/static/i18n.js"
dry = "--dry-run" in sys.argv

with open(PATH, encoding="utf-8") as f:
    src = f.read()
orig = src

lines = src.split("\n")
out = []
ja_done = en_done = False
for line in lines:
    out.append(line)
    if '"alerts.cleared"' in line or "'alerts.cleared'" in line:
        is_ja = any('\u3040' <= ch <= '\u30ff' or '\u4e00' <= ch <= '\u9fff' for ch in line)
        indent = line[:len(line) - len(line.lstrip())]
        q = '"' if '"' in line else "'"
        if is_ja and not ja_done:
            out.append(indent + q + "alerts.source_trusted" + q + ": " + q + "信頼" + q + ",")
            out.append(indent + q + "alerts.source_untrusted" + q + ": " + q + "未検証" + q + ",")
            ja_done = True
        elif (not is_ja) and not en_done:
            out.append(indent + q + "alerts.source_trusted" + q + ": " + q + "Trusted" + q + ",")
            out.append(indent + q + "alerts.source_untrusted" + q + ": " + q + "Unverified" + q + ",")
            en_done = True
if not ja_done or not en_done:
    raise SystemExit("ERROR: anchor not found (ja=%s en=%s)" % (ja_done, en_done))
src = "\n".join(out)

if src == orig:
    raise SystemExit("ERROR: no change")
if dry:
    print("DRY-RUN OK: i18n keys added")
    sys.exit(0)
shutil.copy2(PATH, PATH + ".prepatch-src")
with open(PATH, "w", encoding="utf-8") as f:
    f.write(src)
print("PATCHED: i18n.js")
